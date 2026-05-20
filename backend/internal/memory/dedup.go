// Package memory — dedup.go: LLM-driven conflict resolution between newly
// extracted atoms and the persona's existing long-term memory.
//
// We assemble a "candidate pool" via BM25 (see Service.CandidatesForDedup)
// then ask the LLM, in a single batch call, to decide for each NEW atom
// whether to:
//
//   store   — completely new, write a fresh row
//   skip    — fully redundant with an existing atom, drop the new one
//   update  — the new atom is a more precise/recent version of one existing
//             atom; rewrite that row's content
//   merge   — multiple old atoms + the new one collapse into a single
//             consolidated statement; create one new row and supersede the
//             old ones
//
// All actions happen in a single transaction in ApplyDedupActions.

package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/llm"
	"github.com/opentheone/opentheone/backend/internal/model"
)

// DedupAction is one decision from the LLM about what to do with one
// extracted atom relative to the candidate pool.
type DedupAction struct {
	// NewIndex is the 0-based position of the new atom in the input list.
	NewIndex int `json:"new_index"`
	// Action is store / skip / update / merge.
	Action string `json:"action"`
	// TargetIDs is the existing memory_id(s) involved:
	//   - "update" expects exactly one
	//   - "merge"  expects one or more (all will be superseded)
	//   - "store" / "skip" expects none
	TargetIDs []string `json:"target_ids"`
	// FinalContent is the final atom text. For "store" it's the new atom
	// (optionally reworded by the LLM); for "update" / "merge" it's the
	// consolidated version. Empty / whitespace-only is treated as "skip".
	FinalContent string `json:"final_content"`
	// FinalImportance — final importance after consolidation. <=0 means
	// keep the new atom's value.
	FinalImportance int `json:"final_importance"`
	// FinalKind — usually preserved from the new atom; the LLM may
	// reclassify when merging across kinds.
	FinalKind string `json:"final_kind"`
}

// dedupBatchResult is the LLM output envelope.
type dedupBatchResult struct {
	Actions []DedupAction `json:"actions"`
}

// IngestMeta carries the contextual fields the dedup applier needs when
// it materialises new rows. PersonaID is mandatory; the others may be
// blank for manual / cron-driven ingests.
type IngestMeta struct {
	PersonaID       string
	ConversationID  string
	SourceMessageID string
	ActivityAt      time.Time
}

// DedupExtracted runs the LLM dedup decision and returns the actions to
// apply. Pure decision step — does not mutate the DB.
//
// If `candidates` is empty, returns a trivial "store all" action set without
// hitting the LLM (saves a round-trip on cold-start where the persona has
// zero existing memories).
func (s *Service) DedupExtracted(ctx context.Context, client *llm.Client, newAtoms []ExtractedAtom, candidates []model.Memory) ([]DedupAction, error) {
	if len(newAtoms) == 0 {
		return nil, nil
	}
	if len(candidates) == 0 {
		actions := make([]DedupAction, 0, len(newAtoms))
		for i, a := range newAtoms {
			actions = append(actions, DedupAction{
				NewIndex:        i,
				Action:          "store",
				FinalContent:    a.Content,
				FinalImportance: a.Importance,
				FinalKind:       a.Kind,
			})
		}
		return actions, nil
	}
	if client == nil {
		return nil, errors.New("memory.DedupExtracted: nil llm client (candidates exist, need LLM judgment)")
	}

	var b strings.Builder
	b.WriteString("【已有记忆候选】\n")
	for _, c := range candidates {
		fmt.Fprintf(&b, "[%s | kind=%s | imp=%d] %s\n", c.ID, c.Kind, c.Importance, strings.TrimSpace(c.Content))
	}
	b.WriteString("\n【新抽取的待处理记忆】\n")
	for i, a := range newAtoms {
		fmt.Fprintf(&b, "%d. [kind=%s | imp=%d] %s\n", i, a.Kind, a.Importance, strings.TrimSpace(a.Content))
	}
	b.WriteString("\n请按要求输出 JSON。")

	msgs := []llm.ChatMessage{
		{Role: "system", Content: dedupSystemPrompt},
		{Role: "user", Content: b.String()},
	}
	raw, err := client.Chat(ctx, msgs)
	if err != nil {
		return nil, fmt.Errorf("memory.DedupExtracted: chat: %w", err)
	}
	raw = stripJSONFence(raw)
	var out dedupBatchResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("memory.DedupExtracted: parse json: %w (raw=%q)", err, raw)
	}
	cleaned := make([]DedupAction, 0, len(out.Actions))
	for _, a := range out.Actions {
		a.Action = strings.ToLower(strings.TrimSpace(a.Action))
		a.FinalContent = strings.TrimSpace(a.FinalContent)
		a.FinalKind = normaliseKind(a.FinalKind)
		switch a.Action {
		case "store", "skip", "update", "merge":
			// ok
		default:
			a.Action = "store"
		}
		cleaned = append(cleaned, a)
	}
	return cleaned, nil
}

// ApplyDedupActions executes the decisions in a single GORM transaction and
// returns the IDs of new rows that were created (caller can hand them to
// the L2 scene fitter immediately).
func (s *Service) ApplyDedupActions(ctx context.Context, actions []DedupAction, newAtoms []ExtractedAtom, meta IngestMeta) ([]string, error) {
	if len(actions) == 0 {
		return nil, nil
	}
	type postOp struct {
		kind      string // "index" | "delete"
		memID     string
		personaID string
		content   string
	}
	var (
		createdIDs []string
		postOps    []postOp
	)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, action := range actions {
			if action.NewIndex < 0 || action.NewIndex >= len(newAtoms) {
				continue
			}
			atom := newAtoms[action.NewIndex]
			finalContent := pickContent(action.FinalContent, atom.Content)
			finalKind := action.FinalKind
			if finalKind == "" {
				finalKind = atom.Kind
			}
			finalImp := action.FinalImportance
			if finalImp <= 0 {
				finalImp = atom.Importance
			}
			if finalImp <= 0 || finalImp > 10 {
				finalImp = 5
			}

			switch action.Action {
			case "skip":
				// nothing
			case "store":
				mem := buildMem(meta, finalKind, finalContent, finalImp, atom)
				if err := tx.Create(&mem).Error; err != nil {
					return err
				}
				createdIDs = append(createdIDs, mem.ID)
				postOps = append(postOps, postOp{kind: "index", memID: mem.ID, personaID: meta.PersonaID, content: finalContent})
			case "update":
				if len(action.TargetIDs) != 1 {
					// Bad LLM output — degrade to store.
					mem := buildMem(meta, finalKind, finalContent, finalImp, atom)
					if err := tx.Create(&mem).Error; err != nil {
						return err
					}
					createdIDs = append(createdIDs, mem.ID)
					postOps = append(postOps, postOp{kind: "index", memID: mem.ID, personaID: meta.PersonaID, content: finalContent})
					continue
				}
				updates := map[string]interface{}{"content": finalContent, "importance": finalImp}
				if action.FinalKind != "" {
					updates["kind"] = finalKind
				}
				if err := tx.Model(&model.Memory{}).
					Where("id = ?", action.TargetIDs[0]).
					Updates(updates).Error; err != nil {
					return err
				}
				postOps = append(postOps, postOp{kind: "index", memID: action.TargetIDs[0], personaID: meta.PersonaID, content: finalContent})
			case "merge":
				mem := buildMem(meta, finalKind, finalContent, finalImp, atom)
				if err := tx.Create(&mem).Error; err != nil {
					return err
				}
				createdIDs = append(createdIDs, mem.ID)
				postOps = append(postOps, postOp{kind: "index", memID: mem.ID, personaID: meta.PersonaID, content: finalContent})
				if len(action.TargetIDs) > 0 {
					if err := tx.Model(&model.Memory{}).
						Where("id IN ?", action.TargetIDs).
						Updates(map[string]interface{}{
							"status":        "superseded",
							"superseded_by": mem.ID,
						}).Error; err != nil {
						return err
					}
					for _, sid := range action.TargetIDs {
						postOps = append(postOps, postOp{kind: "delete", memID: sid})
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// FTS index passes run post-commit; failure here is logged-and-forgotten
	// (next IndexMemory on the same row will heal it).
	for _, op := range postOps {
		switch op.kind {
		case "index":
			_ = s.bm25.IndexMemory(ctx, op.memID, op.personaID, op.content)
		case "delete":
			_ = s.bm25.DeleteMemory(ctx, op.memID)
		}
	}
	return createdIDs, nil
}

func buildMem(meta IngestMeta, kind, content string, imp int, atom ExtractedAtom) model.Memory {
	return model.Memory{
		PersonaID:       meta.PersonaID,
		ConversationID:  meta.ConversationID,
		SourceMessageID: meta.SourceMessageID,
		Kind:            kind,
		Content:         content,
		Importance:      imp,
		ActivityStart:   atom.ActivityStart,
		ActivityEnd:     atom.ActivityEnd,
		Status:          "active",
		Metadata:        "{}",
	}
}

func pickContent(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return strings.TrimSpace(primary)
	}
	return strings.TrimSpace(fallback)
}

const dedupSystemPrompt = `你是 OpenTheOne 的「长期记忆冲突仲裁器」。
输入是这位用户已有的若干「候选记忆」（带 id），以及一组新抽取的「待处理记忆」（按下标 0,1,2,… 编号）。
请你按顺序判断每一条「待处理记忆」应该如何写入。

【输出格式】严格输出一个 JSON 对象：
{
  "actions": [
    {
      "new_index": 0,                       // 对应新抽取记忆的下标
      "action":    "store|skip|update|merge",
      "target_ids": ["uuid", ...],          // store/skip 为空数组；update 必须 1 个；merge 至少 1 个
      "final_content":   "最终写入的内容（≤80字）",
      "final_importance": 1-10 的整数，可保持新值
      "final_kind":      "persona|episodic|instruction"
    }
  ]
}

【动作语义】
- store：候选中没有同义条目，直接新建。final_content 可以与原文一致或微调措辞。
- skip：候选中已有一条完全包含同一信息的条目，丢弃新条目，不要 update（不需要重写）。
- update：候选中正好有一条同义但措辞旧/不够精确的，把该条 (target_ids 单元素) 改写为 final_content。
- merge：多条候选 + 这条新条目讲的是同一个事实的不同侧面，归并成一条新条目并把所有 target_ids 标记为 superseded。

【判定原则】
1. 不确定时优先 store，不要乱合并不同事实。
2. 同样是「用户喜欢手冲咖啡」与「用户偏爱 Aeropress」——是相关但不同事实，应分别 store 而非 merge。
3. 「用户喜欢手冲咖啡」与「用户喜欢手冲」——同义，应 skip 或 update。
4. instruction 类（对 AI 的长期指令）若发生冲突（旧：叫全名；新：叫小名）必须 update 为最新值。
5. final_content 保持一条事实，不要拼接成长句。
6. 只输出 JSON，不要解释。`

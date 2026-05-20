// Package memory — profile.go: L3 user profile synthesis.
//
// One UserProfile row per persona. The LLM is asked to consolidate the
// persona's L1 atoms + L2 scenes + recent L0 messages into a single ≤2000
// char markdown narrative covering:
//
//   - 基本资料   (name / nickname / role / location / 重要时间点)
//   - 长期偏好   (cuisine / hobby / 节奏 / 沟通风格)
//   - 关键关系   (friends / family / pets that come up)
//   - 行动模式   (作息 / 工作节奏 / 健身 / 阅读)
//   - 当前焦点   (近 30 天最热的主题)
//   - 对 AI 的长期指令 (称呼 / 语气 / 边界 / 禁忌)
//
// Why 2000 chars? It needs to fit in the system-prompt header (cache-
// friendly) without crowding out the actual instructions. Empirically this
// is also where prompt compliance starts to degrade — past ~600 lines of
// system prompt the model begins ignoring sections at the bottom.

package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/llm"
	"github.com/opentheone/opentheone/backend/internal/model"
)

// MaxProfileChars hard-caps L3 profile length. We truncate post-LLM as a
// belt-and-suspenders measure (the prompt also asks for ≤2000 chars).
const MaxProfileChars = 2000

// GetProfile returns the persona's current L3 profile, or (nil, nil) when
// it hasn't been generated yet.
func (s *Service) GetProfile(ctx context.Context, personaID string) (*model.UserProfile, error) {
	var p model.UserProfile
	err := s.db.WithContext(ctx).Where("persona_id = ?", personaID).First(&p).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// ProfileForPrompt returns the profile body suitable for system-prompt
// injection (always wrapped with a heading so the model knows what it is).
// Returns empty string when no profile exists yet — caller should skip the
// injection block in that case.
func (s *Service) ProfileForPrompt(ctx context.Context, personaID string) (string, error) {
	p, err := s.GetProfile(ctx, personaID)
	if err != nil {
		return "", err
	}
	if p == nil || strings.TrimSpace(p.Content) == "" {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("【你与这位用户的长期画像】（基于历次对话沉淀，不要照念，请自然内化）\n")
	b.WriteString(p.Content)
	b.WriteString("\n")
	return b.String(), nil
}

// RegenerateProfile runs the L3 synthesis LLM call and overwrites the
// persona's UserProfile row. Caller is responsible for the rate-limiting
// decision (see pipeline.go); this method always runs the LLM when invoked.
//
// `reason` is stored alongside the profile for debugging ("threshold",
// "cold-start", "manual", "scene-merge-rebalance", …).
func (s *Service) RegenerateProfile(ctx context.Context, client *llm.Client, personaID, reason string) (*model.UserProfile, error) {
	if client == nil {
		return nil, errors.New("memory.RegenerateProfile: nil llm client")
	}

	// Pull the materials in one pass — bounded numbers so the prompt is
	// O(n) on persona size, never blowing up for long-time users.
	const (
		topScenes        = MaxScenesPerPersona // already capped
		topPersonaAtoms  = 60                  // stable attributes
		topInstrAtoms    = 20                  // AI instructions
		topEpisodicAtoms = 20                  // most recent events
	)
	var scenes []model.MemoryScene
	if err := s.db.WithContext(ctx).
		Where("persona_id = ?", personaID).
		Order("heat desc, updated_at desc").
		Limit(topScenes).Find(&scenes).Error; err != nil {
		return nil, err
	}
	var personaAtoms []model.Memory
	if err := s.db.WithContext(ctx).
		Where("persona_id = ? AND status = ? AND kind = ?", personaID, "active", "persona").
		Order("importance desc, created_at desc").
		Limit(topPersonaAtoms).Find(&personaAtoms).Error; err != nil {
		return nil, err
	}
	var instrAtoms []model.Memory
	if err := s.db.WithContext(ctx).
		Where("persona_id = ? AND status = ? AND kind = ?", personaID, "active", "instruction").
		Order("importance desc, created_at desc").
		Limit(topInstrAtoms).Find(&instrAtoms).Error; err != nil {
		return nil, err
	}
	var episodicAtoms []model.Memory
	if err := s.db.WithContext(ctx).
		Where("persona_id = ? AND status = ? AND kind = ?", personaID, "active", "episodic").
		Order("created_at desc").
		Limit(topEpisodicAtoms).Find(&episodicAtoms).Error; err != nil {
		return nil, err
	}

	// Carry over the previous profile so the LLM can do incremental edits
	// rather than rewriting from scratch — keeps prose style continuity.
	prev, _ := s.GetProfile(ctx, personaID)

	var b strings.Builder
	if prev != nil && strings.TrimSpace(prev.Content) != "" {
		b.WriteString("【上一版画像（如果只是细节微调，请尽量保留段落与措辞，只改动该改的部分）】\n")
		b.WriteString(prev.Content)
		b.WriteString("\n\n")
	}
	b.WriteString("【L2 主题场景】\n")
	if len(scenes) == 0 {
		b.WriteString("（空）\n")
	}
	for _, sc := range scenes {
		fmt.Fprintf(&b, "- %s — %s\n", sc.Title, oneLine(sc.Summary))
	}
	b.WriteString("\n【L1 persona 类原子记忆（按重要性排序）】\n")
	for _, a := range personaAtoms {
		fmt.Fprintf(&b, "- [imp=%d] %s\n", a.Importance, strings.TrimSpace(a.Content))
	}
	b.WriteString("\n【L1 instruction 类原子记忆（用户对 AI 的长期指令）】\n")
	for _, a := range instrAtoms {
		fmt.Fprintf(&b, "- [imp=%d] %s\n", a.Importance, strings.TrimSpace(a.Content))
	}
	b.WriteString("\n【L1 episodic 类原子记忆（最近事件）】\n")
	for _, a := range episodicAtoms {
		fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(a.Content))
	}
	b.WriteString("\n请按要求输出最终的 markdown 画像。")

	msgs := []llm.ChatMessage{
		{Role: "system", Content: profileSystemPrompt},
		{Role: "user", Content: b.String()},
	}
	raw, err := client.Chat(ctx, msgs)
	if err != nil {
		return nil, fmt.Errorf("memory.RegenerateProfile: chat: %w", err)
	}
	body := strings.TrimSpace(raw)
	// Strip an accidental ```markdown fence if the model snuck one in.
	if strings.HasPrefix(body, "```") {
		if idx := strings.Index(body, "\n"); idx >= 0 {
			body = body[idx+1:]
		}
		body = strings.TrimSuffix(strings.TrimSpace(body), "```")
	}
	body = strings.TrimSpace(body)
	if r := []rune(body); len(r) > MaxProfileChars {
		body = string(r[:MaxProfileChars])
	}

	now := time.Now()
	if prev != nil {
		updates := map[string]interface{}{
			"content":            body,
			"scene_count_at_gen": len(scenes),
			"atoms_at_gen":       len(personaAtoms) + len(instrAtoms) + len(episodicAtoms),
			"generated_at":       now,
			"request_reason":     reason,
		}
		if err := s.db.WithContext(ctx).Model(prev).Updates(updates).Error; err != nil {
			return nil, err
		}
		// Re-read so the returned struct reflects updated fields.
		var fresh model.UserProfile
		if err := s.db.WithContext(ctx).Where("id = ?", prev.ID).First(&fresh).Error; err != nil {
			return nil, err
		}
		return &fresh, nil
	}
	np := model.UserProfile{
		PersonaID:       personaID,
		Content:         body,
		SceneCountAtGen: len(scenes),
		AtomsAtGen:      len(personaAtoms) + len(instrAtoms) + len(episodicAtoms),
		GeneratedAt:     now,
		RequestReason:   reason,
	}
	if err := s.db.WithContext(ctx).Create(&np).Error; err != nil {
		return nil, err
	}
	return &np, nil
}

const profileSystemPrompt = `你是 OpenTheOne 的「用户长期画像撰写者」。你的任务是把这位用户的所有
长期记忆、主题场景、近期事件，整合成一份让 AI 角色在每次对话开始前都能秒读完的简洁画像。

【输出要求】
1. 纯 markdown，不要包裹三个反引号围栏的代码块。
2. 总字数 ≤ 2000 中文字（含标题与列表符号）。
3. 章节结构如下（缺料的章节可省略，不要硬凑）：
   ## 基本资料
   ## 长期偏好
   ## 关键关系
   ## 行动模式
   ## 当前焦点
   ## 对 AI 的长期指令
4. 「对 AI 的长期指令」一节必须列在最后，且使用 - 列表项，每条一行，便于 AI 严格遵守。
5. 用陈述句，不要用「该用户」「他/她」之类第三人称转述；直接说「喜欢手冲咖啡」。
6. 不要罗列时间戳，把同主题事件浓缩成一句概括。
7. 没有的内容不要补全，不要推测，不要写「未知」「待补充」之类占位句。
8. 如果上一版画像已经覆盖了全部信息且没有新事实，可以原样输出，不要为了改而改。

【风格示例】
## 基本资料
- 昵称：猫猫；不喜欢被叫全名
- 角色：互联网产品经理

## 长期偏好
- 咖啡：手冲为主，喜欢 Aeropress
- 阅读：偏好科幻短篇

## 行动模式
- 每天 6:30 起床晨跑
- 项目截止前一周会熬夜冲刺

## 对 AI 的长期指令
- 称呼：叫「猫猫」，不要用全名
- 风格：日常聊天用轻松口语，工作问题用条目化回答
- 边界：不要主动提父母相关话题

直接输出 markdown。`

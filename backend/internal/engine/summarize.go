package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/opentheone/opentheone/backend/internal/crypto"
	"github.com/opentheone/opentheone/backend/internal/llm"
	"github.com/opentheone/opentheone/backend/internal/model"
)

// summarizeLocks holds one mutex per conversation_id so that concurrent
// MaybeSummarize / RebuildSummary calls for the same conversation cannot
// double-spend an LLM call or clobber each other's watermark.
//
// Process-local; that's fine because:
//   - the long-poll runner is single-binding-per-goroutine, so the only way to
//     race is via overlapping HandleInbound fire-and-forget goroutines and the
//     RebuildSummary HTTP handler.
//   - this project ships as a single-binary deployment by design.
//
// For multi-replica setups, swap this out for a Redis-backed lease.
var summarizeLocks sync.Map // conversation_id (string) → *sync.Mutex

func summarizeLock(convID string) *sync.Mutex {
	if v, ok := summarizeLocks.Load(convID); ok {
		return v.(*sync.Mutex)
	}
	mu := &sync.Mutex{}
	actual, _ := summarizeLocks.LoadOrStore(convID, mu)
	return actual.(*sync.Mutex)
}

// summarizeSystemPrompt is intentionally short and structured.
// The model must produce a single Chinese paragraph (or a few bullets) that:
//   - preserves who the user is and what they care about
//   - notes any unfinished topics or commitments
//   - captures emotional tone changes
//
// Style notes (Markdown, headings) are explicitly forbidden so the summary
// concatenates cleanly into a system message later.
const summarizeSystemPrompt = `你是一个对话摘要助手。你将拿到：
1) 此前的旧摘要（可能为空）
2) 一批新的对话消息（USER 与 ASSISTANT 角色轮流）

请用第三人称中文，给出一段更新后的摘要（约 200-500 字），覆盖：
- 对方（USER）的身份、偏好、近况
- 双方此前讨论过的具体话题和结论
- 任何未完成的承诺、待回复的话题、情感基调
- ASSISTANT 角色当前的态度/口吻特征

要求：
- 输出**纯文本一段**，不要使用 Markdown 标题、列表、代码块。
- 不要复述具体的句子，要抽象概括。
- 不要输出任何"以下是摘要"之类的前缀。`

// MaybeSummarize is called fire-and-forget after each successful reply.
// If the conversation has accumulated more than (historyN + summaryEvery)
// unsummarized messages, it folds the older portion into Conversation.Summary
// via an LLM call.
//
// Concurrency: guarded by a per-conversation TryLock. Overlapping calls for the
// same conversation are a no-op (the second one returns immediately) so we
// don't double-spend the LLM call or clobber the watermark. Inside the lock we
// re-read SummaryUpdatedAt from the DB rather than trusting the snapshot the
// caller passed in.
func (e *Engine) MaybeSummarize(ctx context.Context, conv *model.Conversation, llmCfg *model.LLMConfig) {
	if conv == nil || llmCfg == nil || e.summaryEvery <= 0 {
		return
	}
	mu := summarizeLock(conv.ID)
	if !mu.TryLock() {
		// another summarize for this conversation is already in flight
		return
	}
	defer mu.Unlock()

	// Re-read the watermark from the DB so we don't act on a stale snapshot
	// from before a concurrent summarize finished.
	var fresh model.Conversation
	if err := e.db.WithContext(ctx).Where("id = ?", conv.ID).First(&fresh).Error; err != nil {
		e.log.Debug("reload conversation for summarize failed", zap.Error(err))
		return
	}
	conv.Summary = fresh.Summary
	conv.SummaryUntilMessageID = fresh.SummaryUntilMessageID
	conv.SummaryUpdatedAt = fresh.SummaryUpdatedAt

	// Count only real conversational messages (skip agent-loop audit rows),
	// otherwise a single chat turn with 5 tool calls would prematurely trip
	// the threshold.
	q := e.db.WithContext(ctx).
		Model(&model.Message{}).
		Where("conversation_id = ? AND direction IN ?", conv.ID, []string{"inbound", "outbound"})
	if !conv.SummaryUpdatedAt.IsZero() {
		q = q.Where("created_at > ?", conv.SummaryUpdatedAt)
	}
	var unsummarized int64
	if err := q.Count(&unsummarized).Error; err != nil {
		e.log.Debug("count unsummarized failed", zap.Error(err))
		return
	}

	threshold := int64(e.historyN + e.summaryEvery)
	if unsummarized < threshold {
		return
	}

	// Pick the oldest (unsummarized - historyN) messages to fold in. We keep
	// the most recent historyN verbatim for the next prompt.
	limit := int(unsummarized) - e.historyN
	if limit <= 0 {
		return
	}

	mq := e.db.WithContext(ctx).
		Where("conversation_id = ? AND direction IN ?", conv.ID, []string{"inbound", "outbound"})
	if !conv.SummaryUpdatedAt.IsZero() {
		mq = mq.Where("created_at > ?", conv.SummaryUpdatedAt)
	}
	var batch []model.Message
	if err := mq.Order("created_at asc").
		Limit(limit).
		Find(&batch).Error; err != nil {
		e.log.Debug("fetch batch for summarize failed", zap.Error(err))
		return
	}
	if len(batch) == 0 {
		return
	}

	apiKey, err := crypto.Decrypt(e.secret, llmCfg.APIKeyEnc)
	if err != nil || apiKey == "" {
		e.log.Debug("no api key for summarize", zap.Error(err))
		return
	}
	llmClient := llm.NewClient(llmCfg, apiKey)

	userBlob := renderBatchForSummary(conv.Summary, batch, e.summaryTarget)
	newSummary, err := llmClient.Chat(ctx, []llm.ChatMessage{
		{Role: "system", Content: summarizeSystemPrompt},
		{Role: "user", Content: userBlob},
	})
	if err != nil {
		e.log.Warn("rolling summarize failed", zap.Error(err), zap.String("conversation_id", conv.ID))
		return
	}
	newSummary = strings.TrimSpace(newSummary)
	if newSummary == "" {
		return
	}

	// Advance the watermark to the timestamp of the newest message in this batch
	// (NOT to time.Now() — concurrent inbound messages between Count and Update
	// would otherwise be wrongly considered "already summarized").
	last := batch[len(batch)-1]
	upd := map[string]interface{}{
		"summary":                  newSummary,
		"summary_until_message_id": last.ID,
		"summary_updated_at":       last.CreatedAt,
	}
	if err := e.db.WithContext(ctx).Model(&model.Conversation{}).
		Where("id = ?", conv.ID).
		Updates(upd).Error; err != nil {
		e.log.Warn("persist summary failed", zap.Error(err))
		return
	}
	conv.Summary = newSummary
	conv.SummaryUntilMessageID = last.ID
	conv.SummaryUpdatedAt = last.CreatedAt
	e.log.Info("rolling summary updated",
		zap.String("conversation_id", conv.ID),
		zap.Int("folded_messages", len(batch)),
		zap.Int("summary_chars", len([]rune(newSummary))))
}

// RebuildSummary discards the existing summary watermark and re-summarizes the
// entire conversation from scratch. Useful as a manual "fix it" knob for
// operators (exposed via /api/conversation/rebuild_summary).
//
// Concurrency: shares the per-conversation lock with MaybeSummarize, but uses
// blocking Lock (not TryLock) since this is user-initiated and they expect a
// fresh result. If a rolling MaybeSummarize is in flight we just wait for it.
func (e *Engine) RebuildSummary(ctx context.Context, conv *model.Conversation, llmCfg *model.LLMConfig) error {
	if conv == nil {
		return fmt.Errorf("nil conversation")
	}
	if llmCfg == nil {
		return fmt.Errorf("no llm config available")
	}
	mu := summarizeLock(conv.ID)
	mu.Lock()
	defer mu.Unlock()

	var msgs []model.Message
	if err := e.db.WithContext(ctx).
		Where("conversation_id = ? AND direction IN ?", conv.ID, []string{"inbound", "outbound"}).
		Order("created_at asc").
		Find(&msgs).Error; err != nil {
		return err
	}
	if len(msgs) <= e.historyN {
		// Nothing to summarize yet; clear the existing summary so downstream
		// prompt building doesn't keep injecting a stale summary.
		return e.db.WithContext(ctx).Model(&model.Conversation{}).
			Where("id = ?", conv.ID).
			Updates(map[string]interface{}{
				"summary":                  "",
				"summary_until_message_id": "",
				"summary_updated_at":       time.Time{},
			}).Error
	}
	keep := e.historyN
	old := msgs[:len(msgs)-keep]
	if len(old) == 0 {
		return nil
	}

	apiKey, err := crypto.Decrypt(e.secret, llmCfg.APIKeyEnc)
	if err != nil || apiKey == "" {
		return fmt.Errorf("decrypt api key: %w", err)
	}
	llmClient := llm.NewClient(llmCfg, apiKey)

	userBlob := renderBatchForSummary("", old, e.summaryTarget)
	newSummary, err := llmClient.Chat(ctx, []llm.ChatMessage{
		{Role: "system", Content: summarizeSystemPrompt},
		{Role: "user", Content: userBlob},
	})
	if err != nil {
		return err
	}
	newSummary = strings.TrimSpace(newSummary)
	last := old[len(old)-1]
	return e.db.WithContext(ctx).Model(&model.Conversation{}).
		Where("id = ?", conv.ID).
		Updates(map[string]interface{}{
			"summary":                  newSummary,
			"summary_until_message_id": last.ID,
			"summary_updated_at":       last.CreatedAt,
		}).Error
}

// renderBatchForSummary produces the user-side prompt content for the
// summarizer: previous summary (if any), then a labeled transcript.
// Long messages are clipped so a single rogue paste doesn't blow up the
// summarizer's input tokens — we lose a bit of fidelity per message but in
// exchange a 10MB message can't OOM the prompt.
func renderBatchForSummary(prevSummary string, batch []model.Message, target int) string {
	var b strings.Builder
	if strings.TrimSpace(prevSummary) != "" {
		b.WriteString("[此前的累积摘要]\n")
		b.WriteString(prevSummary)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "[新增对话（共 %d 条），请融合到摘要里。目标长度约 %d 字]\n", len(batch), target)
	const maxPerMsg = 800
	for _, m := range batch {
		role := "USER"
		if m.Direction == "outbound" {
			role = "ASSISTANT"
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		if r := []rune(text); len(r) > maxPerMsg {
			text = string(r[:maxPerMsg]) + " …(截断)"
		}
		b.WriteString(role)
		b.WriteString(": ")
		b.WriteString(text)
		b.WriteString("\n")
	}
	return b.String()
}

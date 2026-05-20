package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/crypto"
	"github.com/opentheone/opentheone/backend/internal/ilink"
	"github.com/opentheone/opentheone/backend/internal/llm"
	"github.com/opentheone/opentheone/backend/internal/mcp"
	"github.com/opentheone/opentheone/backend/internal/memory"
	"github.com/opentheone/opentheone/backend/internal/model"
)

// Engine glues conversation persistence, LLM generation, and iLink sending.
type Engine struct {
	db             *gorm.DB
	ilink          *ilink.Client
	mem            *memory.Service
	mcp            *mcp.Manager
	log            *zap.Logger
	secret         string // for decrypting LLMConfig.APIKeyEnc
	maxChunk       int    // max chars per outbound sendmessage
	historyN       int    // recent dialog lines fed to LLM verbatim
	retrieveK      int    // top-K memory snippets fed to LLM
	summaryEvery   int    // trigger rolling summary once unsummarized msgs exceed historyN + summaryEvery
	summaryTarget  int    // approx target char length for the rolling summary
	attachmentsDir string // where inbound media files are saved
	// agentMaxSteps caps how many tool-call rounds the agent loop will run
	// before falling back to whatever text the LLM produced. Guards against
	// runaway loops where the model keeps re-calling the same tool.
	agentMaxSteps int
	// pipeline runs async L1→L2→L3 memory processing after each reply.
	// Optional — when nil the engine falls back to legacy inline extraction.
	pipeline *memory.Pipeline
}

type Options struct {
	Secret         string
	MaxChunk       int
	HistoryN       int
	RetrieveK      int
	SummaryEvery   int
	SummaryTarget  int
	AttachmentsDir string
	AgentMaxSteps  int
}

func NewEngine(db *gorm.DB, ilinkClient *ilink.Client, mem *memory.Service, mcpMgr *mcp.Manager, log *zap.Logger, opts Options) *Engine {
	if opts.MaxChunk <= 0 {
		opts.MaxChunk = 1800
	}
	if opts.HistoryN <= 0 {
		opts.HistoryN = 16
	}
	if opts.RetrieveK <= 0 {
		opts.RetrieveK = 5
	}
	if opts.SummaryEvery <= 0 {
		opts.SummaryEvery = 8
	}
	if opts.SummaryTarget <= 0 {
		opts.SummaryTarget = 600
	}
	if opts.AgentMaxSteps <= 0 {
		opts.AgentMaxSteps = 6
	}
	return &Engine{
		db:             db,
		ilink:          ilinkClient,
		mem:            mem,
		mcp:            mcpMgr,
		log:            log,
		secret:         opts.Secret,
		maxChunk:       opts.MaxChunk,
		historyN:       opts.HistoryN,
		retrieveK:      opts.RetrieveK,
		summaryEvery:   opts.SummaryEvery,
		summaryTarget:  opts.SummaryTarget,
		attachmentsDir: opts.AttachmentsDir,
		agentMaxSteps:  opts.AgentMaxSteps,
	}
}

// AttachPipeline wires the async memory pipeline. Called once during boot
// (main.go) after both the engine and the pipeline exist. Memory triggers
// are no-ops until this is called, so handlers that only need synchronous
// memory features (manual ingest, scene/profile HTTP) still work without
// the pipeline running.
func (e *Engine) AttachPipeline(p *memory.Pipeline) {
	e.pipeline = p
}

// ResolveClientForPersona is the engine's persona → LLM client lookup,
// exported so the memory pipeline can reuse it (it needs the same fallback
// rules: persona pin → user default).
func (e *Engine) ResolveClientForPersona(ctx context.Context, personaID string) (*llm.Client, error) {
	var p model.Persona
	if err := e.db.WithContext(ctx).Where("id = ?", personaID).First(&p).Error; err != nil {
		return nil, err
	}
	var cfg model.LLMConfig
	if p.LLMConfigID != "" {
		_ = e.db.WithContext(ctx).Where("id = ?", p.LLMConfigID).First(&cfg).Error
	}
	if cfg.ID == "" {
		_ = e.db.WithContext(ctx).Where("user_id = ? AND is_default = ?", p.UserID, true).First(&cfg).Error
	}
	if cfg.ID == "" {
		return nil, nil
	}
	key, err := crypto.Decrypt(e.secret, cfg.APIKeyEnc)
	if err != nil || key == "" {
		return nil, nil
	}
	return llm.NewClient(&cfg, key), nil
}

// upsertConversation finds-or-creates a conversation row and refreshes last_message_at.
func (e *Engine) upsertConversation(ctx context.Context, bindingID, peerID, sessionID string) (*model.Conversation, error) {
	var conv model.Conversation
	// NOTE: physical column is `i_link_user_id`, NOT `ilink_user_id`.
	// GORM's NamingStrategy splits CamelCase at every transition, so the
	// Go field `ILinkUserID` becomes `i_link_user_id` in SQL. Verify with
	// `sqlite3 data/oto.db ".schema conversations"`.
	err := e.db.WithContext(ctx).
		Where("binding_id = ? AND i_link_user_id = ?", bindingID, peerID).
		First(&conv).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		conv = model.Conversation{
			BindingID:     bindingID,
			ILinkUserID:   peerID,
			SessionID:     sessionID,
			LastMessageAt: time.Now(),
		}
		if err := e.db.WithContext(ctx).Create(&conv).Error; err != nil {
			return nil, err
		}
		return &conv, nil
	}
	if err != nil {
		return nil, err
	}
	conv.LastMessageAt = time.Now()
	if sessionID != "" && conv.SessionID != sessionID {
		conv.SessionID = sessionID
	}
	if err := e.db.WithContext(ctx).Save(&conv).Error; err != nil {
		return nil, err
	}
	return &conv, nil
}

// extractInboundText flattens an inbound iLink message into plain text + best-effort kind.
func extractInboundText(msg *ilink.WeixinMessage) (string, string) {
	if msg == nil || len(msg.ItemList) == 0 {
		return "", "text"
	}
	var b strings.Builder
	kind := "text"
	for _, it := range msg.ItemList {
		switch it.Type {
		case ilink.ItemTypeText:
			if it.TextItem != nil {
				b.WriteString(it.TextItem.Text)
				b.WriteString("\n")
			}
		case ilink.ItemTypeVoice:
			kind = "voice"
			if it.VoiceItem != nil && it.VoiceItem.Text != "" {
				b.WriteString(it.VoiceItem.Text)
				b.WriteString("\n")
			} else {
				b.WriteString("[voice message]\n")
			}
		case ilink.ItemTypeImage:
			kind = "image"
			b.WriteString("[image]\n")
		case ilink.ItemTypeFile:
			kind = "file"
			if it.FileItem != nil && it.FileItem.FileName != "" {
				b.WriteString("[file: " + it.FileItem.FileName + "]\n")
			} else {
				b.WriteString("[file]\n")
			}
		case ilink.ItemTypeVideo:
			kind = "video"
			b.WriteString("[video]\n")
		}
	}
	return strings.TrimSpace(b.String()), kind
}

// HandleInbound processes one inbound WeChat message: persist, generate reply, send, ingest memory.
func (e *Engine) HandleInbound(ctx context.Context, binding *model.WeChatBinding, persona *model.Persona, llmCfg *model.LLMConfig, msg ilink.WeixinMessage) error {
	if msg.FromUserID == "" {
		return nil
	}

	text, kind := extractInboundText(&msg)
	conv, err := e.upsertConversation(ctx, binding.ID, msg.FromUserID, msg.SessionID)
	if err != nil {
		return fmt.Errorf("upsert conversation: %w", err)
	}

	var extraJSON string
	if len(msg.ItemList) > 0 {
		if buf, err := json.Marshal(msg.ItemList); err == nil {
			extraJSON = string(buf)
		}
	}

	inboundMsg := model.Message{
		ConversationID: conv.ID,
		Direction:      "inbound",
		ILinkMessageID: msg.MessageID,
		ClientID:       msg.ClientID,
		ContextToken:   msg.ContextToken,
		Type:           kind,
		Text:           text,
		Extra:          extraJSON,
		Status:         "received",
	}
	if err := e.db.WithContext(ctx).Create(&inboundMsg).Error; err != nil {
		return fmt.Errorf("persist inbound: %w", err)
	}

	if e.attachmentsDir != "" {
		hasMedia := false
		for i := range msg.ItemList {
			switch msg.ItemList[i].Type {
			case ilink.ItemTypeImage, ilink.ItemTypeFile, ilink.ItemTypeVoice, ilink.ItemTypeVideo:
				hasMedia = true
			}
		}
		if hasMedia {
			itemListCopy := append([]ilink.MessageItem(nil), msg.ItemList...)
			msgID := inboundMsg.ID
			go func(items []ilink.MessageItem, id string) {
				defer func() {
					if rec := recover(); rec != nil {
						e.log.Error("attachment download goroutine panicked",
							zap.Any("panic", rec),
							zap.String("message_id", id))
					}
				}()
				bg, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				for i := range items {
					it := items[i]
					e.downloadIfNeeded(bg, id, &it)
				}
			}(itemListCopy, msgID)
		}
	}

	if msg.ContextToken != "" {
		_ = e.db.WithContext(ctx).Model(&model.WeChatBinding{}).
			Where("id = ?", binding.ID).
			Update("last_context_token", msg.ContextToken).Error
		binding.LastContextToken = msg.ContextToken

		_ = e.db.WithContext(ctx).Model(&model.Conversation{}).
			Where("id = ?", conv.ID).
			Update("last_context_token", msg.ContextToken).Error
		conv.LastContextToken = msg.ContextToken
	}

	if persona == nil {
		e.log.Warn("no persona attached, skipping reply", zap.String("binding_id", binding.ID))
		return nil
	}
	if llmCfg == nil {
		e.log.Warn("no llm config, skipping reply",
			zap.String("binding_id", binding.ID),
			zap.String("persona_id", persona.ID))
		return nil
	}

	apiKey, err := crypto.Decrypt(e.secret, llmCfg.APIKeyEnc)
	if err != nil {
		return fmt.Errorf("decrypt api key: %w", err)
	}
	llmClient := llm.NewClient(llmCfg, apiKey)

	// Build the iLink session early so we can start the "正在输入" indicator
	// BEFORE the LLM (and any MCP tool calls) start. With the agent loop a
	// turn can easily take 5-15s; without an early typing signal the peer
	// just sees silence.
	sess := ilink.Session{
		BotToken:    binding.BotToken,
		BaseURL:     binding.BaseURL,
		ILinkBotID:  binding.ILinkBotID,
		ILinkUserID: binding.ILinkUserID,
	}
	typingTicket := e.ensureTypingTicket(ctx, sess, msg.FromUserID, msg.ContextToken)
	if typingTicket != "" {
		_ = e.ilink.SendTyping(ctx, sess, msg.FromUserID, typingTicket, 1)
	}
	stopTyping := func() {
		if typingTicket != "" {
			// fire and forget; we don't want a typing stop failure to mask the original error
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = e.ilink.SendTyping(stopCtx, sess, msg.FromUserID, typingTicket, 2)
		}
	}

	reply, err := e.generateReply(ctx, persona, conv, llmClient, text)
	if err != nil {
		stopTyping()
		e.log.Error("generate reply failed", zap.Error(err))
		return err
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		stopTyping()
		return nil
	}

	chunks := splitForWeChat(reply, e.maxChunk)
	for i, chunk := range chunks {
		clientID := fmt.Sprintf("oto:%d-%s", time.Now().UnixMilli(), uuid.NewString()[:8])
		out := model.Message{
			ConversationID: conv.ID,
			Direction:      "outbound",
			ClientID:       clientID,
			ContextToken:   msg.ContextToken,
			Type:           "text",
			Text:           chunk,
			Status:         "pending",
		}
		if err := e.db.WithContext(ctx).Create(&out).Error; err != nil {
			stopTyping()
			return err
		}
		if err := e.ilink.SendTextMessage(ctx, sess, msg.FromUserID, msg.ContextToken, clientID, chunk); err != nil {
			// Column-scoped update — rewriting the whole row via Save would
			// also clobber any concurrent edit and write 10+ columns we don't
			// need to touch.
			out.Status = "failed"
			_ = e.db.WithContext(ctx).Model(&model.Message{}).
				Where("id = ?", out.ID).
				Update("status", "failed").Error
			e.log.Error("sendmessage failed",
				zap.Error(err),
				zap.Int("chunk_idx", i),
				zap.String("binding_id", binding.ID))
			stopTyping()
			return err
		}
		out.Status = "sent"
		if err := e.db.WithContext(ctx).Model(&model.Message{}).
			Where("id = ?", out.ID).
			Update("status", "sent").Error; err != nil {
			stopTyping()
			return err
		}
		// Backfill the conversation FTS index immediately so the
		// `oto_conversation_search` built-in tool can find this chunk on
		// the next turn.
		if e.mem != nil {
			_ = e.mem.BM25().IndexMessage(ctx, out.ID, conv.ID, chunk)
		}
		if i+1 < len(chunks) {
			time.Sleep(400 * time.Millisecond)
		}
	}
	stopTyping()

	// Index the inbound message into the conversation FTS so
	// `oto_conversation_search` can already find it on the next turn.
	if e.mem != nil {
		_ = e.mem.BM25().IndexMessage(ctx, inboundMsg.ID, conv.ID, text)
	}
	// Hand the new message pair off to the async memory pipeline. The
	// pipeline decides — based on per-persona warmup / threshold / idle
	// timing — whether to actually run the L1→L2→L3 LLM cycle now or wait.
	// Pipeline failures must never affect message delivery (it runs in
	// its own goroutine and only logs errors).
	if e.pipeline != nil {
		e.pipeline.Trigger(persona.ID, conv.ID)
	}

	// Fire-and-forget rolling summary. If the conversation has accumulated more
	// than (historyN + summaryEvery) new messages, fold the old portion in.
	go func(c model.Conversation, cfg model.LLMConfig) {
		bg, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		defer func() {
			if rec := recover(); rec != nil {
				e.log.Error("MaybeSummarize panicked", zap.Any("panic", rec))
			}
		}()
		e.MaybeSummarize(bg, &c, &cfg)
	}(*conv, *llmCfg)

	return nil
}

// generateReply builds the prompt and runs the agent loop.
//
// Agent loop overview:
//  1. Build a chat history (system + summary + memory + recent turns).
//  2. Load the persona's enabled MCP tools (if any).
//  3. Streaming chat-completion call with the tool list.
//  4. If the model returns tool_calls instead of (or in addition to) text:
//     execute each call via the MCP registry, append tool result messages,
//     loop. Otherwise return the text.
//  5. Bounded by agentMaxSteps so a misbehaving model can't loop forever.
//
// When no MCP servers are enabled we keep the original (non-streaming,
// no-tools) Chat() path; it's slightly cheaper and bypasses the streaming
// machinery for providers that don't speak it perfectly.
func (e *Engine) generateReply(ctx context.Context, persona *model.Persona, conv *model.Conversation, llmClient *llm.Client, userText string) (string, error) {
	var prior int64
	_ = e.db.WithContext(ctx).Model(&model.Message{}).
		Where("conversation_id = ? AND direction = ?", conv.ID, "outbound").
		Count(&prior).Error
	firstInteraction := prior == 0

	// ----- HEADER (cache-friendly, stable) ----------------------------
	//
	// Everything above the rolling summary stays byte-stable across turns
	// inside the same persona + same L3 profile generation, which lets
	// OpenAI-style prompt-caching kick in. Keep prompt-prefix changes to a
	// minimum.
	//
	// Order:
	//   1. Persona system prompt
	//   2. L3 user profile (if any)
	//   3. L2 scene index (if any)
	header := buildSystemPrompt(persona, firstInteraction)
	if e.mem != nil {
		if profile, err := e.mem.ProfileForPrompt(ctx, persona.ID); err == nil && profile != "" {
			header += "\n" + profile
		}
		if index, err := e.mem.SceneIndexForPrompt(ctx, persona.ID, memory.MaxScenesPerPersona); err == nil && index != "" {
			header += "\n" + index
		}
	}
	msgs := []llm.ChatMessage{{Role: "system", Content: header}}

	// ----- DYNAMIC SECTIONS (post-header) -----------------------------
	//
	// These vary per turn, so they intentionally live BELOW the header to
	// preserve the cached prefix above.

	if strings.TrimSpace(conv.Summary) != "" {
		msgs = append(msgs, llm.ChatMessage{
			Role:    "system",
			Content: "【你和对方此前对话的累积摘要（保持连续性参考，不要复述）】\n" + conv.Summary,
		})
	}

	if e.mem != nil {
		mems, err := e.mem.RetrieveForConversation(ctx, llmClient, persona.ID, conv.ID, userText, e.retrieveK)
		if err == nil && len(mems) > 0 {
			var b strings.Builder
			b.WriteString("【与当前消息最相关的长期记忆（按相关度排序，不要直接照念，自然融入回复）】\n")
			for _, m := range mems {
				b.WriteString("- ")
				b.WriteString(m.Content)
				b.WriteString("\n")
			}
			msgs = append(msgs, llm.ChatMessage{Role: "system", Content: b.String()})
		}
	}

	// Recent verbatim history — only real inbound/outbound messages newer
	// than the summary watermark. We deliberately exclude the agent-loop
	// audit rows (tool_call / tool_result) here: they're persisted for the
	// user's debugging, not for re-feeding into a fresh LLM turn (the live
	// agent loop maintains its own in-memory tool-call history).
	q := e.db.WithContext(ctx).
		Where("conversation_id = ? AND direction IN ?", conv.ID, []string{"inbound", "outbound"})
	if !conv.SummaryUpdatedAt.IsZero() {
		q = q.Where("created_at > ?", conv.SummaryUpdatedAt)
	}
	var history []model.Message
	if err := q.Order("created_at desc").
		Limit(e.historyN).
		Find(&history).Error; err == nil {
		for i := len(history) - 1; i >= 0; i-- {
			h := history[i]
			role := "user"
			if h.Direction == "outbound" {
				role = "assistant"
			}
			if strings.TrimSpace(h.Text) == "" {
				continue
			}
			msgs = append(msgs, llm.ChatMessage{Role: role, Content: h.Text})
		}
	}

	// ----- TOOLS -------------------------------------------------------
	//
	// Built-in (memory_search / scene_read / conversation_search) are
	// ALWAYS available; MCP tools are appended on top when the persona has
	// any enabled.
	var registry *mcp.Registry
	if e.mcp != nil {
		registry = mcp.LoadForPersona(ctx, e.db, e.mcp, e.log, persona)
	}

	tools := append([]mcp.LLMTool(nil), builtinTools...)
	if registry != nil {
		tools = append(tools, registry.Tools()...)
	}

	// Tool usage hint inserted as the second system message so it doesn't
	// disrupt the cached header prefix.
	hint := builtinToolUsageHint
	if registry != nil && !registry.Empty() {
		hint += "\n\n此外你还接入了若干 MCP 工具，名字以 mcp__ 开头，按需调用即可。"
	}
	msgs = append([]llm.ChatMessage{msgs[0], {Role: "system", Content: hint}}, msgs[1:]...)

	return e.runAgentLoop(ctx, persona, conv, llmClient, registry, tools, msgs)
}

// runAgentLoop drives the assistant → tool_calls → tool_results loop until
// the model produces a final text reply (or we hit the step cap).
//
// Implementation notes:
//   - We feed the LLM the FULL history including its own assistant-with-tool-
//     calls messages, then the corresponding tool result messages. OpenAI
//     rejects orphan tool messages (no matching assistant.tool_calls), so we
//     always append both halves together.
//   - A tool that errors out is still reported back to the model as a "tool"
//     message with the error string, so the model can choose to retry or
//     apologize to the user. We do NOT abort the loop on a tool failure.
//   - If we exhaust agentMaxSteps with no text reply, we return a polite
//     fallback so the user isn't ghosted.
func (e *Engine) runAgentLoop(
	ctx context.Context,
	persona *model.Persona,
	conv *model.Conversation,
	llmClient *llm.Client,
	registry *mcp.Registry,
	tools []mcp.LLMTool,
	msgs []llm.ChatMessage,
) (string, error) {
	llmTools := make([]llm.Tool, 0, len(tools))
	for _, t := range tools {
		llmTools = append(llmTools, llm.Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}

	var lastContent string
	for step := 0; step < e.agentMaxSteps; step++ {
		turn, err := llmClient.ChatWithTools(ctx, msgs, llmTools)
		if err != nil {
			return "", fmt.Errorf("agent step %d: %w", step, err)
		}
		if strings.TrimSpace(turn.Content) != "" {
			lastContent = turn.Content
		}

		// If the model didn't ask for tools, we're done.
		if len(turn.ToolCalls) == 0 {
			return turn.Content, nil
		}

		e.log.Debug("agent step requested tool calls",
			zap.Int("step", step),
			zap.Int("calls", len(turn.ToolCalls)),
			zap.String("finish_reason", turn.FinishReason))

		// Persist the assistant-with-tool-calls turn into the running history
		// so the model sees its own prior decision next round.
		msgs = append(msgs, llm.ChatMessage{
			Role:      "assistant",
			Content:   turn.Content,
			ToolCalls: turn.ToolCalls,
		})

		// Execute each tool call sequentially. Parallel is possible but
		// makes error attribution harder and most MCP servers we expect
		// are local stdio (no real network parallelism benefit).
		for _, call := range turn.ToolCalls {
			// Persist the agent's *decision* row (tool_call). Failure is
			// best-effort; an audit-row write should never block actual
			// tool execution.
			e.persistToolCallRow(ctx, conv, call)

			args := map[string]any{}
			if call.Arguments != "" {
				if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
					// Bad JSON from the model: report back as a tool error
					// so the next round can self-correct.
					errMsg := fmt.Sprintf("error: arguments JSON parse failed: %v", err)
					e.persistToolResultRow(ctx, conv, call, errMsg, "failed")
					msgs = append(msgs, llm.ChatMessage{
						Role:       "tool",
						ToolCallID: call.ID,
						Content:    errMsg,
					})
					continue
				}
			}
			var (
				result string
				isErr  bool
				err    error
			)
			if isBuiltinTool(call.Name) {
				result, isErr, err = e.invokeBuiltinTool(ctx, call.Name, args, persona.ID, conv.ID)
			} else if registry != nil {
				result, isErr, err = registry.Invoke(ctx, call.Name, args)
			} else {
				err = fmt.Errorf("tool %q not available (no MCP registry loaded)", call.Name)
			}
			content := result
			status := "ok"
			switch {
			case err != nil:
				content = "error: " + err.Error()
				status = "failed"
				e.log.Warn("tool invoke failed",
					zap.String("tool", call.Name),
					zap.Error(err))
			case isErr:
				if content == "" {
					content = "(tool reported error)"
				}
				status = "failed"
			case content == "":
				content = "(tool returned no content)"
			}
			e.persistToolResultRow(ctx, conv, call, content, status)
			msgs = append(msgs, llm.ChatMessage{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    content,
			})
		}
	}

	// Step budget exhausted. Prefer the last bit of text the model produced
	// (even if it was alongside another tool call), so the user gets *some*
	// answer instead of a canned apology.
	if strings.TrimSpace(lastContent) != "" {
		return lastContent, nil
	}
	e.log.Warn("agent loop hit step cap without final reply",
		zap.Int("max_steps", e.agentMaxSteps))
	return "（抱歉，我刚刚卡住了，过会儿再聊好吗？）", nil
}

// persistToolCallRow writes a "tool_call" audit message into the conversation
// so the user can see what the AI decided to invoke. Best-effort: errors are
// logged but never block the agent loop.
func (e *Engine) persistToolCallRow(ctx context.Context, conv *model.Conversation, call llm.ToolCall) {
	if conv == nil {
		return
	}
	row := model.Message{
		ConversationID: conv.ID,
		Direction:      "tool_call",
		Type:           "tool",
		Status:         "ok",
		ToolName:       call.Name,
		ToolCallID:     call.ID,
		ToolArgs:       call.Arguments,
	}
	if err := e.db.WithContext(ctx).Create(&row).Error; err != nil {
		e.log.Warn("persist tool_call row failed",
			zap.String("tool", call.Name),
			zap.Error(err))
	}
}

// persistToolResultRow writes a "tool_result" audit message paired with the
// previous tool_call (joined by ToolCallID). content is the rendered tool
// output (already truncated by mcp.renderResult upstream); status is "ok"
// or "failed".
func (e *Engine) persistToolResultRow(ctx context.Context, conv *model.Conversation, call llm.ToolCall, content, status string) {
	if conv == nil {
		return
	}
	row := model.Message{
		ConversationID: conv.ID,
		Direction:      "tool_result",
		Type:           "tool",
		Status:         status,
		ToolName:       call.Name,
		ToolCallID:     call.ID,
		ToolResult:     content,
	}
	if err := e.db.WithContext(ctx).Create(&row).Error; err != nil {
		e.log.Warn("persist tool_result row failed",
			zap.String("tool", call.Name),
			zap.Error(err))
	}
}

func buildSystemPrompt(p *model.Persona, firstInteraction bool) string {
	var b strings.Builder
	b.WriteString("你的名字是「" + p.Name + "」。\n")
	if strings.TrimSpace(p.Description) != "" {
		b.WriteString("你的角色简介：" + p.Description + "\n")
	}
	if strings.TrimSpace(p.SystemPrompt) != "" {
		b.WriteString("\n你的设定：\n" + p.SystemPrompt + "\n")
	}
	if strings.TrimSpace(p.SpeakingStyle) != "" {
		b.WriteString("\n你的说话风格：" + p.SpeakingStyle + "\n")
	}
	if firstInteraction && strings.TrimSpace(p.Greeting) != "" {
		b.WriteString("\n这是你第一次跟对方在微信里讲话。你的开场白参考（无需照念，按风格自然展开）：\n")
		b.WriteString(p.Greeting)
		b.WriteString("\n")
	}
	b.WriteString(`
你正在以「微信联系人」的身份和对方对话，请遵守：
1. 输出**纯文本**，不要 Markdown 标题/列表/代码块/链接图片标记。
2. 像真实的人聊微信：自然、简短、口语化、有情绪。可以分多条短消息（用换行分隔），但单次回复不要太长。
3. 你只是一个被对方加为微信好友的角色，不要暴露"我是 AI"或"大语言模型"等字样。
4. 用第二人称称呼对方，回应对方说的具体内容，不要重复套话。
`)
	return b.String()
}

// ensureTypingTicket fetches a typing ticket for the given (peer, context_token).
//
// We deliberately do NOT cache the ticket on the WeChatBinding row anymore: the
// /ilink/bot/getconfig request is parameterized by `ilink_user_id` (the peer
// we're chatting with), so a ticket fetched for peer A is at best ignored and
// at worst rejected by the server when used to "I'm typing" to peer B. The
// old per-binding cache silently misbehaved as soon as a single bot had two
// active conversations — exactly the documented use case.
//
// One extra getconfig HTTP call per inbound message is cheap (≤ 50 ms over a
// warm connection) compared to the LLM + MCP work that follows it, and we
// always run it BEFORE the LLM call so it's not on the user-perceived latency
// critical path the way an in-the-loop call would be.
//
// The TypingTicket / TypingTicketAt columns on the model remain for backward
// compatibility (they may be set by older builds; we just no longer read
// them).
func (e *Engine) ensureTypingTicket(ctx context.Context, sess ilink.Session, toUserID, contextToken string) string {
	ticket, err := e.ilink.GetTypingTicket(ctx, sess, toUserID, contextToken)
	if err != nil || ticket == "" {
		return ""
	}
	return ticket
}

// splitForWeChat tries to split text on paragraph boundaries to fit the 2000-char limit.
//
// All emitted chunks are guaranteed non-empty after TrimSpace: a window that
// happens to land on a run of pure whitespace would otherwise produce an empty
// string, and iLink's sendmessage rejects empty TextItem payloads with a
// confusing "ret=-1" error that masks the real cause.
func splitForWeChat(text string, maxChars int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if utf8.RuneCountInString(text) <= maxChars {
		return []string{text}
	}
	var out []string
	remaining := text
	for utf8.RuneCountInString(remaining) > maxChars {
		cut := findSplit(remaining, maxChars)
		chunk := strings.TrimSpace(remaining[:cut])
		if chunk != "" {
			out = append(out, chunk)
		}
		remaining = strings.TrimLeft(remaining[cut:], "\n ")
	}
	if tail := strings.TrimSpace(remaining); tail != "" {
		out = append(out, tail)
	}
	return out
}

func findSplit(s string, maxChars int) int {
	runeCount := 0
	cutByte := 0
	for i, r := range s {
		runeCount++
		cutByte = i + utf8.RuneLen(r)
		if runeCount >= maxChars {
			break
		}
	}
	if cutByte > len(s) {
		cutByte = len(s)
	}
	prefix := s[:cutByte]
	if idx := strings.LastIndex(prefix, "\n\n"); idx > maxChars/2 {
		return idx + 2
	}
	if idx := strings.LastIndex(prefix, "\n"); idx > maxChars/2 {
		return idx + 1
	}
	if idx := strings.LastIndex(prefix, " "); idx > maxChars/2 {
		return idx + 1
	}
	return cutByte
}

// The legacy synchronous ingestMemory()/buildSnippet() helpers were removed
// in the M5 pipeline migration. New memory extraction is driven by
// engine.pipeline (set via AttachPipeline) and runs entirely on the
// pipeline's own goroutines + per-persona scheduling state.

// SendLiteralText sends an exact piece of text from the bot to the given peer, bypassing the LLM.
// It is used for the "manual override" send_manual endpoint and for posting persona greetings.
// Returns ErrNoContextToken if no context_token has been cached yet for this conversation.
func (e *Engine) SendLiteralText(ctx context.Context, binding *model.WeChatBinding, peerUserID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("empty text")
	}

	var conv model.Conversation
	if peerUserID != "" {
		if err := e.db.WithContext(ctx).
			Where("binding_id = ? AND i_link_user_id = ?", binding.ID, peerUserID).
			First(&conv).Error; err != nil {
			return fmt.Errorf("conversation not found for peer: %w", err)
		}
	} else {
		if err := e.db.WithContext(ctx).
			Where("binding_id = ?", binding.ID).
			Order("last_message_at desc").
			First(&conv).Error; err != nil {
			return fmt.Errorf("no conversation to send to: %w", err)
		}
	}

	token := conv.LastContextToken
	if token == "" {
		token = binding.LastContextToken
	}
	if token == "" {
		return ErrNoContextToken
	}

	sess := ilink.Session{
		BotToken:    binding.BotToken,
		BaseURL:     binding.BaseURL,
		ILinkBotID:  binding.ILinkBotID,
		ILinkUserID: binding.ILinkUserID,
	}
	chunks := splitForWeChat(text, e.maxChunk)
	for _, chunk := range chunks {
		clientID := fmt.Sprintf("oto-manual:%d-%s", time.Now().UnixMilli(), uuid.NewString()[:8])
		if err := e.ilink.SendTextMessage(ctx, sess, conv.ILinkUserID, token, clientID, chunk); err != nil {
			return err
		}
		out := model.Message{
			ConversationID: conv.ID,
			Direction:      "outbound",
			ClientID:       clientID,
			ContextToken:   token,
			Type:           "text",
			Text:           chunk,
			Status:         "sent",
		}
		_ = e.db.WithContext(ctx).Create(&out).Error
		_ = e.db.WithContext(ctx).Model(&model.Conversation{}).
			Where("id = ?", conv.ID).
			Update("last_message_at", time.Now()).Error
	}
	return nil
}

// ErrNoContextToken is returned when there is no cached context_token for proactive/manual sending.
var ErrNoContextToken = errors.New("no context_token cached; have the user message the bot first")

// SendProactive composes and sends a proactive message based on persona's proactive_prompt.
func (e *Engine) SendProactive(ctx context.Context, binding *model.WeChatBinding, persona *model.Persona, llmCfg *model.LLMConfig, peerUserID string) error {
	var conv model.Conversation
	if peerUserID != "" {
		if err := e.db.WithContext(ctx).
			Where("binding_id = ? AND i_link_user_id = ?", binding.ID, peerUserID).
			First(&conv).Error; err != nil {
			return fmt.Errorf("conversation not found for peer: %w", err)
		}
	} else {
		if err := e.db.WithContext(ctx).
			Where("binding_id = ?", binding.ID).
			Order("last_message_at desc").
			First(&conv).Error; err != nil {
			return err
		}
	}

	token := conv.LastContextToken
	if token == "" {
		token = binding.LastContextToken
	}
	if token == "" {
		return ErrNoContextToken
	}

	apiKey, err := crypto.Decrypt(e.secret, llmCfg.APIKeyEnc)
	if err != nil {
		return err
	}
	llmClient := llm.NewClient(llmCfg, apiKey)
	prompt := persona.ProactivePrompt
	if strings.TrimSpace(prompt) == "" {
		prompt = "请用你的口吻，主动给对方发一句关心问候，简短自然。"
	}
	// Build the same cache-friendly system header generateReply uses, so
	// proactive greetings benefit from the L3 user portrait + L2 scene
	// index. Without this the AI would send generic "你好" greetings
	// instead of e.g. referencing what the user mentioned yesterday.
	sysHeader := buildSystemPrompt(persona, false)
	if e.mem != nil {
		if profile, perr := e.mem.ProfileForPrompt(ctx, persona.ID); perr == nil && profile != "" {
			sysHeader += "\n\n" + profile
		}
		if sceneIdx, serr := e.mem.SceneIndexForPrompt(ctx, persona.ID, 0); serr == nil && sceneIdx != "" {
			sysHeader += "\n\n" + sceneIdx
		}
	}
	msgs := []llm.ChatMessage{
		{Role: "system", Content: sysHeader},
		{Role: "user", Content: prompt},
	}
	reply, err := llmClient.Chat(ctx, msgs)
	if err != nil {
		return err
	}
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return nil
	}

	sess := ilink.Session{
		BotToken:    binding.BotToken,
		BaseURL:     binding.BaseURL,
		ILinkBotID:  binding.ILinkBotID,
		ILinkUserID: binding.ILinkUserID,
	}
	chunks := splitForWeChat(reply, e.maxChunk)

	for _, chunk := range chunks {
		clientID := fmt.Sprintf("oto-pro:%d-%s", time.Now().UnixMilli(), uuid.NewString()[:8])
		if err := e.ilink.SendTextMessage(ctx, sess, conv.ILinkUserID, token, clientID, chunk); err != nil {
			return err
		}
		out := model.Message{
			ConversationID: conv.ID,
			Direction:      "outbound",
			ClientID:       clientID,
			ContextToken:   token,
			Type:           "text",
			Text:           chunk,
			Status:         "sent",
		}
		if err := e.db.WithContext(ctx).Create(&out).Error; err == nil && e.mem != nil {
			// Index proactive outbound so `oto_conversation_search` can
			// find what the AI said unprompted. Errors here are non-fatal;
			// the message is already persisted in the canonical table.
			_ = e.mem.BM25().IndexMessage(ctx, out.ID, conv.ID, chunk)
		}
	}
	now := time.Now()
	_ = e.db.WithContext(ctx).Model(&model.WeChatBinding{}).
		Where("id = ?", binding.ID).
		Update("last_proactive_at", now).Error
	_ = e.db.WithContext(ctx).Model(&model.Conversation{}).
		Where("id = ?", conv.ID).
		Update("last_message_at", now).Error

	// Fire-and-forget rolling summary. HandleInbound does this on every
	// reply, but proactive sends don't go through HandleInbound — so a
	// persona that sends daily greetings into a one-sided conversation
	// would accumulate outbound messages past `historyN + summaryEvery`
	// forever, and the next inbound reply would blow up the prompt
	// budget. Run on a fresh background context so a slow summarize
	// doesn't extend the proactive RPC's deadline.
	go func(c model.Conversation, cfg model.LLMConfig) {
		defer func() {
			if rec := recover(); rec != nil {
				e.log.Error("MaybeSummarize (proactive) panicked", zap.Any("panic", rec))
			}
		}()
		bg, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		e.MaybeSummarize(bg, &c, &cfg)
	}(conv, *llmCfg)

	return nil
}

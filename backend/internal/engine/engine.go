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

	"github.com/wzyjerry/opentheone/backend/internal/crypto"
	"github.com/wzyjerry/opentheone/backend/internal/ilink"
	"github.com/wzyjerry/opentheone/backend/internal/llm"
	"github.com/wzyjerry/opentheone/backend/internal/memory"
	"github.com/wzyjerry/opentheone/backend/internal/model"
)

// Engine glues conversation persistence, LLM generation, and iLink sending.
type Engine struct {
	db             *gorm.DB
	ilink          *ilink.Client
	mem            *memory.Service
	log            *zap.Logger
	secret         string // for decrypting LLMConfig.APIKeyEnc
	maxChunk       int    // max chars per outbound sendmessage
	historyN       int    // recent dialog lines fed to LLM verbatim
	retrieveK      int    // top-K memory snippets fed to LLM
	summaryEvery   int    // trigger rolling summary once unsummarized msgs exceed historyN + summaryEvery
	summaryTarget  int    // approx target char length for the rolling summary
	attachmentsDir string // where inbound media files are saved
}

type Options struct {
	Secret         string
	MaxChunk       int
	HistoryN       int
	RetrieveK      int
	SummaryEvery   int
	SummaryTarget  int
	AttachmentsDir string
}

func NewEngine(db *gorm.DB, ilinkClient *ilink.Client, mem *memory.Service, log *zap.Logger, opts Options) *Engine {
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
	return &Engine{
		db:             db,
		ilink:          ilinkClient,
		mem:            mem,
		log:            log,
		secret:         opts.Secret,
		maxChunk:       opts.MaxChunk,
		historyN:       opts.HistoryN,
		retrieveK:      opts.RetrieveK,
		summaryEvery:   opts.SummaryEvery,
		summaryTarget:  opts.SummaryTarget,
		attachmentsDir: opts.AttachmentsDir,
	}
}

// upsertConversation finds-or-creates a conversation row and refreshes last_message_at.
func (e *Engine) upsertConversation(ctx context.Context, bindingID, peerID, sessionID string) (*model.Conversation, error) {
	var conv model.Conversation
	err := e.db.WithContext(ctx).
		Where("binding_id = ? AND ilink_user_id = ?", bindingID, peerID).
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
			case ilink.ItemTypeImage, ilink.ItemTypeFile, ilink.ItemTypeVoice:
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

	reply, err := e.generateReply(ctx, persona, conv, llmClient, text)
	if err != nil {
		e.log.Error("generate reply failed", zap.Error(err))
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

	typingTicket := e.ensureTypingTicket(ctx, binding, sess, msg.FromUserID, msg.ContextToken)
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
			out.Status = "failed"
			_ = e.db.WithContext(ctx).Save(&out).Error
			e.log.Error("sendmessage failed",
				zap.Error(err),
				zap.Int("chunk_idx", i),
				zap.String("binding_id", binding.ID))
			stopTyping()
			return err
		}
		out.Status = "sent"
		if err := e.db.WithContext(ctx).Save(&out).Error; err != nil {
			stopTyping()
			return err
		}
		if i+1 < len(chunks) {
			time.Sleep(400 * time.Millisecond)
		}
	}
	stopTyping()

	go func(snippet string) {
		defer func() {
			if rec := recover(); rec != nil {
				e.log.Error("ingestMemory goroutine panicked",
					zap.Any("panic", rec),
					zap.String("conversation_id", conv.ID))
			}
		}()
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		e.ingestMemory(bg, persona.ID, conv.ID, inboundMsg.ID, llmClient, snippet)
	}(buildSnippet(text, reply))

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

// generateReply builds the prompt and calls the LLM.
func (e *Engine) generateReply(ctx context.Context, persona *model.Persona, conv *model.Conversation, llmClient *llm.Client, userText string) (string, error) {
	var prior int64
	_ = e.db.WithContext(ctx).Model(&model.Message{}).
		Where("conversation_id = ? AND direction = ?", conv.ID, "outbound").
		Count(&prior).Error
	firstInteraction := prior == 0

	msgs := []llm.ChatMessage{
		{Role: "system", Content: buildSystemPrompt(persona, firstInteraction)},
	}

	// 1) Rolling summary of older turns (older than summary_updated_at).
	if strings.TrimSpace(conv.Summary) != "" {
		msgs = append(msgs, llm.ChatMessage{
			Role:    "system",
			Content: "【你和对方此前对话的累积摘要（保持连续性参考，不要复述）】\n" + conv.Summary,
		})
	}

	// 2) Long-term memory bullets, scoped to this persona + boosted by this conversation.
	if e.mem != nil {
		mems, err := e.mem.RetrieveForConversation(ctx, llmClient, persona.ID, conv.ID, userText, e.retrieveK)
		if err == nil && len(mems) > 0 {
			var b strings.Builder
			b.WriteString("【你需要记住的关于对方的长期信息（不要直接照念，自然融入回复）】\n")
			for _, m := range mems {
				b.WriteString("- ")
				b.WriteString(m.Content)
				b.WriteString("\n")
			}
			msgs = append(msgs, llm.ChatMessage{Role: "system", Content: b.String()})
		}
	}

	// 3) Recent verbatim history — only messages newer than the summary watermark.
	q := e.db.WithContext(ctx).Where("conversation_id = ?", conv.ID)
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

	reply, err := llmClient.Chat(ctx, msgs)
	if err != nil {
		return "", err
	}
	return reply, nil
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

// ensureTypingTicket fetches and caches a typing ticket if absent / stale.
func (e *Engine) ensureTypingTicket(ctx context.Context, binding *model.WeChatBinding, sess ilink.Session, toUserID, contextToken string) string {
	if binding.TypingTicket != "" && time.Since(binding.TypingTicketAt) < 12*time.Hour {
		return binding.TypingTicket
	}
	ticket, err := e.ilink.GetTypingTicket(ctx, sess, toUserID, contextToken)
	if err != nil || ticket == "" {
		return ""
	}
	binding.TypingTicket = ticket
	binding.TypingTicketAt = time.Now()
	_ = e.db.WithContext(ctx).Model(&model.WeChatBinding{}).
		Where("id = ?", binding.ID).
		Updates(map[string]interface{}{
			"typing_ticket":    ticket,
			"typing_ticket_at": binding.TypingTicketAt,
		}).Error
	return ticket
}

// splitForWeChat tries to split text on paragraph boundaries to fit the 2000-char limit.
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
		out = append(out, strings.TrimSpace(remaining[:cut]))
		remaining = strings.TrimLeft(remaining[cut:], "\n ")
	}
	if strings.TrimSpace(remaining) != "" {
		out = append(out, strings.TrimSpace(remaining))
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

func buildSnippet(userText, assistantText string) string {
	var b strings.Builder
	b.WriteString("USER: ")
	b.WriteString(userText)
	b.WriteString("\nASSISTANT: ")
	b.WriteString(assistantText)
	return b.String()
}

func (e *Engine) ingestMemory(ctx context.Context, personaID, convID, sourceMsgID string, llmClient *llm.Client, snippet string) {
	if e.mem == nil {
		return
	}
	facts, err := e.mem.ExtractFacts(ctx, llmClient, snippet)
	if err != nil {
		e.log.Debug("extract facts failed", zap.Error(err))
		return
	}
	for _, f := range facts {
		if strings.TrimSpace(f.Content) == "" {
			continue
		}
		_ = e.mem.Ingest(ctx, llmClient, personaID, convID, sourceMsgID, f.Kind, f.Content, f.Importance)
	}
}

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
			Where("binding_id = ? AND ilink_user_id = ?", binding.ID, peerUserID).
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
			Where("binding_id = ? AND ilink_user_id = ?", binding.ID, peerUserID).
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
	msgs := []llm.ChatMessage{
		{Role: "system", Content: buildSystemPrompt(persona, false)},
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
		_ = e.db.WithContext(ctx).Create(&out).Error
	}
	now := time.Now()
	_ = e.db.WithContext(ctx).Model(&model.WeChatBinding{}).
		Where("id = ?", binding.ID).
		Update("last_proactive_at", now).Error
	_ = e.db.WithContext(ctx).Model(&model.Conversation{}).
		Where("id = ?", conv.ID).
		Update("last_message_at", now).Error
	return nil
}

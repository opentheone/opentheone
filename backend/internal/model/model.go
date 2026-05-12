package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BaseModel is shared by every table.
type BaseModel struct {
	ID        string    `gorm:"primaryKey;type:varchar(36)" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// BeforeCreate fills UUID when not set.
func (b *BaseModel) BeforeCreate(_ *gorm.DB) error {
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	return nil
}

// User account.
type User struct {
	BaseModel
	Username     string `gorm:"uniqueIndex;size:64;not null" json:"username"`
	PasswordHash string `gorm:"not null" json:"-"`
	DisplayName  string `gorm:"size:64" json:"display_name"`
	Role         string `gorm:"size:16;default:user" json:"role"` // admin / user
}

// LLMConfig stores OpenAI-compatible endpoints and credentials.
type LLMConfig struct {
	BaseModel
	UserID         string  `gorm:"index;size:36;not null" json:"user_id"`
	Name           string  `gorm:"size:64;not null" json:"name"`
	BaseURL        string  `gorm:"size:255;not null" json:"base_url"`
	APIKeyEnc      string  `gorm:"type:text" json:"-"` // encrypted
	ChatModel      string  `gorm:"size:128;not null" json:"chat_model"`
	EmbeddingModel string  `gorm:"size:128" json:"embedding_model"`
	Temperature    float32 `gorm:"default:0.8" json:"temperature"`
	MaxTokens      int     `gorm:"default:1024" json:"max_tokens"`
	IsDefault      bool    `gorm:"default:false" json:"is_default"`
}

// Persona is an AI role/persona created by user.
type Persona struct {
	BaseModel
	UserID          string `gorm:"index;size:36;not null" json:"user_id"`
	Name            string `gorm:"size:64;not null" json:"name"`
	Avatar          string `gorm:"size:255" json:"avatar"`
	Description     string `gorm:"type:text" json:"description"`
	SystemPrompt    string `gorm:"type:text" json:"system_prompt"`
	Greeting        string `gorm:"type:text" json:"greeting"`
	SpeakingStyle   string `gorm:"type:text" json:"speaking_style"`
	ProactiveCron   string `gorm:"size:64" json:"proactive_cron"`
	ProactivePrompt string `gorm:"type:text" json:"proactive_prompt"`
	IsActive        bool   `gorm:"default:false" json:"is_active"`
	LLMConfigID     string `gorm:"size:36" json:"llm_config_id"`
}

// WeChatBinding stores ClawBot/iLink credentials and runtime cursor for a persona.
type WeChatBinding struct {
	BaseModel
	UserID           string    `gorm:"index;size:36;not null" json:"user_id"`
	PersonaID        string    `gorm:"index;size:36;not null" json:"persona_id"`
	State            string    `gorm:"size:32;default:pending_scan" json:"state"` // pending_scan / active / expired / revoked
	BotToken         string    `gorm:"type:text" json:"-"`
	BaseURL          string    `gorm:"size:255" json:"base_url"`
	ILinkBotID       string    `gorm:"size:128" json:"ilink_bot_id"`
	ILinkUserID      string    `gorm:"size:128" json:"ilink_user_id"`
	GetUpdatesBuf    string    `gorm:"type:text" json:"-"`
	LastContextToken string    `gorm:"type:text" json:"-"`
	TypingTicket     string    `gorm:"type:text" json:"-"`
	TypingTicketAt   time.Time `json:"-"`
	QRCodeToken      string    `gorm:"size:128" json:"-"`
	QRCodeImageURL   string    `gorm:"size:255" json:"qrcode_image_url"`
	ScanPhase        string    `gorm:"size:16" json:"scan_phase"` // wait / scanned / confirmed / expired
	LastProactiveAt  time.Time `json:"last_proactive_at"`
}

// Conversation represents a chat thread between a binding and a remote WeChat user.
type Conversation struct {
	BaseModel
	BindingID        string    `gorm:"index;size:36;not null" json:"binding_id"`
	ILinkUserID      string    `gorm:"index;size:128;not null" json:"ilink_user_id"`
	SessionID        string    `gorm:"size:255" json:"session_id"`
	Nickname         string    `gorm:"size:128" json:"nickname"`
	LastMessageAt    time.Time `json:"last_message_at"`
	LastContextToken string    `gorm:"type:text" json:"-"`

	// Rolling-summary compression: when a conversation grows past the recent
	// window, older messages get folded into Summary so the LLM still has the
	// thread of past topics without paying O(N) token cost every turn.
	// SummaryUntilMessageID is the id of the latest message included in the
	// current summary; subsequent messages up to that timestamp are considered
	// "already summarized" and will not be fed verbatim again. Together these
	// form the LangChain-style ConversationSummaryBufferMemory pattern.
	Summary               string    `gorm:"type:text" json:"summary"`
	SummaryUntilMessageID string    `gorm:"size:36" json:"summary_until_message_id"`
	SummaryUpdatedAt      time.Time `json:"summary_updated_at"`
}

// Message stores both inbound and outbound messages.
type Message struct {
	BaseModel
	ConversationID string `gorm:"index;size:36;not null" json:"conversation_id"`
	Direction      string `gorm:"size:16;not null" json:"direction"` // inbound / outbound
	ILinkMessageID int64  `gorm:"index" json:"ilink_message_id"`
	ClientID       string `gorm:"size:128" json:"client_id"`
	ContextToken   string `gorm:"type:text" json:"-"`
	Type           string `gorm:"size:16;default:text" json:"type"` // text/image/voice/file/video
	Text           string `gorm:"type:text" json:"text"`
	MediaURL       string `gorm:"size:255" json:"media_url"`
	Extra          string `gorm:"type:text" json:"extra"`
	Status         string `gorm:"size:16;default:received" json:"status"`
}

// Memory is one long-term memory fragment for a persona.
type Memory struct {
	BaseModel
	PersonaID       string `gorm:"index;size:36;not null" json:"persona_id"`
	ConversationID  string `gorm:"size:36" json:"conversation_id"`
	Kind            string `gorm:"size:32;default:fact" json:"kind"` // fact/preference/event/summary
	Content         string `gorm:"type:text" json:"content"`
	Embedding       []byte `gorm:"type:blob" json:"-"`
	Importance      int    `gorm:"default:5" json:"importance"`
	SourceMessageID string `gorm:"size:36" json:"source_message_id"`
}

// Attachment for downloaded media.
type Attachment struct {
	BaseModel
	MessageID string `gorm:"index;size:36;not null" json:"message_id"`
	Kind      string `gorm:"size:16" json:"kind"`
	LocalPath string `gorm:"size:255" json:"local_path"`
	Size      int64  `json:"size"`
	Mime      string `gorm:"size:64" json:"mime"`
}

// SystemSetting is a key-value row for runtime-mutable global settings.
// Seeded on first start from config.yaml, mutable by admins via /api/admin/settings.
type SystemSetting struct {
	BaseModel
	Key   string `gorm:"uniqueIndex;size:64;not null" json:"key"`
	Value string `gorm:"type:text" json:"value"`
}

// AllModels returns the list passed to AutoMigrate.
func AllModels() []interface{} {
	return []interface{}{
		&User{},
		&LLMConfig{},
		&Persona{},
		&WeChatBinding{},
		&Conversation{},
		&Message{},
		&Memory{},
		&Attachment{},
		&SystemSetting{},
	}
}

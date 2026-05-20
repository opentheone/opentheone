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
	UserID    string `gorm:"index;size:36;not null" json:"user_id"`
	Name      string `gorm:"size:64;not null" json:"name"`
	BaseURL   string `gorm:"size:255;not null" json:"base_url"`
	APIKeyEnc string `gorm:"type:text" json:"-"` // encrypted
	ChatModel string `gorm:"size:128;not null" json:"chat_model"`
	// Temperature has no GORM `default:` tag on purpose: with `default:` set,
	// GORM omits zero values from the INSERT statement so the database default
	// kicks in — which silently rewrites an explicitly requested
	// `temperature=0` (deterministic decoding) to 0.8. The handler layer is
	// now the single source of truth for the "missing field → 0.8" rule, so
	// any value the application hands GORM is written verbatim.
	Temperature float32 `json:"temperature"`
	MaxTokens   int     `gorm:"default:1024" json:"max_tokens"`
	IsDefault   bool    `gorm:"default:false" json:"is_default"`
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
	// EnabledMCPIDs is a JSON-encoded []string of MCPServer IDs this persona
	// is allowed to use as agent tools during chat. Empty string == no MCP
	// tools enabled (chat falls back to plain LLM completion, no agent loop).
	EnabledMCPIDs string `gorm:"type:text" json:"enabled_mcp_ids"`
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
	// QRCodeImageURL is misnamed for historical reasons: it stores the URL that
	// should be **encoded into** a QR code (i.e. what a scanning WeChat client
	// will decode), NOT a URL that points to an image. The handler layer is
	// responsible for rendering it into a PNG / data-URI before shipping it
	// to the frontend. See handler/binding.go::renderQRDataURI.
	QRCodeImageURL  string    `gorm:"size:255" json:"-"`
	ScanPhase       string    `gorm:"size:16" json:"scan_phase"` // wait / scanned / confirmed / expired
	LastProactiveAt time.Time `json:"last_proactive_at"`
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

// Message stores both inbound and outbound messages, plus the auxiliary
// agent-loop bookkeeping rows ("tool_call" + "tool_result") so the user can
// audit what the AI did under the hood in the conversation detail view.
//
// Direction values:
//   - "inbound" / "outbound": real WeChat messages.
//   - "tool_call": the assistant decided to call an MCP tool. ToolName +
//     ToolCallID + ToolArgs describe the request; Text is empty.
//   - "tool_result": the MCP tool's response (possibly truncated). ToolName +
//     ToolCallID match the corresponding tool_call row; Text is empty;
//     ToolResult holds the rendered content.
//
// These two new directions are *not* sent over iLink, never carry a
// context_token, and never participate in the rolling summary or
// long-term memory ingestion. They're purely UI/audit rows.
type Message struct {
	BaseModel
	ConversationID string `gorm:"index;size:36;not null" json:"conversation_id"`
	Direction      string `gorm:"size:16;not null" json:"direction"` // inbound / outbound / tool_call / tool_result
	ILinkMessageID int64  `gorm:"index" json:"ilink_message_id"`
	ClientID       string `gorm:"size:128" json:"client_id"`
	ContextToken   string `gorm:"type:text" json:"-"`
	Type           string `gorm:"size:16;default:text" json:"type"` // text/image/voice/file/video/tool
	Text           string `gorm:"type:text" json:"text"`
	MediaURL       string `gorm:"size:255" json:"media_url"`
	Extra          string `gorm:"type:text" json:"extra"`
	Status         string `gorm:"size:16;default:received" json:"status"`
	// Agent-loop fields (only set for direction in tool_call / tool_result):
	ToolName   string `gorm:"size:128" json:"tool_name,omitempty"`
	ToolCallID string `gorm:"size:128" json:"tool_call_id,omitempty"`
	ToolArgs   string `gorm:"type:text" json:"tool_args,omitempty"`
	ToolResult string `gorm:"type:text" json:"tool_result,omitempty"`
}

// Memory is an L1 atomic memory record extracted from conversations.
//
// Kind taxonomy (TencentDB-Agent-Memory inspired):
//   - persona: stable user attributes ("用户喜欢手冲咖啡")
//   - episodic: dated events ("用户 2026-05-19 完成了项目交付")
//   - instruction: long-term behavior rules for AI ("以后都叫我猫猫")
//
// The legacy kinds (fact/preference/event/summary) are still readable for
// rows written by older builds; the extractor now only emits the three above.
//
// Vector embeddings are GONE — retrieval is now BM25 (over `memories_fts`)
// plus LLM-driven hierarchical summarization (L2 scene / L3 user profile).
type Memory struct {
	BaseModel
	PersonaID       string `gorm:"index;size:36;not null" json:"persona_id"`
	ConversationID  string `gorm:"size:36" json:"conversation_id"`
	Kind            string `gorm:"size:32;default:persona" json:"kind"`
	Content         string `gorm:"type:text" json:"content"`
	Importance      int    `gorm:"default:5" json:"importance"`
	SourceMessageID string `gorm:"size:36" json:"source_message_id"`
	// SceneID is the L2 scene this atom belongs to. Empty until the L2
	// extractor has run on it.
	SceneID string `gorm:"index;size:36" json:"scene_id"`
	// ActivityStart / ActivityEnd are only meaningful for `episodic`
	// memories where the extractor could nail down absolute times.
	ActivityStart *time.Time `json:"activity_start,omitempty"`
	ActivityEnd   *time.Time `json:"activity_end,omitempty"`
	// Metadata is a free-form JSON blob for future expansion (e.g.
	// extractor-provided tags). Always emitted by the API even when empty.
	Metadata string `gorm:"type:text" json:"metadata"`
	// Status tracks soft-delete / merge state:
	//   active     — live, returned by retrieval
	//   superseded — replaced by another atom (see SupersededBy)
	//   archived   — soft-deleted by user / janitor
	Status       string `gorm:"size:16;default:active;index" json:"status"`
	SupersededBy string `gorm:"size:36" json:"superseded_by,omitempty"`
}

// MemoryScene is an L2 thematic block grouping related L1 atoms. The LLM
// extractor maintains these via a "fit-or-create-or-merge" loop with a hard
// cap (default 15) to force consolidation as the user accumulates memories.
type MemoryScene struct {
	BaseModel
	PersonaID  string     `gorm:"index;size:36;not null" json:"persona_id"`
	Title      string     `gorm:"size:128;not null" json:"title"` // "AI 在和 X 讨论 Y"
	Summary    string     `gorm:"type:text" json:"summary"`       // one-liner for system-prompt index
	Content    string     `gorm:"type:text" json:"content"`       // full markdown body
	Heat       int        `gorm:"default:0" json:"heat"`          // recall counter
	AtomCount  int        `gorm:"default:0" json:"atom_count"`
	LastAtomAt *time.Time `json:"last_atom_at,omitempty"`
}

// UserProfile is the L3 synthesized portrait of the USER (not the AI).
// One row per persona. Injected verbatim into every system prompt as a stable
// "who is this user" preamble. Bounded to ~2000 chars to keep prompts cheap.
type UserProfile struct {
	BaseModel
	PersonaID       string    `gorm:"uniqueIndex;size:36;not null" json:"persona_id"`
	Content         string    `gorm:"type:text" json:"content"` // markdown
	SceneCountAtGen int       `json:"scene_count_at_gen"`
	AtomsAtGen      int       `json:"atoms_at_gen"`
	GeneratedAt     time.Time `json:"generated_at"`
	RequestReason   string    `gorm:"size:255" json:"request_reason"`
}

// MemoryPipelineState tracks per-persona state for L3 user-profile generation
// (counters that aggregate across all conversations of one persona, plus the
// L3 cool-down). Per-conversation watermarks live in MemoryExtractCheckpoint
// instead — a persona usually has multiple WeChat peers, each with its own
// message stream and warmup curve, so the L1/L2 extraction checkpoint must
// be keyed at conversation granularity.
type MemoryPipelineState struct {
	PersonaID              string    `gorm:"primaryKey;size:36" json:"persona_id"`
	AtomsSinceLastProfile  int       `json:"atoms_since_last_profile"`
	ScenesSinceLastProfile int       `json:"scenes_since_last_profile"`
	LastL3At               time.Time `json:"last_l3_at"`
	RequestProfileUpdate   bool      `json:"request_profile_update"`
	ProfileUpdateReason    string    `gorm:"size:255" json:"profile_update_reason"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// MemoryExtractCheckpoint is a per-(persona, conversation) watermark that
// drives L1 extraction and L2 scene fitting. A single persona can have many
// concurrent WeChat conversations; each carries its own warmup curve and
// last-extracted message id so the pipeline doesn't accidentally skip /
// reprocess messages when several threads interleave.
//
// NextThreshold drives the warmup ramp: 1 → 2 → 4 → 8 → 16 → 16 ... per
// conversation. Once a conversation hits 16, every subsequent batch waits for
// 16 fresh messages or the idle timeout, whichever comes first.
type MemoryExtractCheckpoint struct {
	PersonaID              string    `gorm:"primaryKey;size:36" json:"persona_id"`
	ConversationID         string    `gorm:"primaryKey;size:36" json:"conversation_id"`
	LastExtractedMessageID string    `gorm:"size:36" json:"last_extracted_message_id"`
	TotalProcessed         int       `json:"total_processed"`
	LastL1At               time.Time `json:"last_l1_at"`
	LastL2At               time.Time `json:"last_l2_at"`
	NextThreshold          int       `gorm:"default:1" json:"next_threshold"`
	UpdatedAt              time.Time `json:"updated_at"`
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

// MCPServer is one user-configured Model Context Protocol server.
// Two transports are supported:
//   - "stdio": local subprocess, fields Command/Args/Env are used.
//   - "streamable_http": remote HTTP(S) endpoint per the MCP 2025-03-26 spec,
//     fields URL/Headers are used.
//
// Args/Env/Headers are JSON-encoded for portability across drivers (sqlite
// has no native array column).
type MCPServer struct {
	BaseModel
	UserID      string `gorm:"index;size:36;not null" json:"user_id"`
	Name        string `gorm:"size:64;not null" json:"name"`
	Description string `gorm:"type:text" json:"description"`
	// Transport: "stdio" or "streamable_http".
	Transport string `gorm:"size:32;not null;default:stdio" json:"transport"`
	// stdio fields:
	Command string `gorm:"size:255" json:"command"`
	Args    string `gorm:"type:text" json:"args"` // JSON []string
	Env     string `gorm:"type:text" json:"env"`  // JSON map[string]string
	// streamable_http fields:
	URL     string `gorm:"size:512" json:"url"`
	Headers string `gorm:"type:text" json:"headers"` // JSON map[string]string
	// Enabled is a global on/off; even if a persona references it, a disabled
	// server is skipped during tool discovery.
	// Enabled has no GORM `default:` tag: see LLMConfig.Temperature above for
	// the rationale. With `default:true`, GORM omits the zero value (`false`)
	// from the INSERT, so a client request `{"enabled": false}` would land
	// as `enabled = true` in the database. The handler layer always sets
	// Enabled explicitly (defaulting to true when the field is absent), so
	// the column-level default is unnecessary AND actively harmful.
	Enabled bool `json:"enabled"`
	// TimeoutMs caps a single tool invocation. 0 falls back to a 30s default.
	TimeoutMs int `gorm:"default:30000" json:"timeout_ms"`
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
		&MemoryScene{},
		&UserProfile{},
		&MemoryPipelineState{},
		&MemoryExtractCheckpoint{},
		&Attachment{},
		&SystemSetting{},
		&MCPServer{},
	}
}

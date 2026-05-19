package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	mcpproto "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/model"
)

// LLMTool is the OpenAI-compatible tool descriptor we hand to the LLM.
// We deliberately keep the shape independent of the openai SDK so the
// engine/llm packages don't pick up a transitive dependency on mcp-go.
type LLMTool struct {
	// Name is the prefixed tool name (mcp__<serverID>__<toolName>).
	Name string `json:"name"`
	// Description merges the upstream description with the server's name
	// so the LLM has enough context to pick the right one.
	Description string `json:"description"`
	// Parameters is a JSON-Schema object describing arguments.
	// Always at least `{"type":"object","properties":{}}`.
	Parameters json.RawMessage `json:"parameters"`
}

// Registry resolves prefixed tool names back to (server, tool) pairs for
// one persona's chat turn. It is short-lived: build it once at the start
// of a turn, throw it away when done. Internally it shares the process-
// wide Manager for actual connections.
type Registry struct {
	mgr *Manager
	log *zap.Logger
	// servers indexed by MCPServer.ID (the real UUID).
	servers map[string]*model.MCPServer
	// tools indexed by prefixed LLM-facing name.
	tools map[string]LLMTool
	// shortToID maps the compact short id we expose to the LLM (e.g. "s0")
	// back to the real MCPServer.ID. The short form keeps prefixed tool names
	// well within OpenAI's 64-char tool-name budget; a UUID would burn 36 of
	// those 64 chars on the server identifier alone, silently dropping any
	// tool whose own name pushed it over the limit.
	shortToID map[string]string
	mu        sync.Mutex
}

// LoadForPersona builds a registry containing every enabled MCP tool the
// given persona has opted in to. Servers that fail to come up are logged
// and skipped — one broken MCP server should not kill a chat turn.
//
// If the persona has zero enabled servers (or every enabled server failed),
// the registry will report Empty() == true and the engine should fall back
// to a plain (no-tool) chat completion.
func LoadForPersona(ctx context.Context, db *gorm.DB, mgr *Manager, log *zap.Logger, persona *model.Persona) *Registry {
	r := &Registry{
		mgr:       mgr,
		log:       log.With(zap.String("subsys", "mcp_registry")),
		servers:   make(map[string]*model.MCPServer),
		tools:     make(map[string]LLMTool),
		shortToID: make(map[string]string),
	}
	if persona == nil {
		return r
	}
	ids := DecodeEnabledIDs(persona.EnabledMCPIDs)
	if len(ids) == 0 {
		return r
	}
	var rows []model.MCPServer
	if err := db.WithContext(ctx).
		Where("id IN ? AND user_id = ? AND enabled = ?", ids, persona.UserID, true).
		Find(&rows).Error; err != nil {
		r.log.Warn("load mcp servers failed", zap.Error(err))
		return r
	}
	for i := range rows {
		srv := &rows[i]
		tools, err := mgr.ListTools(ctx, srv)
		if err != nil {
			r.log.Warn("mcp server unavailable, skipping",
				zap.String("server_id", srv.ID),
				zap.String("name", srv.Name),
				zap.Error(err))
			continue
		}
		short := "s" + strconv.Itoa(len(r.servers))
		r.servers[srv.ID] = srv
		r.shortToID[short] = srv.ID
		for _, t := range tools {
			lt, ok := convertTool(short, srv, t)
			if !ok {
				r.log.Warn("mcp tool skipped (name exceeds OpenAI 64-char tool budget or has invalid chars)",
					zap.String("server", srv.Name),
					zap.String("tool", t.Name))
				continue
			}
			r.tools[lt.Name] = lt
		}
	}
	return r
}

// Empty reports whether the registry has zero available tools.
func (r *Registry) Empty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tools) == 0
}

// Tools returns the LLM-facing tool descriptors, sorted by name for
// deterministic prompts.
func (r *Registry) Tools() []LLMTool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]LLMTool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Invoke routes a tool call (as decided by the LLM) to the correct backing
// MCP server, runs it, and returns the textual result + isError flag.
// The result string is the concatenated TextContent items from the MCP
// CallToolResult; non-text content is summarized so the LLM at least knows
// something was returned.
func (r *Registry) Invoke(ctx context.Context, toolName string, args map[string]any) (string, bool, error) {
	r.mu.Lock()
	if _, ok := r.tools[toolName]; !ok {
		r.mu.Unlock()
		return "", true, fmt.Errorf("unknown tool %q", toolName)
	}
	r.mu.Unlock()

	short, mcpToolName, ok := DecodeToolName(toolName)
	if !ok {
		return "", true, fmt.Errorf("malformed tool name %q", toolName)
	}
	r.mu.Lock()
	realID, ok := r.shortToID[short]
	var srv *model.MCPServer
	if ok {
		srv, ok = r.servers[realID]
	}
	r.mu.Unlock()
	if !ok {
		return "", true, fmt.Errorf("server for tool %q is not loaded", toolName)
	}

	res, err := r.mgr.CallTool(ctx, srv, mcpToolName, args)
	if err != nil {
		return "", true, err
	}
	return renderResult(res), res.IsError, nil
}

// convertTool maps one MCP tool descriptor to its OpenAI-compatible LLM
// equivalent. shortID is the compact identifier the Registry assigned to
// this server (e.g. "s0") so the prefixed tool name stays well inside
// OpenAI's 64-char budget. Returns ok=false for tools whose name produces
// an invalid prefixed identifier (we keep things conservative — OpenAI's
// tool names must match `^[a-zA-Z0-9_-]+$`).
func convertTool(shortID string, srv *model.MCPServer, t mcpproto.Tool) (LLMTool, bool) {
	name := EncodeToolName(shortID, t.Name)
	if !validToolName(name) {
		return LLMTool{}, false
	}
	desc := strings.TrimSpace(t.Description)
	if t.Title != "" && t.Title != t.Name {
		desc = strings.TrimSpace(t.Title) + " — " + desc
	}
	// Server-level description (e.g. "this is my private knowledge base")
	// is configured by the user and is invaluable context for the model when
	// it has to choose between two servers that expose similar tool names.
	// Inject it once at the start so each tool description gets it for free.
	srvDesc := strings.TrimSpace(srv.Description)
	prefix := fmt.Sprintf("[%s] ", srv.Name)
	if srvDesc != "" {
		prefix = fmt.Sprintf("[%s — %s] ", srv.Name, srvDesc)
	}
	if !strings.HasPrefix(desc, prefix) {
		desc = prefix + desc
	}

	var params json.RawMessage
	if len(t.RawInputSchema) > 0 {
		params = json.RawMessage(t.RawInputSchema)
	} else {
		// MarshalJSON for ToolInputSchema is custom; round-trip via that.
		buf, err := json.Marshal(t.InputSchema)
		if err != nil || len(buf) == 0 || string(buf) == "null" {
			buf = []byte(`{"type":"object","properties":{}}`)
		}
		params = json.RawMessage(buf)
	}
	// Some MCP servers send `{}` (no type), but the OpenAI API requires the
	// outer schema to be an object schema. Patch it in if missing.
	params = ensureObjectSchema(params)

	return LLMTool{
		Name:        name,
		Description: desc,
		Parameters:  params,
	}, true
}

func ensureObjectSchema(raw json.RawMessage) json.RawMessage {
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil || asMap == nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	if _, ok := asMap["type"]; !ok {
		asMap["type"] = "object"
	}
	if _, ok := asMap["properties"]; !ok {
		asMap["properties"] = map[string]any{}
	}
	buf, err := json.Marshal(asMap)
	if err != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return buf
}

// validToolName enforces OpenAI's tool naming constraint.
func validToolName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// maxToolResultRunes caps how much text from a single tool call we feed back
// into the LLM context. Without this, a single rogue tool (e.g. `grep` on a
// large file) can blow up the next chat completion's input tokens — both
// expensive and likely to exceed the model's context window. 16k runes is
// roughly 4-8k tokens depending on language; plenty for typical tool output.
const maxToolResultRunes = 16000

// renderResult flattens an MCP CallToolResult into a single string the LLM
// can read. TextContent is concatenated verbatim; other content kinds get
// a short placeholder so the LLM at least knows they exist. Output is
// truncated to maxToolResultRunes with a visible "...(truncated)" suffix so
// the model knows it didn't get the whole thing.
func renderResult(res *mcpproto.CallToolResult) string {
	if res == nil {
		return ""
	}
	if res.StructuredContent != nil {
		if buf, err := json.Marshal(res.StructuredContent); err == nil && len(buf) > 0 {
			return clipRunes(string(buf), maxToolResultRunes)
		}
	}
	var parts []string
	for _, c := range res.Content {
		switch v := c.(type) {
		case mcpproto.TextContent:
			parts = append(parts, v.Text)
		case *mcpproto.TextContent:
			if v != nil {
				parts = append(parts, v.Text)
			}
		case mcpproto.ImageContent:
			parts = append(parts, fmt.Sprintf("[image:%s,%d bytes]", v.MIMEType, len(v.Data)))
		case mcpproto.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio:%s,%d bytes]", v.MIMEType, len(v.Data)))
		default:
			parts = append(parts, fmt.Sprintf("[%T]", v))
		}
	}
	return clipRunes(strings.TrimSpace(strings.Join(parts, "\n")), maxToolResultRunes)
}

func clipRunes(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "\n...(truncated, total " + fmt.Sprintf("%d", len(r)) + " chars)"
}

// ErrEmptyToolList is returned when the persona's registry has no tools.
var ErrEmptyToolList = errors.New("mcp: no tools available")

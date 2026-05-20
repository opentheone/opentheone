package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/opentheone/opentheone/backend/internal/mcp"
	"github.com/opentheone/opentheone/backend/internal/model"
)

// builtinTools is the static catalog of memory tools the engine offers to
// the LLM alongside any MCP-provided tools. These are always available;
// they don't require any MCP server configuration.
//
// All names are prefixed with `oto_` so they never collide with MCP tool
// names (which use the `mcp__` prefix — see mcp/registry.go).
//
// The shape is mcp.LLMTool because that's what the agent loop already
// understands; we don't want to introduce a parallel tool descriptor type.
var builtinTools = []mcp.LLMTool{
	{
		Name:        "oto_memory_search",
		Description: "在用户的长期记忆中按关键词检索原子事实。当你需要确认用户的偏好、身份、对你的指令时使用。返回最相关的若干条记忆。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {"type": "string", "description": "中文关键词或短语，例如「咖啡偏好」「起床时间」"},
				"limit": {"type": "integer", "description": "返回条目上限，默认 5，最大 20", "default": 5}
			},
			"required": ["query"]
		}`),
	},
	{
		Name:        "oto_scene_read",
		Description: "读取一个主题场景的完整内容（标题 + 摘要 + 全部归属其下的原子事实）。先用 oto_memory_search 拿到 scene_id，或者从系统提示中的场景索引拿到标题再 list。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"scene_id": {"type": "string", "description": "场景 uuid"}
			},
			"required": ["scene_id"]
		}`),
	},
	{
		Name:        "oto_conversation_search",
		Description: "在历史聊天消息原文中按关键词检索（仅当前会话）。当你需要回忆某段具体对话原话时使用，而不是用记忆系统。",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {"type": "string", "description": "中文关键词"},
				"limit": {"type": "integer", "description": "返回条数上限，默认 5，最大 20", "default": 5}
			},
			"required": ["query"]
		}`),
	},
}

// builtinToolNames is a quick lookup for routing in the agent loop.
var builtinToolNames = func() map[string]bool {
	out := make(map[string]bool, len(builtinTools))
	for _, t := range builtinTools {
		out[t.Name] = true
	}
	return out
}()

// isBuiltinTool reports whether a tool name routes to the engine's built-in
// handler instead of the MCP registry.
func isBuiltinTool(name string) bool { return builtinToolNames[name] }

// invokeBuiltinTool runs one built-in tool call. Returns (textOutput,
// isError, err). The contract mirrors registry.Invoke so the agent loop
// can switch on isBuiltinTool() without diverging code paths.
func (e *Engine) invokeBuiltinTool(ctx context.Context, name string, args map[string]any, personaID, conversationID string) (string, bool, error) {
	switch name {
	case "oto_memory_search":
		return e.toolMemorySearch(ctx, args, personaID)
	case "oto_scene_read":
		return e.toolSceneRead(ctx, args, personaID)
	case "oto_conversation_search":
		return e.toolConversationSearch(ctx, args, conversationID)
	}
	return "", true, fmt.Errorf("unknown builtin tool %q", name)
}

func (e *Engine) toolMemorySearch(ctx context.Context, args map[string]any, personaID string) (string, bool, error) {
	if e.mem == nil {
		return "", true, errors.New("memory service not available")
	}
	query := pickString(args, "query")
	if strings.TrimSpace(query) == "" {
		return "", true, errors.New("query is required")
	}
	limit := pickInt(args, "limit", 5)
	if limit > 20 {
		limit = 20
	}
	rows, err := e.mem.SearchMemories(ctx, personaID, query, limit)
	if err != nil {
		return "", true, err
	}
	if len(rows) == 0 {
		return "（未找到匹配的长期记忆）", false, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "找到 %d 条相关记忆：\n", len(rows))
	for i, r := range rows {
		fmt.Fprintf(&b, "%d. [kind=%s | imp=%d", i+1, r.Kind, r.Importance)
		if r.SceneID != "" {
			fmt.Fprintf(&b, " | scene_id=%s", r.SceneID)
		}
		fmt.Fprintf(&b, "] %s\n", strings.TrimSpace(r.Content))
	}
	return b.String(), false, nil
}

func (e *Engine) toolSceneRead(ctx context.Context, args map[string]any, personaID string) (string, bool, error) {
	if e.mem == nil {
		return "", true, errors.New("memory service not available")
	}
	sceneID := pickString(args, "scene_id")
	if strings.TrimSpace(sceneID) == "" {
		return "", true, errors.New("scene_id is required")
	}
	sc, atoms, err := e.mem.GetScene(ctx, personaID, sceneID)
	if err != nil {
		return "", true, fmt.Errorf("scene not found: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "场景：%s\n摘要：%s\n", sc.Title, strings.TrimSpace(sc.Summary))
	if strings.TrimSpace(sc.Content) != "" {
		fmt.Fprintf(&b, "内容：\n%s\n", strings.TrimSpace(sc.Content))
	}
	fmt.Fprintf(&b, "归属原子（%d 条）：\n", len(atoms))
	for i, a := range atoms {
		fmt.Fprintf(&b, "%d. [kind=%s | imp=%d] %s\n", i+1, a.Kind, a.Importance, strings.TrimSpace(a.Content))
	}
	return b.String(), false, nil
}

func (e *Engine) toolConversationSearch(ctx context.Context, args map[string]any, conversationID string) (string, bool, error) {
	if e.mem == nil {
		return "", true, errors.New("memory service not available")
	}
	if conversationID == "" {
		return "", true, errors.New("conversation context not available")
	}
	query := pickString(args, "query")
	if strings.TrimSpace(query) == "" {
		return "", true, errors.New("query is required")
	}
	limit := pickInt(args, "limit", 5)
	if limit > 20 {
		limit = 20
	}
	hits, err := e.mem.BM25().SearchMessages(ctx, conversationID, query, limit)
	if err != nil {
		return "", true, err
	}
	if len(hits) == 0 {
		return "（未找到匹配的历史消息）", false, nil
	}
	// Hydrate to figure out direction / created_at — gives the LLM enough
	// to reason about who said what.
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.MessageID)
	}
	var msgs []model.Message
	if err := e.db.WithContext(ctx).Where("id IN ?", ids).Find(&msgs).Error; err != nil {
		return "", true, err
	}
	byID := map[string]*model.Message{}
	for i := range msgs {
		byID[msgs[i].ID] = &msgs[i]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "找到 %d 条匹配消息：\n", len(hits))
	for i, h := range hits {
		m := byID[h.MessageID]
		who := "USER"
		ts := ""
		if m != nil {
			if m.Direction == "outbound" {
				who = "ASSISTANT"
			}
			ts = m.CreatedAt.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(&b, "%d. [%s | %s] %s\n", i+1, who, ts, strings.TrimSpace(h.Text))
	}
	return b.String(), false, nil
}

func pickString(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func pickInt(args map[string]any, key string, dflt int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		case string:
			// some clients send numeric args as strings
			var i int
			_, err := fmt.Sscan(n, &i)
			if err == nil {
				return i
			}
		}
	}
	return dflt
}

// builtinToolUsageHint is appended to the agent-loop system prompt so the
// LLM knows when these tools are appropriate.
const builtinToolUsageHint = `你有 3 个内置工具用于记忆与历史检索：
- oto_memory_search(query, limit): 长期记忆关键词检索。需要确认用户偏好/身份/对你下达的指令时使用。
- oto_scene_read(scene_id): 读取一个主题场景的完整内容，先通过 memory_search 或系统提示里的场景索引拿到 scene_id。
- oto_conversation_search(query, limit): 当前会话的原文检索。需要回忆具体某段对话原话时使用，而非泛泛复述。

仅在你需要核对事实/原话时调用这些工具，日常闲聊不必调用；返回结果应自然内化到回复中，不要直接复述工具输出。`

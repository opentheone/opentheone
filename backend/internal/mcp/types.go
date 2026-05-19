// Package mcp is the OpenTheOne side of the Model Context Protocol.
// It owns:
//   - a process-wide cache of live MCP client connections (one per
//     configured MCPServer row), so we don't pay the start-up cost on every
//     chat turn;
//   - schema-conversion helpers between MCP tool descriptors and the
//     OpenAI-compatible tool format the LLM expects;
//   - a per-persona registry that resolves prefixed tool names back to the
//     (server, tool) pair to call.
package mcp

import (
	"encoding/json"
	"strings"
)

// ToolName is the OpenAI-visible tool name we expose to the LLM. We prefix
// every MCP-backed tool with `mcp__<serverID>__` so that:
//  1. distinct MCP servers with overlapping tool names don't collide;
//  2. the agent loop can route a tool-call decision back to the originating
//     server by stripping the prefix.
const ToolNamePrefix = "mcp__"

// EncodeToolName makes the OpenAI tool name we hand to the LLM.
func EncodeToolName(serverID, toolName string) string {
	return ToolNamePrefix + serverID + "__" + toolName
}

// DecodeToolName splits a prefixed tool name back into (serverID, toolName).
// Returns ok=false for any name that wasn't produced by EncodeToolName.
func DecodeToolName(prefixed string) (serverID, toolName string, ok bool) {
	if !strings.HasPrefix(prefixed, ToolNamePrefix) {
		return "", "", false
	}
	rest := prefixed[len(ToolNamePrefix):]
	idx := strings.Index(rest, "__")
	if idx <= 0 || idx == len(rest)-2 {
		return "", "", false
	}
	return rest[:idx], rest[idx+2:], true
}

// DecodeEnabledIDs unmarshals a Persona.EnabledMCPIDs JSON string into a list
// of MCPServer IDs. Empty / invalid input degrades silently to nil.
func DecodeEnabledIDs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if s := strings.TrimSpace(id); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// EncodeEnabledIDs is the inverse of DecodeEnabledIDs. nil/empty → "".
func EncodeEnabledIDs(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	buf, err := json.Marshal(ids)
	if err != nil {
		return ""
	}
	return string(buf)
}

// DecodeArgs unmarshals a JSON-encoded []string from the MCPServer.Args column.
func DecodeArgs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil
	}
	return args
}

// DecodeMap unmarshals a JSON-encoded map[string]string. Used for Env/Headers.
func DecodeMap(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}

// EnvSlice converts a map[string]string into the "K=V" slice form expected
// by Go's os/exec and the mcp-go stdio transport.
func EnvSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		if k == "" {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

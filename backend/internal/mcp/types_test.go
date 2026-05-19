package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeDecodeToolName_RoundTrip(t *testing.T) {
	cases := []struct{ server, tool string }{
		{"s0", "list_files"},
		{"abc123", "search"},
		{"s0", "do_one_thing"},
	}
	for _, c := range cases {
		enc := EncodeToolName(c.server, c.tool)
		if !strings.HasPrefix(enc, ToolNamePrefix) {
			t.Errorf("missing prefix: %q", enc)
		}
		gotSrv, gotTool, ok := DecodeToolName(enc)
		if !ok {
			t.Errorf("DecodeToolName(%q) ok=false", enc)
			continue
		}
		if gotSrv != c.server || gotTool != c.tool {
			t.Errorf("round-trip mismatch for (%q,%q): got (%q,%q)", c.server, c.tool, gotSrv, gotTool)
		}
	}
}

// A tool name that itself contains `__` should still survive a round trip
// — the splitter only consumes the FIRST `__` after the prefix.
func TestDecodeToolName_AllowsDoubleUnderscoreInTool(t *testing.T) {
	enc := EncodeToolName("s0", "do__thing")
	srv, tool, ok := DecodeToolName(enc)
	if !ok {
		t.Fatalf("ok=false for %q", enc)
	}
	if srv != "s0" || tool != "do__thing" {
		t.Errorf("got (%q,%q) want (s0, do__thing)", srv, tool)
	}
}

func TestDecodeToolName_RejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"plain",
		"mcp__",
		"mcp____tool",
		"mcp__srv__",
		"not_mcp__srv__tool",
	}
	for _, in := range cases {
		if _, _, ok := DecodeToolName(in); ok {
			t.Errorf("%q should not decode", in)
		}
	}
}

func TestDecodeEnabledIDs(t *testing.T) {
	got := DecodeEnabledIDs(`["a", "b", "  ", "c"]`)
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("got %v", got)
	}
	if DecodeEnabledIDs("") != nil {
		t.Errorf("empty should be nil")
	}
	if DecodeEnabledIDs("not-json") != nil {
		t.Errorf("invalid json should fall back to nil")
	}
}

func TestEncodeEnabledIDs(t *testing.T) {
	if EncodeEnabledIDs(nil) != "" {
		t.Error("nil should encode to empty")
	}
	if EncodeEnabledIDs([]string{}) != "" {
		t.Error("empty slice should encode to empty")
	}
	enc := EncodeEnabledIDs([]string{"x", "y"})
	if enc != `["x","y"]` {
		t.Errorf("got %q", enc)
	}
}

func TestEnvSlice_SkipsEmptyKey(t *testing.T) {
	got := EnvSlice(map[string]string{"": "v", "FOO": "bar"})
	if len(got) != 1 || got[0] != "FOO=bar" {
		t.Errorf("got %v, want [FOO=bar]", got)
	}
}

// validToolName must reject 65-char names. Tool names come from MCP servers
// we don't control; if a single oversized name slips through, the OpenAI API
// rejects the entire chat completion. Skipping individually is the safe
// degraded mode.
func TestValidToolName_LengthBudget(t *testing.T) {
	if !validToolName(strings.Repeat("a", 64)) {
		t.Error("64 chars should be allowed")
	}
	if validToolName(strings.Repeat("a", 65)) {
		t.Error("65 chars must be rejected")
	}
	if validToolName("") {
		t.Error("empty must be rejected")
	}
	if validToolName("has space") {
		t.Error("space must be rejected")
	}
	if validToolName("a-b_c") == false {
		t.Error("hyphens and underscores must be allowed")
	}
}

// ensureObjectSchema must patch `{}` and `null` into a proper object schema,
// otherwise OpenAI's tools=[...] payload rejects the whole completion.
func TestEnsureObjectSchema(t *testing.T) {
	patched := string(ensureObjectSchema(json.RawMessage(`{}`)))
	if !strings.Contains(patched, `"type":"object"`) {
		t.Errorf("missing type=object: %s", patched)
	}
	if !strings.Contains(patched, `"properties":{}`) {
		t.Errorf("missing properties: %s", patched)
	}
	patched = string(ensureObjectSchema(json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)))
	if !strings.Contains(patched, `"x"`) {
		t.Errorf("must preserve existing properties: %s", patched)
	}
	// `null` and garbage degrade to the safe default.
	if got := string(ensureObjectSchema(json.RawMessage(`null`))); !strings.Contains(got, `"type":"object"`) {
		t.Errorf("null fallback: %s", got)
	}
	if got := string(ensureObjectSchema(json.RawMessage(`not-json`))); !strings.Contains(got, `"type":"object"`) {
		t.Errorf("invalid-json fallback: %s", got)
	}
}

// clipRunes must keep multi-byte strings intact (no half-character cuts) and
// indicate truncation only when it actually trimmed.
func TestClipRunes(t *testing.T) {
	if got := clipRunes("hello", 100); got != "hello" {
		t.Errorf("under-limit changed: %q", got)
	}
	long := strings.Repeat("中", 100)
	clipped := clipRunes(long, 10)
	if !strings.Contains(clipped, "truncated") {
		t.Errorf("expected truncated marker: %q", clipped)
	}
	// First 10 chinese chars must be preserved (30 bytes UTF-8).
	if !strings.HasPrefix(clipped, strings.Repeat("中", 10)) {
		t.Errorf("rune-cut violated: %q", clipped)
	}
}

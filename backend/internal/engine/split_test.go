package engine

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/opentheone/opentheone/backend/internal/ilink"
)

func TestSplitForWeChat_NoSplitWhenShort(t *testing.T) {
	in := "hello world"
	out := splitForWeChat(in, 100)
	if len(out) != 1 || out[0] != in {
		t.Fatalf("expected 1 chunk %q, got %v", in, out)
	}
}

func TestSplitForWeChat_EmptyReturnsNil(t *testing.T) {
	if got := splitForWeChat("", 100); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
	if got := splitForWeChat("   \n\n  ", 100); got != nil {
		t.Fatalf("whitespace-only should be nil, got %v", got)
	}
}

func TestSplitForWeChat_PrefersDoubleNewline(t *testing.T) {
	a := strings.Repeat("早安", 30)
	b := strings.Repeat("晚安", 30)
	in := a + "\n\n" + b
	out := splitForWeChat(in, 50)
	if len(out) < 2 {
		t.Fatalf("expected >=2 chunks, got %d", len(out))
	}
	if !strings.HasPrefix(out[0], "早") {
		t.Errorf("first chunk should start with 早, got prefix %q", out[0][:6])
	}
}

func TestSplitForWeChat_ChunksRespectMax(t *testing.T) {
	in := strings.Repeat("a", 2000)
	out := splitForWeChat(in, 500)
	if len(out) < 4 {
		t.Errorf("expected at least 4 chunks, got %d", len(out))
	}
	for i, c := range out {
		if utf8.RuneCountInString(c) > 500 {
			t.Errorf("chunk %d exceeds max: %d runes", i, utf8.RuneCountInString(c))
		}
	}
	// concat should re-produce the original (modulo trimming).
	joined := strings.Join(out, "")
	if joined != in {
		t.Errorf("chunks don't recompose to input")
	}
}

func TestSplitForWeChat_HandlesMultibyteRunes(t *testing.T) {
	in := strings.Repeat("中文", 800)
	out := splitForWeChat(in, 500)
	for i, c := range out {
		if utf8.RuneCountInString(c) > 500 {
			t.Errorf("chunk %d exceeds rune cap: %d", i, utf8.RuneCountInString(c))
		}
	}
}

func TestExtractInboundText_Text(t *testing.T) {
	msg := &ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "hi"}},
			{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "there"}},
		},
	}
	got, kind := extractInboundText(msg)
	if kind != "text" {
		t.Errorf("kind = %q want text", kind)
	}
	if !strings.Contains(got, "hi") || !strings.Contains(got, "there") {
		t.Errorf("missing text pieces: %q", got)
	}
}

func TestExtractInboundText_VoiceWithTranscript(t *testing.T) {
	msg := &ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeVoice, VoiceItem: &ilink.VoiceItem{Text: "你好"}},
		},
	}
	got, kind := extractInboundText(msg)
	if kind != "voice" {
		t.Errorf("kind = %q want voice", kind)
	}
	if got != "你好" {
		t.Errorf("text = %q want 你好", got)
	}
}

func TestExtractInboundText_ImageFallback(t *testing.T) {
	msg := &ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeImage},
		},
	}
	got, kind := extractInboundText(msg)
	if kind != "image" {
		t.Errorf("kind = %q want image", kind)
	}
	if !strings.Contains(got, "image") {
		t.Errorf("expected [image] placeholder, got %q", got)
	}
}

func TestExtractInboundText_NilSafe(t *testing.T) {
	got, kind := extractInboundText(nil)
	if got != "" || kind != "text" {
		t.Errorf("nil msg should return (\"\",\"text\"), got (%q,%q)", got, kind)
	}
}

func TestBuildSnippet(t *testing.T) {
	s := buildSnippet("u", "a")
	if !strings.Contains(s, "USER: u") || !strings.Contains(s, "ASSISTANT: a") {
		t.Errorf("snippet missing roles: %q", s)
	}
}

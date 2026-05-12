package handler

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestRenderQRDataURI locks in the contract that the handler ships a PNG
// data URI for the QR code, not the upstream URL itself. If somebody ever
// regresses this back to "pass the iLink URL straight through", the test
// breaks loudly — because the iLink URL is meant to be ENCODED INTO a QR,
// not USED AS one.
func TestRenderQRDataURI(t *testing.T) {
	if got := renderQRDataURI(""); got != "" {
		t.Errorf("empty input should yield empty output, got %q", got)
	}

	const sample = "https://weixin.qq.com/x/cAbCdEfGhIj"
	got := renderQRDataURI(sample)
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("expected data:image/png;base64,… prefix, got %q", got[:min(64, len(got))])
	}
	if strings.Contains(got, sample) {
		t.Error("output should not contain the raw URL verbatim — it should be encoded into PNG bytes")
	}
	b64 := strings.TrimPrefix(got, "data:image/png;base64,")
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64 body is not valid: %v", err)
	}
	if len(raw) < 100 {
		t.Errorf("PNG payload suspiciously small: %d bytes", len(raw))
	}
	// PNG magic header: 89 50 4E 47 0D 0A 1A 0A
	if raw[0] != 0x89 || raw[1] != 0x50 || raw[2] != 0x4E || raw[3] != 0x47 {
		t.Errorf("decoded payload is not a PNG; first 4 bytes = % x", raw[:4])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

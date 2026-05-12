package engine

import (
	"strings"
	"testing"

	"github.com/wzyjerry/opentheone/backend/internal/model"
)

func TestRenderBatchForSummary_ClipsLongMessages(t *testing.T) {
	long := strings.Repeat("话", 2000)
	batch := []model.Message{
		{Direction: "inbound", Text: long},
		{Direction: "outbound", Text: "好的"},
	}
	got := renderBatchForSummary("", batch, 400)
	if !strings.Contains(got, "(截断)") {
		t.Error("expected long inbound text to be clipped with (截断) marker")
	}
	if !strings.Contains(got, "ASSISTANT: 好的") {
		t.Error("expected short outbound preserved")
	}
}

func TestRenderBatchForSummary_IncludesPrevSummary(t *testing.T) {
	prev := "previous summary text"
	batch := []model.Message{{Direction: "inbound", Text: "hello"}}
	got := renderBatchForSummary(prev, batch, 200)
	if !strings.Contains(got, prev) {
		t.Errorf("missing previous summary, got: %q", got)
	}
	if !strings.Contains(got, "[此前的累积摘要]") {
		t.Error("missing previous summary header")
	}
}

func TestRenderBatchForSummary_SkipsEmptyMessages(t *testing.T) {
	batch := []model.Message{
		{Direction: "inbound", Text: ""},
		{Direction: "inbound", Text: "   "},
		{Direction: "outbound", Text: "real reply"},
	}
	got := renderBatchForSummary("", batch, 200)
	if strings.Contains(got, "USER: \n") {
		t.Error("empty user message should not have been rendered")
	}
	if !strings.Contains(got, "ASSISTANT: real reply") {
		t.Errorf("expected the real reply, got %q", got)
	}
}

func TestRenderBatchForSummary_EmptyPrevSummaryOmitsHeader(t *testing.T) {
	batch := []model.Message{{Direction: "inbound", Text: "hi"}}
	got := renderBatchForSummary("   ", batch, 200)
	if strings.Contains(got, "[此前的累积摘要]") {
		t.Errorf("empty/whitespace prev summary should not render the header: %q", got)
	}
}

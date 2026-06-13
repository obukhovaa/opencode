package mattermost

import (
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

func TestBuildMultiSelectAttachmentAddsFooterWhenCustom(t *testing.T) {
	t.Parallel()
	att := buildMultiSelectAttachment("Pick capabilities", []bridge.QuestionChoice{
		{Label: "auth", Value: "auth", Custom: true},
		{Label: "billing", Value: "billing", Custom: true},
	})
	footer, _ := att["footer"].(string)
	if !strings.Contains(footer, "reply in this thread") {
		t.Errorf("expected discoverability footer, got %q", footer)
	}
	pretext, _ := att["pretext"].(string)
	if pretext != "Pick capabilities" {
		t.Errorf("pretext = %q; want prompt", pretext)
	}
}

func TestBuildMultiSelectAttachmentOmitsFooterWhenCustomFalse(t *testing.T) {
	t.Parallel()
	att := buildMultiSelectAttachment("Pick capabilities", []bridge.QuestionChoice{
		{Label: "auth", Value: "auth", Custom: false},
	})
	if _, ok := att["footer"]; ok {
		t.Errorf("custom=false MUST NOT add footer; got %+v", att)
	}
}

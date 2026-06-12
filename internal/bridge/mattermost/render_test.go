package mattermost

import (
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
)

func TestBuildToolCallAttachmentHasPretextAndFields(t *testing.T) {
	t.Parallel()
	att := buildToolCallAttachment(bridge.NewToolCallHint("bash", "abc", map[string]string{"command": "ls"}))
	pretext, _ := att["pretext"].(string)
	if !strings.Contains(pretext, "bash") || !strings.Contains(pretext, "abc") || !strings.Contains(pretext, "⏳") {
		t.Errorf("pretext missing parts: %v", pretext)
	}
	if att["color"] != "#0066cc" {
		t.Errorf("color = %v; want #0066cc", att["color"])
	}
	fields, ok := att["fields"].([]map[string]any)
	if !ok || len(fields) != 1 {
		t.Fatalf("fields = %+v; want 1 entry", att["fields"])
	}
	if fields[0]["title"] != "command" || fields[0]["value"] != "ls" {
		t.Errorf("field shape unexpected: %+v", fields[0])
	}
}

func TestBuildToolResultAttachmentErrorStateUsesDanger(t *testing.T) {
	t.Parallel()
	att := buildToolResultAttachment(bridge.NewToolResultHint("bash", "abc", "error", "permission denied", 1400))
	if att["color"] != "danger" {
		t.Errorf("error color = %v; want danger", att["color"])
	}
	pretext, _ := att["pretext"].(string)
	if !strings.Contains(pretext, "✗") || !strings.Contains(pretext, "1.4s") {
		t.Errorf("pretext = %v; want ✗ + duration", pretext)
	}
	body, _ := att["text"].(string)
	if !strings.Contains(body, "```") || !strings.Contains(body, "permission denied") {
		t.Errorf("body = %v; want code-fenced preview", body)
	}
}

func TestBuildToolResultAttachmentSuccessStateUsesGood(t *testing.T) {
	t.Parallel()
	att := buildToolResultAttachment(bridge.NewToolResultHint("read", "xyz", "ok", "12 lines", 850))
	if att["color"] != "good" {
		t.Errorf("ok color = %v; want good", att["color"])
	}
	pretext, _ := att["pretext"].(string)
	if !strings.Contains(pretext, "✓") {
		t.Errorf("pretext missing ✓: %v", pretext)
	}
}

func TestBuildListAttachmentRendersActiveMarker(t *testing.T) {
	t.Parallel()
	hint := bridge.NewListHint("Agents", []bridge.ListItem{
		{Label: "coder", Marker: "active"},
		{Label: "architect"},
	}, "active")
	att := buildListAttachment(hint)
	body, _ := att["text"].(string)
	if !strings.Contains(body, "coder") || !strings.Contains(body, "architect") {
		t.Errorf("body missing items: %v", body)
	}
	if !strings.Contains(body, "🟢") {
		t.Errorf("active marker should render green dot: %v", body)
	}
	pretext, _ := att["pretext"].(string)
	if !strings.Contains(pretext, "Agents") {
		t.Errorf("pretext missing title: %v", pretext)
	}
}

func TestBuildMarkdownTableShape(t *testing.T) {
	t.Parallel()
	hint := bridge.NewTableHint(
		[]string{"ID", "Title"},
		[][]string{{"s1", "First"}, {"s2", "Second"}},
	)
	out := buildMarkdownTable(hint)
	// Native markdown table: pipe-separated header + separator + rows.
	if !strings.Contains(out, "| ID | Title |") {
		t.Errorf("header row missing: %v", out)
	}
	if !strings.Contains(out, "| --- |") {
		t.Errorf("separator row missing: %v", out)
	}
	if !strings.Contains(out, "| s2 | Second |") {
		t.Errorf("data row missing: %v", out)
	}
}

func TestToolCardCacheTTLEvicts(t *testing.T) {
	t.Parallel()
	c := newToolCardCache()
	c.ttl = 10 * time.Millisecond
	c.store("C123", "abc", "post1")
	time.Sleep(15 * time.Millisecond)
	if _, ok := c.consume("C123", "abc"); ok {
		t.Error("expired entry should not be returned by consume")
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()
	cases := map[int64]string{
		0:      "0ms",
		500:    "500ms",
		1400:   "1.4s",
		60_000: "1m0s",
		62_500: "1m2s",
	}
	for ms, want := range cases {
		if got := formatDuration(ms); got != want {
			t.Errorf("formatDuration(%d) = %q; want %q", ms, got, want)
		}
	}
}

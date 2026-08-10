package service

import (
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// TestToolCallRender_Compact locks the compact tool-update contract for
// the pending line: glyph + tool name + pairing id, and NO params. Tool
// arguments must never reach the chat surface — they live in the session
// store (messages.parts) and Langfuse.
func TestToolCallRender_Compact(t *testing.T) {
	hint, fallback := toolCallRender("bash", "#a1b2c3", `{"command":"ls -la /etc"}`, false)

	if hint.Kind != bridge.RenderKindToolCall {
		t.Errorf("Kind = %v; want RenderKindToolCall", hint.Kind)
	}
	if hint.ToolName != "bash" || hint.CallID != "#a1b2c3" {
		t.Errorf("ToolName/CallID = %q/%q; want bash/#a1b2c3", hint.ToolName, hint.CallID)
	}
	if len(hint.Params) != 0 {
		t.Errorf("Params = %v; want empty (args must not reach chat)", hint.Params)
	}
	if hint.Preview != "" {
		t.Errorf("Preview = %q; want empty on a pending call", hint.Preview)
	}
	if fallback != "🔧 bash#a1b2c3" {
		t.Errorf("fallback = %q; want %q", fallback, "🔧 bash#a1b2c3")
	}
}

// TestToolResultRender_Compact covers the completion line across the
// success / failure / no-timing cases.
func TestToolResultRender_Compact(t *testing.T) {
	tests := []struct {
		name         string
		tool         string
		callID       string
		isError      bool
		content      string
		durationMs   int64
		wantStatus   string
		wantPreview  string
		wantFallback string
	}{
		{
			name:         "success drops the result body",
			tool:         "read",
			callID:       "#xyz",
			content:      "line one\nline two\nline three",
			durationMs:   850,
			wantStatus:   "ok",
			wantPreview:  "",
			wantFallback: "✓ read#xyz · 850ms",
		},
		{
			name:         "success without timing omits the duration",
			tool:         "read",
			callID:       "#xyz",
			content:      "12 lines",
			durationMs:   0,
			wantStatus:   "ok",
			wantPreview:  "",
			wantFallback: "✓ read#xyz",
		},
		{
			name:         "failure keeps a one-line reason",
			tool:         "bash",
			callID:       "#abc",
			isError:      true,
			content:      "permission denied\nexit 1",
			durationMs:   1400,
			wantStatus:   "error",
			wantPreview:  "permission denied exit 1",
			wantFallback: "✗ bash#abc · 1.4s · permission denied exit 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hint, fallback := toolResultRender(tt.tool, tt.callID, tt.isError, tt.content, tt.durationMs, false)

			if hint.Kind != bridge.RenderKindToolResult {
				t.Errorf("Kind = %v; want RenderKindToolResult", hint.Kind)
			}
			if hint.Status != tt.wantStatus {
				t.Errorf("Status = %q; want %q", hint.Status, tt.wantStatus)
			}
			if hint.Preview != tt.wantPreview {
				t.Errorf("Preview = %q; want %q", hint.Preview, tt.wantPreview)
			}
			if len(hint.Params) != 0 {
				t.Errorf("Params = %v; want empty", hint.Params)
			}
			if hint.DurationMs != tt.durationMs {
				t.Errorf("DurationMs = %d; want %d", hint.DurationMs, tt.durationMs)
			}
			if fallback != tt.wantFallback {
				t.Errorf("fallback = %q; want %q", fallback, tt.wantFallback)
			}
			if strings.Contains(fallback, "\n") {
				t.Errorf("fallback = %q; must stay on a single line", fallback)
			}
		})
	}
}

// TestToolResultRender_ErrorPreviewCapped guards the failure-reason cap:
// a runaway error body must not turn one chat line into a wall of text.
func TestToolResultRender_ErrorPreviewCapped(t *testing.T) {
	hint, _ := toolResultRender("bash", "#abc", true, strings.Repeat("x", toolErrorPreviewRunes*3), 0, false)

	// Cap + the ellipsis truncateRunes appends.
	if want := toolErrorPreviewRunes + 1; len([]rune(hint.Preview)) != want {
		t.Errorf("Preview runes = %d; want %d", len([]rune(hint.Preview)), want)
	}
	if !strings.HasSuffix(hint.Preview, "…") {
		t.Errorf("Preview = %q; want a truncation ellipsis", hint.Preview)
	}
}

func TestFormatDurationMs(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{0, "0ms"},
		{999, "999ms"},
		{1000, "1.0s"},
		{1449, "1.4s"},
		{59_999, "60.0s"},
		{62_000, "1m2s"},
		{3_600_000, "60m0s"},
	}
	for _, tt := range tests {
		if got := formatDurationMs(tt.ms); got != tt.want {
			t.Errorf("formatDurationMs(%d) = %q; want %q", tt.ms, got, tt.want)
		}
	}
}

// TestToolRender_FullVerbosity covers the opt-in detailed rendering:
// arguments on the call line, result body on completion. This is what
// `router.toolUpdateVerbosity: "full"` / `/verbosity full` restores.
func TestToolRender_FullVerbosity(t *testing.T) {
	hint, fallback := toolCallRender("bash", "#a1b2c3", `{"command":"ls -la /etc"}`, true)
	if hint.Params["command"] != "ls -la /etc" {
		t.Errorf("Params[command] = %q; want the command", hint.Params["command"])
	}
	if !strings.Contains(fallback, "ls -la /etc") {
		t.Errorf("fallback = %q; want the command inline", fallback)
	}

	resHint, resFallback := toolResultRender("read", "#xyz", false, "line one\nline two", 850, true)
	if resHint.Preview != "line one line two" {
		t.Errorf("Preview = %q; want the flattened body", resHint.Preview)
	}
	if !strings.Contains(resFallback, "line one line two") {
		t.Errorf("fallback = %q; want the body inline", resFallback)
	}

	// Full mode still caps the body so one call can't flood the thread.
	long, _ := toolResultRender("read", "#xyz", false, strings.Repeat("y", toolFullPreviewRunes*2), 0, true)
	if want := toolFullPreviewRunes + 1; len([]rune(long.Preview)) != want {
		t.Errorf("Preview runes = %d; want %d", len([]rune(long.Preview)), want)
	}
}

// TestServiceToolVerbosity_DefaultAndSwitch pins the runtime switch: a
// zero-valued Service reports compact, /verbosity full flips it, and an
// unknown mode is rejected without changing the live value.
func TestServiceToolVerbosity_DefaultAndSwitch(t *testing.T) {
	s := &Service{}
	if got := s.ToolVerbosity(); got != bridge.ToolUpdateVerbosityCompact {
		t.Errorf("zero Service ToolVerbosity() = %q; want compact", got)
	}
	mode, err := s.SetToolVerbosity("full")
	if err != nil || mode != bridge.ToolUpdateVerbosityFull {
		t.Fatalf("SetToolVerbosity(full) = (%q, %v); want (full, nil)", mode, err)
	}
	if got := s.ToolVerbosity(); got != bridge.ToolUpdateVerbosityFull {
		t.Errorf("ToolVerbosity() = %q; want full", got)
	}
	if _, err := s.SetToolVerbosity("chatty"); err == nil {
		t.Error("SetToolVerbosity(chatty) = nil error; want rejection")
	}
	if got := s.ToolVerbosity(); got != bridge.ToolUpdateVerbosityFull {
		t.Errorf("ToolVerbosity() after bad set = %q; want full (unchanged)", got)
	}
}

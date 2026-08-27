package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/langfuse"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

func TestTelemetryAgentID(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		fallback string
		want     string
	}{
		{
			name:     "context agent wins over the owning agent",
			ctx:      context.WithValue(context.Background(), tools.AgentIDContextKey, config.AgentName("descriptor")),
			fallback: "coder",
			want:     "descriptor",
		},
		{
			name:     "falls back to the owning agent",
			ctx:      context.Background(),
			fallback: "coder",
			want:     "coder",
		},
		{
			name:     "empty context value falls back",
			ctx:      context.WithValue(context.Background(), tools.AgentIDContextKey, config.AgentName("")),
			fallback: "coder",
			want:     "coder",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := telemetryAgentID(tt.ctx, tt.fallback); got != tt.want {
				t.Errorf("telemetryAgentID = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSetTraceOutputGate locks in that an opted-out agent never even builds the
// trace output value — the thunk must stay unevaluated, since on the main turn
// it walks the final message.
func TestSetTraceOutputGate(t *testing.T) {
	// An enabled client is one of the two guards; the other is the config
	// policy under test. Bogus keys/URL are fine — no root span is ever created
	// here, so nothing is queued for export.
	setLangfuseClient(t, true)

	tests := []struct {
		name      string
		gen       *config.GenerationTelemetryConfig
		agentID   string
		wantBuilt bool
	}{
		{
			name:    "unset config builds nothing",
			gen:     nil,
			agentID: "coder",
		},
		{
			name:    "disabled builds nothing",
			gen:     &config.GenerationTelemetryConfig{LogOutput: []string{"*"}},
			agentID: "coder",
		},
		{
			name:    "logInput alone does not open the output side",
			gen:     &config.GenerationTelemetryConfig{Enabled: true, LogInput: []string{"*"}},
			agentID: "coder",
		},
		{
			name:      "wildcard builds for every agent",
			gen:       &config.GenerationTelemetryConfig{Enabled: true, LogOutput: []string{"*"}},
			agentID:   "coder",
			wantBuilt: true,
		},
		{
			name:      "listed agent builds",
			gen:       &config.GenerationTelemetryConfig{Enabled: true, LogOutput: []string{"summarizer"}},
			agentID:   "summarizer",
			wantBuilt: true,
		},
		{
			name:    "unlisted agent builds nothing",
			gen:     &config.GenerationTelemetryConfig{Enabled: true, LogOutput: []string{"summarizer"}},
			agentID: "coder",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loadConfigIn(t, ".")
			config.Get().Telemetry = &config.TelemetryConfig{Generations: tt.gen}

			a := &agent{agentID: tt.agentID}
			built := false
			a.setTraceOutput(context.Background(), func() any {
				built = true
				return "output"
			})
			if built != tt.wantBuilt {
				t.Errorf("output built = %v, want %v", built, tt.wantBuilt)
			}
		})
	}
}

// loadConfigIn loads config from workDir with HOME/XDG pointed at empty temp
// dirs. config.Load reads $HOME/.opencode.json and $XDG_CONFIG_HOME/opencode as
// the *base* config and merges workDir on top — and a merge cannot un-set a
// key, so without this isolation a developer's global telemetry settings make
// the negative assertions here fail.
func loadConfigIn(t *testing.T, workDir string) {
	t.Helper()
	empty := t.TempDir()
	t.Setenv("HOME", empty)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(empty, "xdg"))
	config.Reset()
	if _, err := config.Load(workDir, false); err != nil {
		t.Fatalf("load config: %v", err)
	}
	t.Cleanup(config.Reset)
}

// With no Langfuse client there is no root span to receive the value, so the
// thunk must not run even when the config opts the agent in.
func TestSetTraceOutputSkippedWhenLangfuseDisabled(t *testing.T) {
	setLangfuseClient(t, false)
	loadConfigIn(t, ".")
	config.Get().Telemetry = &config.TelemetryConfig{
		Generations: &config.GenerationTelemetryConfig{Enabled: true, LogOutput: []string{"*"}},
	}

	a := &agent{agentID: "coder"}
	built := false
	a.setTraceOutput(context.Background(), func() any {
		built = true
		return "output"
	})
	if built {
		t.Error("thunk must not be evaluated when no exporter exists")
	}
}

// setLangfuseClient installs an enabled or disabled global Langfuse client and
// restores a disabled one afterwards. It clears LANGFUSE_* first: langfuse.New
// falls back to those env vars, so on a developer machine with real credentials
// exported, Init("", "", "") yields an *enabled* client and the disabled-path
// assertions silently invert.
func setLangfuseClient(t *testing.T, enabled bool) {
	t.Helper()
	t.Setenv("LANGFUSE_PUBLIC_KEY", "")
	t.Setenv("LANGFUSE_SECRET_KEY", "")
	t.Setenv("LANGFUSE_BASE_URL", "")
	t.Cleanup(func() { langfuse.Init("", "", "") })
	if enabled {
		langfuse.Init("pk", "sk", "http://127.0.0.1:1")
		return
	}
	langfuse.Init("", "", "")
}

func TestTraceInputFor(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		hasUserTurn bool
		attachments int
		want        string
	}{
		{
			name:        "plain text turn",
			content:     "fix the bug",
			hasUserTurn: true,
			want:        "fix the bug",
		},
		{
			// Regression: this used to be reported as an auto-resume because the
			// label was chosen on content alone, though a real user message IS
			// created for an attachment-only turn.
			name:        "attachment-only turn is still a user turn",
			content:     "",
			hasUserTurn: true,
			attachments: 2,
			want:        "[attachments only: 2 part(s)]",
		},
		{
			name:        "genuine auto-resume",
			content:     "",
			hasUserTurn: false,
			want:        "[auto-resume: reacting to a completed background task]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := traceInputFor(tt.content, tt.hasUserTurn, tt.attachments); got != tt.want {
				t.Errorf("traceInputFor = %q, want %q", got, tt.want)
			}
		})
	}
}

// Message.Content() returns only the first TextContent, so an answer that
// interleaves thinking and text was truncated in the trace output.
func TestTraceOutputFromEventConcatenatesTextBlocks(t *testing.T) {
	ev := AgentEvent{
		Message: message.Message{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningContent{Thinking: "hmm"},
				message.TextContent{Text: "part one"},
				message.ReasoningContent{Thinking: "more"},
				message.TextContent{Text: "part two"},
			},
		},
	}
	if got := traceOutputFromEvent(ev); got != "part one\npart two" {
		t.Errorf("traceOutputFromEvent = %q, want both text blocks", got)
	}
}

func TestTraceOutputFromEventPrefersErrorThenStructOutput(t *testing.T) {
	errEv := AgentEvent{Error: errors.New("boom"), StructOutput: &message.ToolResult{Content: "ignored"}}
	if got := traceOutputFromEvent(errEv); got != "error: boom" {
		t.Errorf("error event = %q", got)
	}
	structEv := AgentEvent{StructOutput: &message.ToolResult{Content: `{"ok":true}`}}
	if got := traceOutputFromEvent(structEv); got != `{"ok":true}` {
		t.Errorf("struct output event = %q", got)
	}
}

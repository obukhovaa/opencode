package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/langfuse"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildGenerationInput(t *testing.T) {
	msgs := []message.Message{
		{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: "fix the bug"}},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: "looking"},
				message.ToolCall{ID: "tc1", Name: "bash", Input: `{"command":"go test"}`},
			},
		},
		{
			Role: message.Tool,
			Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "tc1", Name: "bash", Content: "ok"},
				message.ToolResult{ToolCallID: "tc2", Name: "read", Content: "boom", IsError: true},
			},
		},
	}

	got := buildGenerationInput("you are a coder", msgs)

	require.Len(t, got, 5)
	assert.Equal(t, genChatMessage{Role: "system", Content: "you are a coder"}, got[0])
	assert.Equal(t, genChatMessage{Role: "user", Content: "fix the bug"}, got[1])
	assert.Equal(t, "assistant", got[2].Role)
	assert.Equal(t, "looking", got[2].Content)
	require.Len(t, got[2].ToolCalls, 1)
	assert.Equal(t, genToolCall{ID: "tc1", Name: "bash", Arguments: `{"command":"go test"}`}, got[2].ToolCalls[0])
	assert.Equal(t, genChatMessage{Role: "tool", ToolCallID: "tc1", Name: "bash", Content: "ok"}, got[3])
	assert.Equal(t, genChatMessage{Role: "tool", ToolCallID: "tc2", Name: "read", Content: "[error] boom"}, got[4])
}

func TestBuildGenerationInputNoSystem(t *testing.T) {
	got := buildGenerationInput("", []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}},
	})
	require.Len(t, got, 1)
	assert.Equal(t, "user", got[0].Role)
}

func TestBuildGenerationInputBinarySummarized(t *testing.T) {
	got := buildGenerationInput("", []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.TextContent{Text: "see attachment"},
				message.BinaryContent{MIMEType: "image/png", Data: make([]byte, 1024)},
			},
		},
	})
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Content, "see attachment")
	assert.Contains(t, got[0].Content, "[binary attachment: image/png, 1024 bytes]")
	assert.NotContains(t, got[0].Content, "AAAA") // no base64 payload
}

func TestBuildGenerationOutput(t *testing.T) {
	resp := &ProviderResponse{
		Content: "done",
		Reasoning: []message.ReasoningContent{
			{Thinking: "step one"},
			{Redacted: true, Data: "opaque"}, // redacted blocks are skipped
			{Thinking: "step two"},
		},
		ToolCalls:    []message.ToolCall{{ID: "tc1", Name: "edit", Input: `{"path":"a.go"}`}},
		FinishReason: message.FinishReasonEndTurn,
	}

	got := buildGenerationOutput(resp)

	require.NotNil(t, got)
	assert.Equal(t, "assistant", got.Role)
	assert.Equal(t, "done", got.Content)
	assert.Equal(t, "step one\nstep two", got.Reasoning)
	require.Len(t, got.ToolCalls, 1)
	assert.Equal(t, genToolCall{ID: "tc1", Name: "edit", Arguments: `{"path":"a.go"}`}, got.ToolCalls[0])
	assert.Equal(t, string(message.FinishReasonEndTurn), got.FinishReason)
}

func TestBuildGenerationOutputNil(t *testing.T) {
	assert.Nil(t, buildGenerationOutput(nil))
}

// TestGenerationInputGate locks in the privacy default: the request payload is
// rendered only for agents the operator opted in via telemetry.generations.
func TestGenerationInputGate(t *testing.T) {
	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "secret prompt"}}},
	}

	tests := []struct {
		name    string
		gen     *config.GenerationTelemetryConfig
		agentID string
		want    bool // want a payload
	}{
		{
			name:    "unset config renders nothing",
			gen:     nil,
			agentID: "coder",
		},
		{
			name:    "enabled but no patterns renders nothing",
			gen:     &config.GenerationTelemetryConfig{Enabled: true},
			agentID: "coder",
		},
		{
			name:    "patterns without enabled render nothing",
			gen:     &config.GenerationTelemetryConfig{LogInput: []string{"*"}},
			agentID: "coder",
		},
		{
			name:    "wildcard renders for every agent",
			gen:     &config.GenerationTelemetryConfig{Enabled: true, LogInput: []string{"*"}},
			agentID: "coder",
			want:    true,
		},
		{
			name:    "listed agent renders",
			gen:     &config.GenerationTelemetryConfig{Enabled: true, LogInput: []string{"workhorse"}},
			agentID: "workhorse",
			want:    true,
		},
		{
			name:    "unlisted agent renders nothing",
			gen:     &config.GenerationTelemetryConfig{Enabled: true, LogInput: []string{"workhorse"}},
			agentID: "coder",
		},
		{
			name:    "logOutput alone does not open the input side",
			gen:     &config.GenerationTelemetryConfig{Enabled: true, LogOutput: []string{"*"}},
			agentID: "coder",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loadConfigIn(t, ".")
			config.Get().Telemetry = &config.TelemetryConfig{Generations: tt.gen}

			got := generationInput(tt.agentID, "you are a coder", msgs)
			if !tt.want {
				// Must be a genuinely nil interface — a typed nil would still
				// be recorded by GenerationStart's `Input != nil` check.
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			rendered, ok := got.([]genChatMessage)
			require.True(t, ok, "expected chat-format payload, got %T", got)
			require.Len(t, rendered, 2)
			assert.Equal(t, "system", rendered[0].Role)
			assert.Equal(t, "secret prompt", rendered[1].Content)
		})
	}
}

// TestGenerationInputGateFromLoadedConfig walks the whole chain the operator
// actually uses: telemetry.generations in a .opencode.json on disk, through the
// viper loader, to the capture decision. The unit tests above set the struct
// directly, so only this one would catch a loader-level regression (viper key
// folding, a renamed json tag) that silently leaves capture off.
func TestGenerationInputGateFromLoadedConfig(t *testing.T) {
	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "secret prompt"}}},
	}

	tests := []struct {
		name          string
		body          string
		agentID       string
		wantInput     bool
		wantOutputFor string // agent ID expected to be allowed on the output side
	}{
		{
			name:      "langfuse on, generations absent: capture stays off",
			body:      `{"telemetry":{"langfuse":{"enabled":true}}}`,
			agentID:   "coder",
			wantInput: false,
		},
		{
			name:      "explicitly disabled with wildcards: capture stays off",
			body:      `{"telemetry":{"generations":{"enabled":false,"logInput":["*"],"logOutput":["*"]}}}`,
			agentID:   "coder",
			wantInput: false,
		},
		{
			name:          "per-agent opt-in reaches the gate",
			body:          `{"telemetry":{"generations":{"enabled":true,"logInput":["workhorse"],"logOutput":["summarizer"]}}}`,
			agentID:       "workhorse",
			wantInput:     true,
			wantOutputFor: "summarizer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(tt.body), 0o644))

			loadConfigIn(t, dir)

			got := generationInput(tt.agentID, "sys", msgs)
			if tt.wantInput {
				require.NotNil(t, got, "loader dropped the opt-in")
			} else {
				assert.Nil(t, got)
			}

			if tt.wantOutputFor != "" {
				assert.True(t, langfuse.ShouldLogGenerationOutput(tt.wantOutputFor),
					"output side should be open for %q", tt.wantOutputFor)
				assert.False(t, langfuse.ShouldLogGenerationOutput(tt.agentID),
					"output side must stay closed for %q — it is only on logInput", tt.agentID)
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

// Reasoning and tool-search blocks ARE part of the outgoing Anthropic-dialect
// request (anthropic.go replays msg.ReasoningParts() and msg.ToolSearchParts()),
// so a payload that omits them is not the request that was sent.
func TestBuildGenerationInputRendersReasoningAndToolSearch(t *testing.T) {
	got := buildGenerationInput("", []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ReasoningContent{Thinking: "first thought", Signature: "sig"},
				message.ToolSearchContent{
					ToolUseID:  "srv1",
					Name:       "tool_search_tool_regex",
					Input:      `{"query":"jira"}`,
					References: []string{"jira_create_issue"},
				},
				message.ReasoningContent{Redacted: true, Data: "OPAQUE_PROVIDER_BLOB"},
				message.TextContent{Text: "here goes"},
			},
		},
	})

	require.Len(t, got, 1)
	assert.Equal(t, "here goes", got[0].Content)
	assert.Equal(t, "first thought\n[redacted thinking block]", got[0].Reasoning)
	assert.NotContains(t, got[0].Reasoning, "OPAQUE_PROVIDER_BLOB", "redacted payload must never be embedded")
	require.Len(t, got[0].ToolSearches, 1)
	assert.Equal(t, genToolSearch{
		ToolUseID:  "srv1",
		Name:       "tool_search_tool_regex",
		Input:      `{"query":"jira"}`,
		References: []string{"jira_create_issue"},
	}, got[0].ToolSearches[0])
}

// A thinking-only assistant turn must not render as an empty message.
func TestBuildGenerationInputThinkingOnlyTurn(t *testing.T) {
	got := buildGenerationInput("", []message.Message{
		{Role: message.Assistant, Parts: []message.ContentPart{message.ReasoningContent{Thinking: "just thinking"}}},
	})
	require.Len(t, got, 1)
	assert.Equal(t, "just thinking", got[0].Reasoning)
}

// An image or binary part opening a message must not produce a leading newline.
func TestBuildGenerationInputNoLeadingNewline(t *testing.T) {
	got := buildGenerationInput("", []message.Message{
		{
			Role: message.User,
			Parts: []message.ContentPart{
				message.BinaryContent{MIMEType: "image/png", Data: make([]byte, 8)},
				message.TextContent{Text: "what is this"},
			},
		},
	})
	require.Len(t, got, 1)
	assert.Equal(t, "[binary attachment: image/png, 8 bytes]\nwhat is this", got[0].Content)
}

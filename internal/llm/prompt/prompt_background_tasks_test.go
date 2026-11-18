package prompt

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
)

const noPollHeader = "# Background tasks (event-driven, no polling)"

// TestGetAgentPrompt_CustomPromptAgentGetsNoPollContract pins the CD-4761
// prompt-delivery fix: an agent with a custom info.Prompt skips CoderPrompt
// entirely, so the background-tasks no-poll contract MUST be appended
// independently of the base prompt.
func TestGetAgentPrompt_CustomPromptAgentGetsNoPollContract(t *testing.T) {
	tmpDir := t.TempDir()
	config.Reset()
	_, err := config.Load(tmpDir, false)
	require.NoError(t, err)
	cfg := config.Get()
	cfg.Agents["custom-flow-dev"] = config.Agent{
		Prompt: "You are a fully custom flow development agent.",
	}
	agentregistry.InvalidateRegistry()
	t.Cleanup(func() {
		config.Reset()
		agentregistry.InvalidateRegistry()
	})

	got := GetAgentPrompt("custom-flow-dev", models.ProviderAnthropic)

	assert.True(t, strings.HasPrefix(got, "You are a fully custom flow development agent."),
		"custom prompt must remain the base")
	assert.NotContains(t, got, "You are OpenCode",
		"CoderPrompt must NOT be appended for a custom-prompt agent")
	assert.Contains(t, got, noPollHeader,
		"no-poll contract must reach custom-prompt agents")
	assert.Contains(t, got, "DO NOT use `sleep N`")
}

// TestGetAgentPrompt_BuiltinAgentsNoPollDelivery: coder keeps the contract
// (now appended rather than inlined — exactly once), while tool-less agents
// (summarizer/descriptor) are exempt.
func TestGetAgentPrompt_BuiltinAgentsNoPollDelivery(t *testing.T) {
	tmpDir := t.TempDir()
	config.Reset()
	_, err := config.Load(tmpDir, false)
	require.NoError(t, err)
	agentregistry.InvalidateRegistry()
	t.Cleanup(func() {
		config.Reset()
		agentregistry.InvalidateRegistry()
	})

	t.Run("coder gets it exactly once", func(t *testing.T) {
		got := GetAgentPrompt(config.AgentCoder, models.ProviderAnthropic)
		assert.Equal(t, 1, strings.Count(got, noPollHeader),
			"coder prompt must contain the no-poll section exactly once")
	})

	t.Run("workhorse subagent gets it", func(t *testing.T) {
		got := GetAgentPrompt(config.AgentWorkhorse, models.ProviderAnthropic)
		assert.Contains(t, got, noPollHeader,
			"subagents with bash access need the contract too")
	})

	t.Run("tool-less summarizer is exempt", func(t *testing.T) {
		got := GetAgentPrompt(config.AgentSummarizer, models.ProviderAnthropic)
		assert.NotContains(t, got, noPollHeader,
			"summarizer has Tools{*:false} — guidance would be dead weight")
	})

	t.Run("tool-less descriptor is exempt", func(t *testing.T) {
		got := GetAgentPrompt(config.AgentDescriptor, models.ProviderAnthropic)
		assert.NotContains(t, got, noPollHeader)
	})
}

func TestGetAgentPrompt_BackgroundGuidanceFollowsAvailableTools(t *testing.T) {
	for _, tt := range []struct {
		name    string
		tools   []string
		present []string
		absent  []string
	}{
		{
			name:    "read only",
			tools:   []string{"read"},
			present: []string{"normal end_turn", "No success claim", "repeated output-file reads"},
			absent:  []string{"`bash`", "`task`", "`monitor`", "`tasklist`", "`taskstop`", "`yield`"},
		},
		{
			name:    "shell without yield",
			tools:   []string{"bash", "tasklist", "taskstop"},
			present: []string{"run-to-completion", "`run_in_background: true`", "one-shot inventory", "`taskstop`", "normal end_turn"},
			absent:  []string{"`yield`", "`monitor`", "`async: true`", "converts a foreground"},
		},
		{
			name:    "monitor and yield",
			tools:   []string{"monitor", "yield", "struct_output"},
			present: []string{"call `yield` alone", "no other tool calls", "suspends generation until a report arrives", "No success claim or `struct_output` is required", "remains a fallback", "shell `command`", "Legacy `cmd`/`args`", "omitting it matches all nonempty lines", "Silence alone does not imply failure"},
			absent:  []string{"`bash`", "`tasklist`", "`taskstop`", "`async: true`"},
		},
		{
			name:    "async delegation",
			tools:   []string{"task", "yield"},
			present: []string{"`async: true`", "non-conflicting writes", "Do not duplicate work", "call `yield` alone"},
			absent:  []string{"`monitor`", "`bash`"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			loadAgents(t, map[config.AgentName]config.Agent{
				"waiting-agent": {
					Prompt:     "Custom agent that persists until completion.",
					AllowTools: tt.tools,
					Output: &config.AgentOutput{Schema: map[string]any{
						"type": "object", "properties": map[string]any{"result": map[string]any{"type": "string"}},
					}},
				},
			}, nil)
			got := GetAgentPrompt("waiting-agent", models.ProviderAnthropic)
			assert.True(t, strings.HasPrefix(got, "Custom agent that persists until completion."))
			assert.Equal(t, 1, strings.Count(got, noPollHeader))
			assert.Contains(t, got, "normal end_turn waits for the next report, not for all background tasks to finish")
			assert.Contains(t, got, "Reports are also refreshed after tool batches")
			for _, want := range tt.present {
				assert.Contains(t, got, want)
			}
			for _, unwanted := range tt.absent {
				assert.NotContains(t, got, unwanted)
			}
			if tt.name == "monitor and yield" {
				assert.Contains(t, got, "until a report arrives in all modes")
				assert.Contains(t, got, "Waiting for asynchronous work is not a final response")
			}
		})
	}
}

func TestBuiltinPrompts_PersistenceAndReporting(t *testing.T) {
	for _, tt := range []struct {
		name   string
		prompt string
	}{
		{"coder", CoderPrompt()},
		{"workhorse", WorkhorsePrompt(models.ProviderAnthropic)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Contains(t, tt.prompt, "follow the background-task waiting instructions")
			assert.Contains(t, tt.prompt, "waiting does not require a success claim or a final result")
			assert.Contains(t, tt.prompt, "already explicitly authorized within")
			assert.Contains(t, tt.prompt, "verification results, and unresolved blockers")
			assert.NotContains(t, tt.prompt, "no unsolicited code explanations or summaries")
		})
	}
}

func TestGetAgentPrompt_ParallelGuidanceSeparatesDependencies(t *testing.T) {
	loadAgents(t, map[config.AgentName]config.Agent{
		"reader": {Prompt: "Read only.", AllowTools: []string{"read", "grep"}},
	}, nil)
	got := GetAgentPrompt("reader", models.ProviderAnthropic)
	assert.Contains(t, got, "Run dependent calls in separate responses after inspecting the earlier result")
	assert.Contains(t, got, "never guess IDs, paths, or arguments")
	assert.Contains(t, got, "Do not batch operations that edit the same files or otherwise conflict")
}

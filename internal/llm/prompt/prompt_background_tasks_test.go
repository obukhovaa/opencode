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

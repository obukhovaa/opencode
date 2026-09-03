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

// TestGetAgentPromptWithOptions_BasePrompt pins the plumbing a
// Langfuse-managed agent prompt depends on.
//
// The registry entry for such an agent holds only the reference — its
// Prompt is empty — and this builder re-fetches the registry by name rather
// than reading the per-call AgentInfo copy the factory resolved onto. Without
// BasePrompt the agent would silently fall back to the built-in prompt for
// its name, which is a failure that looks like "Langfuse is being ignored"
// rather than like an error.
func TestGetAgentPromptWithOptions_BasePrompt(t *testing.T) {
	tmpDir := t.TempDir()
	config.Reset()
	_, err := config.Load(tmpDir, false)
	require.NoError(t, err)
	cfg := config.Get()
	// A reference-declaring agent, exactly as the registry holds it: no
	// inline prompt anywhere.
	cfg.Agents["managed-agent"] = config.Agent{
		LangfusePromptPath: "agents/managed/system",
	}
	agentregistry.InvalidateRegistry()
	t.Cleanup(func() {
		config.Reset()
		agentregistry.InvalidateRegistry()
	})

	t.Run("the resolved prompt becomes the base", func(t *testing.T) {
		got := GetAgentPromptWithOptions("managed-agent", models.ProviderAnthropic, AgentPromptOptions{
			BasePrompt: "You are the managed agent.",
		})
		assert.True(t, strings.HasPrefix(got, "You are the managed agent."),
			"the resolved prompt must be the base, got: %.80s", got)
	})

	t.Run("without it the agent falls back to a built-in prompt", func(t *testing.T) {
		got := GetAgentPromptWithOptions("managed-agent", models.ProviderAnthropic, AgentPromptOptions{})
		assert.NotContains(t, got, "You are the managed agent.",
			"the registry entry carries no prompt, so nothing should supply this text")
	})

	t.Run("an empty BasePrompt leaves registered prompts alone", func(t *testing.T) {
		cfg.Agents["inline-agent"] = config.Agent{Prompt: "You are the inline agent."}
		agentregistry.InvalidateRegistry()

		got := GetAgentPromptWithOptions("inline-agent", models.ProviderAnthropic, AgentPromptOptions{})
		assert.True(t, strings.HasPrefix(got, "You are the inline agent."),
			"an unset BasePrompt must not disturb the registered prompt, got: %.80s", got)
	})
}

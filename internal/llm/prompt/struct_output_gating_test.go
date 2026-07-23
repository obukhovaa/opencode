package prompt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
)

// structOutputSentinel is a stable fragment of structuredOutputPrompt — the
// terse "you MUST call struct_output" instruction the flow relies on to make
// an agent emit structured output as its final action.
const structOutputSentinel = "You MUST use the struct_output tool"

// TestStructuredOutputPromptGating pins the fix for the silent-strand bug: a
// NON-interactive flow step that declares an `output.schema`, but runs on an
// agent type WITHOUT a static schema (the piano-developer / coder shape),
// must still receive the struct_output instruction in its system prompt.
//
// The per-step schema is injected onto the factory's per-call AgentInfo copy,
// which the prompt builder never sees (it re-fetches the ORIGINAL registry
// entry via reg.Get), so its presence is plumbed through
// AgentPromptOptions.HasOutputSchema — exactly like Interactive.
func TestStructuredOutputPromptGating(t *testing.T) {
	tmpDir := t.TempDir()
	config.Reset()
	_, err := config.Load(tmpDir, false)
	require.NoError(t, err)
	cfg := config.Get()
	// A schemaless general agent — the piano-developer / coder shape: a
	// custom prompt, no static output schema, default (allow) tools so the
	// struct_output tool is enabled.
	cfg.Agents["flow-dev"] = config.Agent{
		Prompt: "You are a flow development agent.",
	}
	// An agent that DOES declare a static output schema in its own definition.
	cfg.Agents["static-schema-dev"] = config.Agent{
		Prompt: "You are a static-schema agent.",
		Output: &config.AgentOutput{Schema: map[string]any{"type": "object"}},
	}
	agentregistry.InvalidateRegistry()
	t.Cleanup(func() {
		config.Reset()
		agentregistry.InvalidateRegistry()
	})

	t.Run("schemaless agent, no per-step schema → no struct_output instruction", func(t *testing.T) {
		got := GetAgentPromptWithOptions("flow-dev", models.ProviderAnthropic, AgentPromptOptions{})
		assert.NotContains(t, got, structOutputSentinel,
			"an agent with neither a static nor a per-step schema must not get the struct_output prompt")
	})

	t.Run("schemaless agent + per-step schema → struct_output instruction (the fix)", func(t *testing.T) {
		got := GetAgentPromptWithOptions("flow-dev", models.ProviderAnthropic, AgentPromptOptions{HasOutputSchema: true})
		assert.Contains(t, got, structOutputSentinel,
			"a flow step declaring output.schema must arm struct_output even when the agent type has no static schema")
	})

	t.Run("static-schema agent → struct_output instruction without opts (backward compat)", func(t *testing.T) {
		got := GetAgentPromptWithOptions("static-schema-dev", models.ProviderAnthropic, AgentPromptOptions{})
		assert.Contains(t, got, structOutputSentinel,
			"an agent with a STATIC output schema must keep getting the struct_output prompt")
	})

	t.Run("interactive step wins over the terse struct_output prompt", func(t *testing.T) {
		got := GetAgentPromptWithOptions("flow-dev", models.ProviderAnthropic, AgentPromptOptions{
			HasOutputSchema: true,
			Interactive:     true,
		})
		assert.Contains(t, got, "INTERACTIVE FLOW STEP",
			"interactive steps must get the multi-turn variant")
		assert.NotContains(t, got, structOutputSentinel,
			"interactive steps must NOT get the terse first-turn struct_output prompt")
	})
}

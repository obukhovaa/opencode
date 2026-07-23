package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/prompt"
)

// structOutputInstruction is a stable fragment of prompt.structuredOutputPrompt.
const structOutputInstruction = "You MUST use the struct_output tool"

// testFlowYAML is a minimal flow whose first step declares an output.schema
// (like plan-to-implement) and whose second step does not.
const testFlowYAML = `
steps:
  - id: plan
    agent: flow-dev
    prompt: Produce a plan.
    output:
      schema:
        type: object
        properties:
          blockers:
            type: array
            items: {type: string}
          plan:
            type: array
            items: {type: string}
        required: [plan, blockers]
  - id: chat
    agent: flow-dev
    prompt: Just talk, no structured result.
`

// TestFlowStepWithOutputSchemaGetsStructOutputPrompt is an end-to-end check
// that a flow step declaring an output schema yields an agent system prompt
// containing the struct_output instruction — the regression that silently
// stranded plan-to-implement (the agent replied in prose, never called
// struct_output, so the blockers-based routing rules matched nothing and the
// flow stopped).
//
// It parses a real flow definition, then runs the SAME schema-presence
// computation the agent factory does — AgentFactory.NewAgent sets
// infoCopy.Output from step.Output.Schema, and newAgent passes
// withHasOutputSchema(info.Output != nil && info.Output.Schema != nil) into
// the prompt builder — against a schemaless agent type (the piano-developer
// shape: a custom prompt, no static output schema).
func TestFlowStepWithOutputSchemaGetsStructOutputPrompt(t *testing.T) {
	var spec FlowSpec
	require.NoError(t, yaml.Unmarshal([]byte(testFlowYAML), &spec))
	require.Len(t, spec.Steps, 2)

	planStep := spec.Steps[0]
	chatStep := spec.Steps[1]
	require.Equal(t, "plan", planStep.ID)
	require.NotNil(t, planStep.Output, "the plan step must parse an output schema")
	require.NotNil(t, planStep.Output.Schema)
	require.Nil(t, chatStep.Output, "the chat step must have no output schema")

	// Register "flow-dev" as a schemaless general agent (no static output
	// schema) in the global registry the prompt builder reads.
	tmpDir := t.TempDir()
	config.Reset()
	_, err := config.Load(tmpDir, false)
	require.NoError(t, err)
	config.Get().Agents["flow-dev"] = config.Agent{
		Prompt: "You are a flow development agent.",
	}
	agentregistry.InvalidateRegistry()
	t.Cleanup(func() {
		config.Reset()
		agentregistry.InvalidateRegistry()
	})

	// stepHasOutputSchema mirrors the boolean the factory computes from the
	// per-call AgentInfo (AgentFactory.NewAgent → newAgent).
	stepHasOutputSchema := func(s Step) bool {
		return s.Output != nil && s.Output.Schema != nil
	}

	planPrompt := prompt.GetAgentPromptWithOptions("flow-dev", models.ProviderAnthropic, prompt.AgentPromptOptions{
		HasOutputSchema: stepHasOutputSchema(planStep),
	})
	assert.Contains(t, planPrompt, structOutputInstruction,
		"a flow step with output.schema must instruct the agent to call struct_output")

	chatPrompt := prompt.GetAgentPromptWithOptions("flow-dev", models.ProviderAnthropic, prompt.AgentPromptOptions{
		HasOutputSchema: stepHasOutputSchema(chatStep),
	})
	assert.NotContains(t, chatPrompt, structOutputInstruction,
		"a step without an output schema must not get the struct_output prompt")
}

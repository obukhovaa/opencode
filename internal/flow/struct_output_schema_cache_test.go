package flow

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// twoStepFlowYAML is the shape that produced the reported symptom: consecutive
// steps, one agent, different output schemas, the second forking the first's
// session so its whole history is re-sent.
const twoStepFlowYAML = `
steps:
  - id: plan
    agent: flow-dev
    prompt: Produce a plan.
    output:
      schema:
        type: object
        properties:
          plan:
            type: array
            items: {type: string}
          blockers:
            type: array
            items: {type: string}
        required: [plan, blockers]
  - id: implement
    agent: flow-dev
    prompt: Implement the plan.
    session:
      fork: true
    output:
      schema:
        type: object
        properties:
          merge_request:
            type: string
          summary:
            type: string
        required: [merge_request, summary]
`

func parseTwoStepFlow(t *testing.T) (planSchema, implSchema map[string]any) {
	t.Helper()
	var spec FlowSpec
	require.NoError(t, yaml.Unmarshal([]byte(twoStepFlowYAML), &spec))
	require.Len(t, spec.Steps, 2)
	require.NotNil(t, spec.Steps[0].Output)
	require.NotNil(t, spec.Steps[1].Output)
	require.True(t, spec.Steps[1].Session.Fork, "the forking step is the expensive case")
	return spec.Steps[0].Output.Schema, spec.Steps[1].Output.Schema
}

// The end-to-end property, from flow YAML to the bytes the provider caches: two
// steps of one agent whose only difference is output.schema must produce an
// identical struct_output tool definition. It sits at position 0 of the
// Anthropic prefix, ahead of the system prompt and the entire forked history,
// so a difference here costs the whole request — which is exactly the reported
// "first LLM call of each flow step is a cache miss".
func TestFlowStepSchemasDoNotPerturbTheToolBlock(t *testing.T) {
	planSchema, implSchema := parseTwoStepFlow(t)
	require.NotEqual(t, planSchema, implSchema, "the two steps must actually differ")

	plan := tools.NewStructOutputToolWithDelivery(planSchema, tools.SchemaDeliveryMessage).Info()
	impl := tools.NewStructOutputToolWithDelivery(implSchema, tools.SchemaDeliveryMessage).Info()

	planJSON, err := json.Marshal(plan)
	require.NoError(t, err)
	implJSON, err := json.Marshal(impl)
	require.NoError(t, err)

	assert.Equal(t, string(planJSON), string(implJSON),
		"consecutive flow steps must ship a byte-identical struct_output definition")
	for _, leaked := range []string{"merge_request", "blockers"} {
		assert.NotContains(t, string(planJSON), leaked)
		assert.NotContains(t, string(implJSON), leaked)
	}
}

// …and each step still gets its own schema, in the tail, distinguishable from
// the other's. Identical tool blocks would be worthless if the model could no
// longer tell which document it is being asked for.
func TestFlowStepSchemasProduceDistinctEnvelopes(t *testing.T) {
	planSchema, implSchema := parseTwoStepFlow(t)

	planEnv := tools.RenderSchemaEnvelope(planSchema)
	implEnv := tools.RenderSchemaEnvelope(implSchema)

	assert.NotEqual(t, planEnv, implEnv)
	assert.Contains(t, planEnv, `"blockers"`)
	assert.Contains(t, implEnv, `"merge_request"`)
	assert.NotContains(t, planEnv, "merge_request")
	assert.NotContains(t, implEnv, "blockers")

	assert.NotEqual(t, tools.SchemaFingerprint(planSchema), tools.SchemaFingerprint(implSchema),
		"distinct fingerprints are what make the forked step re-inject instead of inheriting the plan schema")
}

// Both steps' documents must survive the round trip through the tool that the
// model actually calls — the non-regression gate, at the flow layer.
func TestFlowStepOutputsRoundTripThroughStructOutput(t *testing.T) {
	planSchema, implSchema := parseTwoStepFlow(t)

	for name, tc := range map[string]struct {
		schema map[string]any
		input  string
		want   map[string]any
	}{
		"plan step": {
			schema: planSchema,
			input:  `{"output":{"plan":["step one"],"blockers":[]}}`,
			want:   map[string]any{"plan": []any{"step one"}, "blockers": []any{}},
		},
		"implement step": {
			schema: implSchema,
			input:  `{"output":{"merge_request":"!42","summary":"done"}}`,
			want:   map[string]any{"merge_request": "!42", "summary": "done"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			tool := tools.NewStructOutputToolWithDelivery(tc.schema, tools.SchemaDeliveryMessage)
			resp, err := tool.Run(t.Context(), tools.ToolCall{Input: tc.input})
			require.NoError(t, err)
			require.False(t, resp.IsError, resp.Content)

			var got map[string]any
			require.NoError(t, json.Unmarshal([]byte(resp.Content), &got))
			assert.Equal(t, tc.want, got,
				"the step output downstream routing reads must be the bare document")
		})
	}
}

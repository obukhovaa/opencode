package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func planSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary":  map[string]any{"type": "string"},
			"blockers": map[string]any{"type": "array", "default": []any{}},
		},
		"required": []any{"summary", "blockers"},
	}
}

func reviewSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verdict":  map[string]any{"type": "string"},
			"comments": map[string]any{"type": "array"},
		},
		"required": []any{"verdict"},
	}
}

func runDeliveryTool(t *testing.T, tool BaseTool, input string) ToolResponse {
	t.Helper()
	resp, err := tool.Run(context.Background(), ToolCall{Input: input})
	require.NoError(t, err)
	return resp
}

// The whole point of message delivery: the serialized tool definition is what
// sits at position 0 of the provider's cached prefix, so two steps whose only
// difference is the output schema MUST produce the same ToolInfo. Any drift
// here is a cold prefix — tools, system prompt, and the entire (possibly
// forked) message history re-processed at full input price.
func TestStructOutputToolInfoIsInvariantAcrossSchemas(t *testing.T) {
	plan := NewStructOutputToolWithDelivery(planSchema(), SchemaDeliveryMessage).Info()
	review := NewStructOutputToolWithDelivery(reviewSchema(), SchemaDeliveryMessage).Info()

	assert.Equal(t, plan, review, "tool definition must not vary with the step's schema")

	planJSON, err := json.Marshal(plan)
	require.NoError(t, err)
	reviewJSON, err := json.Marshal(review)
	require.NoError(t, err)
	assert.Equal(t, string(planJSON), string(reviewJSON),
		"serialized tool block must be byte-identical across schemas")

	// And nothing schema-derived leaked into it.
	for _, needle := range []string{"summary", "blockers", "verdict", "comments"} {
		assert.NotContains(t, string(planJSON), needle,
			"no property name from the step schema may reach the tool definition")
		assert.NotContains(t, string(reviewJSON), needle,
			"no property name from the step schema may reach the tool definition")
	}
}

func TestStructOutputMessageDeliveryDeclaresOutputObject(t *testing.T) {
	info := NewStructOutputToolWithDelivery(planSchema(), SchemaDeliveryMessage).Info()

	assert.Equal(t, []string{structOutputWrapperKey}, info.Required)
	param, ok := info.Parameters[structOutputWrapperKey].(map[string]any)
	require.True(t, ok, "the single declared parameter must be %q", structOutputWrapperKey)
	assert.Equal(t, "object", param["type"])
	assert.Contains(t, info.Description, SchemaEnvelopeOpenTag,
		"the description must point the model at the envelope carrying the real schema")
}

// The escape hatch has to be a true revert, not an approximation: a deployment
// that hits a regression sets structOutputSchemaDelivery=tool and gets exactly
// the pre-change surface back.
func TestStructOutputToolDeliveryPreservesLegacySurface(t *testing.T) {
	info := NewStructOutputToolWithDelivery(planSchema(), SchemaDeliveryTool).Info()

	assert.Equal(t, []string{"summary", "blockers"}, info.Required)
	assert.Contains(t, info.Parameters, "summary")
	assert.Contains(t, info.Parameters, "blockers")
	assert.NotContains(t, info.Parameters, structOutputWrapperKey)
	assert.Equal(t, NewStructOutputTool(planSchema()).Info(), info,
		"the legacy constructor must stay the tool-delivery surface")
}

func TestStructOutputUnwrapsOutputArgument(t *testing.T) {
	tool := NewStructOutputToolWithDelivery(planSchema(), SchemaDeliveryMessage)

	resp := runDeliveryTool(t, tool, `{"output":{"summary":"done","blockers":["db"]}}`)
	require.False(t, resp.IsError, resp.Content)

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &doc))
	assert.Equal(t, "done", doc["summary"])
	assert.NotContains(t, doc, structOutputWrapperKey,
		"the wrapper is a transport detail and must never reach downstream consumers")
}

// The model is shown the DOCUMENT's schema, not the wrapper's, so it sometimes
// emits the document flat. Rejecting a well-formed answer over our own
// transport detail would be the change causing the very regression it exists to
// avoid.
func TestStructOutputAcceptsFlatPayload(t *testing.T) {
	tool := NewStructOutputToolWithDelivery(planSchema(), SchemaDeliveryMessage)

	resp := runDeliveryTool(t, tool, `{"summary":"done","blockers":[]}`)
	require.False(t, resp.IsError, resp.Content)

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &doc))
	assert.Equal(t, "done", doc["summary"])
}

func TestStructOutputRejectsUnrecognizablePayload(t *testing.T) {
	tool := NewStructOutputToolWithDelivery(planSchema(), SchemaDeliveryMessage)

	for name, input := range map[string]string{
		"empty":             `{}`,
		"unknown keys":      `{"nonsense":1}`,
		"non-object output": `{"output":"a string"}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp := runDeliveryTool(t, tool, input)
			assert.True(t, resp.IsError, "must be retryable, not silently accepted")
			assert.Contains(t, resp.Content, structOutputWrapperKey,
				"the error must name the shape the model should retry with")
		})
	}
}

// Validation is a property of the tool, not of how the model was shown the
// schema. Message delivery must enforce defaults and required fields exactly as
// tool delivery does — this is the safety net the whole design leans on.
func TestStructOutputValidationIsDeliveryIndependent(t *testing.T) {
	for _, delivery := range []SchemaDelivery{SchemaDeliveryMessage, SchemaDeliveryTool} {
		t.Run(string(delivery), func(t *testing.T) {
			tool := NewStructOutputToolWithDelivery(planSchema(), delivery)

			input := `{"summary":"done"}`
			if delivery == SchemaDeliveryMessage {
				input = `{"output":{"summary":"done"}}`
			}
			resp := runDeliveryTool(t, tool, input)
			require.False(t, resp.IsError, resp.Content)

			var doc map[string]any
			require.NoError(t, json.Unmarshal([]byte(resp.Content), &doc))
			assert.Equal(t, []any{}, doc["blockers"],
				"a declared default must be materialized so flow routing sees the key")

			missingInput := `{"blockers":[]}`
			if delivery == SchemaDeliveryMessage {
				missingInput = `{"output":{"blockers":[]}}`
			}
			missing := runDeliveryTool(t, tool, missingInput)
			assert.True(t, missing.IsError)
			assert.Contains(t, missing.Content, "summary")
		})
	}
}

func TestSchemaFingerprintIsStableAndDiscriminating(t *testing.T) {
	a := SchemaFingerprint(planSchema())

	assert.Len(t, a, 12)
	assert.Equal(t, a, SchemaFingerprint(planSchema()), "same schema, same fingerprint")
	assert.NotEqual(t, a, SchemaFingerprint(reviewSchema()), "different schema, different fingerprint")
}

func TestRenderSchemaEnvelopeCarriesSchemaAndFingerprint(t *testing.T) {
	schema := planSchema()
	env := RenderSchemaEnvelope(schema)

	assert.Contains(t, env, EnvelopeFingerprintToken(SchemaFingerprint(schema)),
		"the history scan keys off this exact token")
	assert.Contains(t, env, `"blockers"`, "the envelope must carry the schema itself")
	assert.Contains(t, env, "supersedes",
		"a forked step's history still holds the previous envelope; the model must know which governs")
	assert.True(t, strings.HasPrefix(env, "<system-reminder>\n"))
	assert.True(t, strings.HasSuffix(env, "</system-reminder>"))
}

func TestParseSchemaDeliveryFailsSafe(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want SchemaDelivery
		ok   bool
	}{
		{"", SchemaDeliveryMessage, true},
		{"message", SchemaDeliveryMessage, true},
		{"  message ", SchemaDeliveryMessage, false},
		{"Message", SchemaDeliveryMessage, false},
		{"tool", SchemaDeliveryTool, true},
		{"Tool", SchemaDeliveryMessage, false},
		{"nonsense", SchemaDeliveryMessage, false},
	} {
		got, ok := ParseSchemaDelivery(tc.in)
		assert.Equal(t, tc.want, got, "input %q", tc.in)
		assert.Equal(t, tc.ok, ok, "input %q", tc.in)
	}
}

// Message delivery declares `output` as an object. A schema whose root is not
// an object cannot be satisfied under that declaration — the tool block and the
// envelope would contradict each other — so it keeps the legacy surface rather
// than becoming unanswerable.
func TestStructOutputNonObjectSchemaFallsBackToToolDelivery(t *testing.T) {
	for name, schema := range map[string]map[string]any{
		"array":  {"type": "array", "items": map[string]any{"type": "string"}},
		"string": {"type": "string"},
		"number": {"type": "number"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.False(t, SupportsMessageDelivery(schema))

			tool := NewStructOutputToolWithDelivery(schema, SchemaDeliveryMessage)
			assert.Equal(t, NewStructOutputToolWithDelivery(schema, SchemaDeliveryTool).Info(), tool.Info(),
				"a schema message delivery cannot express must keep the legacy tool surface")

			// And the payload the legacy surface asks for still round-trips.
			resp := runDeliveryTool(t, tool, `{"output":["a","b"]}`)
			assert.False(t, resp.IsError, resp.Content)
		})
	}
}

// A schema that declares its own `output` property makes the wrapper key
// ambiguous with a real field; unwrapping would return that field's value as
// the whole document and silently drop its siblings.
func TestStructOutputSchemaDeclaringOutputFallsBackToToolDelivery(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"output": map[string]any{"type": "object"},
			"status": map[string]any{"type": "string"},
		},
		"required": []any{"output", "status"},
	}
	assert.False(t, SupportsMessageDelivery(schema))

	tool := NewStructOutputToolWithDelivery(schema, SchemaDeliveryMessage)
	resp := runDeliveryTool(t, tool, `{"output":{"body":"hi"},"status":"ok"}`)
	require.False(t, resp.IsError, resp.Content)

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &doc))
	assert.Equal(t, "ok", doc["status"], "sibling fields must not be dropped")
	assert.Equal(t, map[string]any{"body": "hi"}, doc["output"])
}

// A model that wraps only part of the document must not lose the rest. Silently
// dropping `blockers` here would let applyDefaults refill it with its default —
// a flow routing on ${args.blockers} would read "no blockers" from a run that
// reported one, which is the exact failure the defaults logic exists to prevent.
func TestStructOutputRecoversHalfWrappedPayload(t *testing.T) {
	tool := NewStructOutputToolWithDelivery(planSchema(), SchemaDeliveryMessage)

	resp := runDeliveryTool(t, tool, `{"output":{"summary":"x"},"blockers":["db migration"]}`)
	require.False(t, resp.IsError, resp.Content)

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &doc))
	assert.Equal(t, "x", doc["summary"])
	assert.Equal(t, []any{"db migration"}, doc["blockers"],
		"a sibling the schema declares must survive, not be replaced by its default")
}

func TestStructOutputHalfWrapDropsUnknownSiblings(t *testing.T) {
	tool := NewStructOutputToolWithDelivery(planSchema(), SchemaDeliveryMessage)

	resp := runDeliveryTool(t, tool, `{"output":{"summary":"x","blockers":[]},"chatter":"hi"}`)
	require.False(t, resp.IsError, resp.Content)

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(resp.Content), &doc))
	assert.NotContains(t, doc, "chatter", "keys the schema does not declare are noise, not content")
}

// `{}` is resolved by defaults + the required check in tool delivery; message
// delivery must reach the same verdict, or "validation is delivery-independent"
// is not true.
func TestStructOutputEmptyPayloadIsDeliveryIndependent(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"blockers": map[string]any{"type": "array", "default": []any{}},
		},
		"required": []any{"blockers"},
	}

	for _, delivery := range []SchemaDelivery{SchemaDeliveryMessage, SchemaDeliveryTool} {
		t.Run(string(delivery), func(t *testing.T) {
			resp := runDeliveryTool(t, NewStructOutputToolWithDelivery(schema, delivery), `{}`)
			require.False(t, resp.IsError, resp.Content)

			var doc map[string]any
			require.NoError(t, json.Unmarshal([]byte(resp.Content), &doc))
			assert.Equal(t, []any{}, doc["blockers"])
		})
	}
}

// A literal `null` input decodes to a nil map, which panics on the first write
// in applyDefaults.
func TestStructOutputNullInputIsRejectedNotPanicking(t *testing.T) {
	for _, delivery := range []SchemaDelivery{SchemaDeliveryMessage, SchemaDeliveryTool} {
		t.Run(string(delivery), func(t *testing.T) {
			resp := runDeliveryTool(t, NewStructOutputToolWithDelivery(planSchema(), delivery), `null`)
			assert.True(t, resp.IsError)
			assert.Contains(t, resp.Content, "summary")
		})
	}
}

// The envelope is plain text the model reads verbatim, so Go's default HTML
// escaping would show it < instead of <. In the tool block the API decoded
// those escapes before the model saw them; here nothing does.
func TestRenderSchemaEnvelopeDoesNotHTMLEscape(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"note": map[string]any{"type": "string", "description": "MR <id>, a && b, x > 0"},
		},
	}
	env := RenderSchemaEnvelope(schema)

	assert.Contains(t, env, "MR <id>, a && b, x > 0",
		"the model must read the description as written, not as escape sequences")
	// Match on the escape token rather than the backslash form so the assertion
	// cannot itself be mangled by an escaping layer.
	for _, escaped := range []string{"u003c", "u003e", "u0026"} {
		assert.NotContains(t, env, escaped, "schema text must not be HTML-escaped")
	}
}

package provider

import (
	"encoding/json"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stepSchema(field string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			field: map[string]any{"type": "string", "description": "a " + field},
		},
		"required": []any{field},
	}
}

func toolsetForSchema(schema map[string]any, delivery tools.SchemaDelivery) []tools.BaseTool {
	return []tools.BaseTool{
		newTestTool("read", true),
		newTestTool("bash", true),
		tools.NewStructOutputToolWithDelivery(schema, delivery),
	}
}

// This is the test that pins the actual fix.
//
// Anthropic renders the cacheable prefix as tools → system → messages and
// invalidates everything after the first changed byte. So for two consecutive
// flow steps of one agent to share a cache entry, the serialized tools block
// must be byte-identical — a property we can assert directly and cheaply, which
// is both stronger and more durable than chasing a cache_read_input_tokens
// figure from a live call.
func TestConvertToolsIsByteStableAcrossStepSchemas(t *testing.T) {
	client := &anthropicClient{
		providerOptions: providerClientOptions{model: models.Model{}},
	}

	plan := client.convertTools(sessionCtx("s1"), toolsetForSchema(stepSchema("plan"), tools.SchemaDeliveryMessage))
	review := client.convertTools(sessionCtx("s1"), toolsetForSchema(stepSchema("verdict"), tools.SchemaDeliveryMessage))

	planJSON, err := json.Marshal(plan)
	require.NoError(t, err)
	reviewJSON, err := json.Marshal(review)
	require.NoError(t, err)

	assert.JSONEq(t, string(planJSON), string(reviewJSON),
		"two steps differing only in output schema must ship an identical tools block")
	assert.Equal(t, string(planJSON), string(reviewJSON),
		"and identical byte-for-byte — the cache matches on bytes, not on semantics")
}

// The breakpoint must not move either: it is a read point into the prefix, so a
// shifted position is a missed entry even when the bytes ahead of it match.
func TestConvertToolsBreakpointPositionUnchangedAcrossSchemas(t *testing.T) {
	client := &anthropicClient{
		providerOptions: providerClientOptions{model: models.Model{}},
	}

	breakpointAt := func(schema map[string]any) int {
		result := client.convertTools(sessionCtx("s1"), toolsetForSchema(schema, tools.SchemaDeliveryMessage))
		for i, entry := range result {
			if entry.OfTool != nil && entry.OfTool.CacheControl.Type != "" {
				return i
			}
		}
		return -1
	}

	plan := breakpointAt(stepSchema("plan"))
	require.NotEqual(t, -1, plan, "a breakpoint must be placed at all")
	assert.Equal(t, plan, breakpointAt(stepSchema("verdict")))
}

// The escape hatch must still behave the old way — including the old cache
// behavior, so that "set it back to tool and the symptom returns" is a real
// diagnostic rather than a guess.
func TestConvertToolsVariesAcrossSchemasInToolDelivery(t *testing.T) {
	client := &anthropicClient{
		providerOptions: providerClientOptions{model: models.Model{}},
	}

	plan, err := json.Marshal(client.convertTools(sessionCtx("s1"), toolsetForSchema(stepSchema("plan"), tools.SchemaDeliveryTool)))
	require.NoError(t, err)
	review, err := json.Marshal(client.convertTools(sessionCtx("s1"), toolsetForSchema(stepSchema("verdict"), tools.SchemaDeliveryTool)))
	require.NoError(t, err)

	assert.NotEqual(t, string(plan), string(review),
		"tool delivery is the pre-change behavior: the schema is in the tool block and the prefix moves with it")
}

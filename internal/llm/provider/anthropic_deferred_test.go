package provider

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sessionCtx(id string) context.Context {
	return context.WithValue(context.Background(), tools.SessionIDContextKey, id)
}

func TestConvertToolsNativeDeferredPath(t *testing.T) {
	client := &anthropicClient{
		providerOptions: providerClientOptions{model: models.Model{SupportsToolSearch: true}},
	}
	seq := &atomic.Int64{}
	deferred := tools.WrapDeferred(newTestTool("jira_add_comment", false), seq)
	toolset := []tools.BaseTool{
		newTestTool("read", true),
		tools.NewToolSearchTool(),
		deferred,
	}

	result := client.convertTools(sessionCtx("s1"), toolset)

	// read + deferred(jira) + server tool; client toolsearch omitted.
	require.Len(t, result, 3)
	assert.Equal(t, "read", result[0].OfTool.Name)
	assert.False(t, result[0].OfTool.DeferLoading.Valid() && result[0].OfTool.DeferLoading.Value)

	assert.Equal(t, "jira_add_comment", result[1].OfTool.Name)
	assert.True(t, result[1].OfTool.DeferLoading.Valid() && result[1].OfTool.DeferLoading.Value,
		"deferred tool must carry defer_loading on the native path")

	require.NotNil(t, result[2].OfToolSearchToolRegex20251119, "server tool-search tool must be appended")

	// Breakpoint on the last non-deferred entry: the server tool.
	assert.NotEmpty(t, result[2].OfToolSearchToolRegex20251119.CacheControl.Type,
		"breakpoint must land on the server tool (last non-deferred entry)")
	assert.Empty(t, result[1].OfTool.CacheControl.Type, "deferred entry must not carry the breakpoint")

	// Activation must not change the native payload (defer_loading stays).
	deferred.Activate("s1")
	again := client.convertTools(sessionCtx("s1"), toolset)
	require.Len(t, again, 3)
	assert.True(t, again[1].OfTool.DeferLoading.Valid() && again[1].OfTool.DeferLoading.Value,
		"defer_loading stays set after activation — cache prefix stability")
}

func TestConvertToolsFallbackDeferredPath(t *testing.T) {
	// SupportsToolSearch=false — e.g. Kimi riding this same client.
	client := &anthropicClient{
		providerOptions: providerClientOptions{model: models.Model{SupportsToolSearch: false}},
	}
	seq := &atomic.Int64{}
	a := tools.WrapDeferred(newTestTool("a_tool", false), seq)
	b := tools.WrapDeferred(newTestTool("b_tool", false), seq)
	ts := tools.NewToolSearchTool()
	toolset := []tools.BaseTool{newTestTool("read", true), ts, a, b}

	// Nothing activated: deferred omitted, client toolsearch serialized.
	result := client.convertTools(sessionCtx("s1"), toolset)
	require.Len(t, result, 2)
	assert.Equal(t, "read", result[0].OfTool.Name)
	assert.Equal(t, tools.ToolSearchToolName, result[1].OfTool.Name)

	// Activate b then a: appended in activation order after the prefix.
	b.Activate("s1")
	a.Activate("s1")
	result = client.convertTools(sessionCtx("s1"), toolset)
	require.Len(t, result, 4)
	assert.Equal(t, "b_tool", result[2].OfTool.Name)
	assert.Equal(t, "a_tool", result[3].OfTool.Name)
	assert.NotEmpty(t, result[3].OfTool.CacheControl.Type, "breakpoint on last serialized entry")

	// Another session still sees the lean payload.
	other := client.convertTools(sessionCtx("s2"), toolset)
	require.Len(t, other, 2, "sessions must not observe each other's activations")
}

func TestConvertToolsNoDeferralIdentity(t *testing.T) {
	client := &anthropicClient{
		providerOptions: providerClientOptions{model: models.Model{SupportsToolSearch: true}},
	}
	toolset := []tools.BaseTool{newTestTool("read", true), newTestTool("mcp_a", false)}

	result := client.convertTools(sessionCtx("s1"), toolset)
	require.Len(t, result, 2, "no wrappers ⇒ no server tool, no omissions")
	assert.Equal(t, "read", result[0].OfTool.Name)
	assert.Equal(t, "mcp_a", result[1].OfTool.Name)
	assert.NotEmpty(t, result[1].OfTool.CacheControl.Type, "pre-feature breakpoint placement preserved")
}

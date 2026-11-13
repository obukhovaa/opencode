package provider

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

func TestFlowToolGeminiHasNoUnexpressibleObjectParameters(t *testing.T) {
	converted := (&geminiClient{}).convertTools(context.Background(), []tools.BaseTool{tools.NewFlowTool()})
	require.Len(t, converted, 1)
	require.Len(t, converted[0].FunctionDeclarations, 1)
	params := converted[0].FunctionDeclarations[0].Parameters
	require.NotNil(t, params)
	var check func(*genai.Schema)
	check = func(schema *genai.Schema) {
		if schema == nil {
			return
		}
		if schema.Type == genai.TypeObject {
			require.NotEmpty(t, schema.Properties, "Gemini rejects empty object parameter schemas")
		}
		for _, child := range schema.Properties {
			check(child)
		}
		check(schema.Items)
	}
	check(params)
	require.Contains(t, params.Properties, "argsJson")
	require.NotContains(t, params.Properties, "args")
}

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
)

const (
	StructOutputToolName = "struct_output"

	structOutputDescription = `Emit your final answer as structured JSON conforming to the schema defined in this tool's parameters.

WHEN TO USE THIS TOOL:
- Use this tool to provide your final response when the user has requested structured output
- You MUST call this tool exactly once as your final action

HOW TO USE:
- Populate every required field described in the parameters
- The JSON you pass will be validated and returned as the agent's output`
)

type structOutputTool struct {
	schema       map[string]any
	structParams map[string]any
	required     []string
}

func NewStructOutputTool(schema map[string]any) BaseTool {
	params, required := buildParamsFromSchema(schema)
	return &structOutputTool{
		schema:       schema,
		structParams: params,
		required:     required,
	}
}

func (s *structOutputTool) Info() ToolInfo {
	return ToolInfo{
		Name:        StructOutputToolName,
		Description: structOutputDescription,
		Parameters:  s.structParams,
		Required:    s.required,
	}
}

func (s *structOutputTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var result map[string]any
	if err := json.Unmarshal([]byte(call.Input), &result); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Invalid JSON: %s", err.Error())), nil
	}
	output, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to format output: %s", err.Error())), nil
	}
	return NewTextResponse(string(output)), nil
}

// buildParamsFromSchema converts a JSON schema into the ToolInfo.Parameters format.
// If the schema is an object type with properties, those properties are used directly.
// Otherwise, the entire schema is wrapped as a single "output" parameter.
func buildParamsFromSchema(schema map[string]any) (map[string]any, []string) {
	schemaType, _ := schema["type"].(string)
	if schemaType == "object" {
		if props, ok := schema["properties"].(map[string]any); ok {
			params := make(map[string]any, len(props))
			maps.Copy(params, props)
			var required []string
			if req, ok := schema["required"].([]any); ok {
				for _, r := range req {
					if s, ok := r.(string); ok {
						required = append(required, s)
					}
				}
			}
			return params, required
		}
	}

	// Fallback: wrap entire schema as a single "output" parameter
	return map[string]any{
		"output": schema,
	}, []string{"output"}
}

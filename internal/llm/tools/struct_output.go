package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"
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

	// Make the emitted output schema-conformant BEFORE it is stored and threaded
	// into downstream flow routing args. Models routinely omit empty-valued
	// fields that carry a `default` — an empty array like `blockers: []` is the
	// most common. Left absent, any flow routing rule that keys off
	// `${args.<field>}` sees a MISSING key: every predicate evaluates false, no
	// rule matches, and the flow silently strands (observed on
	// developer-react-on-jira's plan-to-implement → implement transition).
	// Materializing declared defaults keeps that routing contract whole. A
	// required field that has no default and is still missing is a genuinely
	// incomplete answer — reject it so the model retries (the agent loop
	// re-enters on an error struct_output result) instead of persisting a
	// half-filled output.
	if _, ok := objectProperties(s.schema); ok {
		applyDefaults(s.schema, result)
		if missing := missingRequiredFields(s.required, result); len(missing) > 0 {
			return NewTextErrorResponse(fmt.Sprintf(
				"struct_output is missing required field(s): %s. Return the complete JSON with every required field populated (an empty array/object/string is a valid value when a field has no content) and call struct_output again.",
				strings.Join(missing, ", "),
			)), nil
		}
	}

	output, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("Failed to format output: %s", err.Error())), nil
	}
	return NewTextResponse(string(output)), nil
}

func (s *structOutputTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
	return false
}

func (s *structOutputTool) IsBaseline() bool { return true }

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

// objectProperties returns the property schemas of an object-type schema node.
// It keys off the presence of a `properties` map rather than the `type` field,
// so it also handles union types such as ["object","null"]. When the node is
// not an object-with-properties (e.g. the non-object fallback schema wrapped as
// a single "output" parameter) it returns ok=false and callers skip
// default/required processing, preserving the prior pass-through behavior.
func objectProperties(schema map[string]any) (props map[string]any, ok bool) {
	props, ok = schema["properties"].(map[string]any)
	return props, ok
}

// applyDefaults recursively materializes JSON-schema `default` values for
// properties omitted from data. An absent property that declares a default is
// filled with a deep copy of that default; a property that is present and is
// itself an object is descended into so nested defaults are filled too. Absent
// properties without a default are left absent — required-ness is enforced
// separately, after defaults have had a chance to satisfy it.
func applyDefaults(schema map[string]any, data map[string]any) {
	props, ok := objectProperties(schema)
	if !ok {
		return
	}
	for key, raw := range props {
		propSchema, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, present := data[key]; !present {
			if def, hasDefault := propSchema["default"]; hasDefault {
				data[key] = cloneJSONValue(def)
			}
			continue
		}
		if child, ok := data[key].(map[string]any); ok {
			applyDefaults(propSchema, child)
		}
	}
}

// missingRequiredFields returns the required keys absent from data, sorted for a
// stable error message. Defaults are expected to have been applied already, so
// a key only surfaces here when it has neither a value nor a declared default.
func missingRequiredFields(required []string, data map[string]any) []string {
	var missing []string
	for _, key := range required {
		if _, ok := data[key]; !ok {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return missing
}

// cloneJSONValue deep-copies a JSON-decoded value (map/slice/scalar) so a
// materialized default is never an alias into the shared, reused schema map.
func cloneJSONValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		cp := make(map[string]any, len(val))
		for k, vv := range val {
			cp[k] = cloneJSONValue(vv)
		}
		return cp
	case []any:
		cp := make([]any, len(val))
		for i, vv := range val {
			cp[i] = cloneJSONValue(vv)
		}
		return cp
	default:
		return val
	}
}

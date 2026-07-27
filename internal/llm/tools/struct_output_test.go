package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewStructOutputTool_ObjectSchema(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
			"score":   map[string]any{"type": "number"},
		},
		"required": []any{"summary", "score"},
	}

	tool := NewStructOutputTool(schema)
	info := tool.Info()

	if info.Name != StructOutputToolName {
		t.Errorf("expected name %q, got %q", StructOutputToolName, info.Name)
	}

	if _, ok := info.Parameters["summary"]; !ok {
		t.Error("expected 'summary' in parameters")
	}
	if _, ok := info.Parameters["score"]; !ok {
		t.Error("expected 'score' in parameters")
	}

	if len(info.Required) != 2 {
		t.Errorf("expected 2 required fields, got %d", len(info.Required))
	}
}

func TestNewStructOutputTool_NonObjectSchema(t *testing.T) {
	schema := map[string]any{
		"type": "string",
	}

	tool := NewStructOutputTool(schema)
	info := tool.Info()

	if _, ok := info.Parameters["output"]; !ok {
		t.Error("expected 'output' wrapper in parameters for non-object schema")
	}
	if len(info.Required) != 1 || info.Required[0] != "output" {
		t.Errorf("expected required=[output], got %v", info.Required)
	}
}

func TestStructOutputTool_Run_ValidJSON(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{"type": "string"},
		},
	}

	tool := NewStructOutputTool(schema)
	input := `{"title": "Hello World"}`

	resp, err := tool.Run(context.Background(), ToolCall{
		ID:    "test-1",
		Name:  StructOutputToolName,
		Input: input,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error response: %s", resp.Content)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(resp.Content), &result); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if result["title"] != "Hello World" {
		t.Errorf("expected title 'Hello World', got %v", result["title"])
	}
}

func TestStructOutputTool_Run_InvalidJSON(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{"type": "string"},
		},
	}

	tool := NewStructOutputTool(schema)

	resp, err := tool.Run(context.Background(), ToolCall{
		ID:    "test-2",
		Name:  StructOutputToolName,
		Input: "not valid json",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Error("expected error response for invalid JSON")
	}
}

// runStruct is a helper that runs the struct_output tool for a schema+input and
// returns the parsed result map plus the raw response.
func runStruct(t *testing.T, schema map[string]any, input string) (map[string]any, ToolResponse) {
	t.Helper()
	resp, err := NewStructOutputTool(schema).Run(context.Background(), ToolCall{
		ID:    "t",
		Name:  StructOutputToolName,
		Input: input,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	if !resp.IsError {
		if uerr := json.Unmarshal([]byte(resp.Content), &result); uerr != nil {
			t.Fatalf("response is not valid JSON: %v\ncontent: %s", uerr, resp.Content)
		}
	}
	return result, resp
}

// TestStructOutputTool_Run_MaterializesOmittedDefault reproduces the
// plan-to-implement strand: the model emits struct_output without the
// `blockers` field (required, default []). The tool must fill the default so the
// stored output carries `blockers: []` and downstream routing on
// `${args.blockers}` evaluates instead of matching no rule.
func TestStructOutputTool_Run_MaterializesOmittedDefault(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"plan":       map[string]any{"type": "array", "default": []any{}, "items": map[string]any{"type": "string"}},
			"blockers":   map[string]any{"type": "array", "default": []any{}, "items": map[string]any{"type": "string"}},
			"estimation": map[string]any{"type": "string"},
		},
		"required": []any{"plan", "blockers"},
	}
	// Omits `blockers` entirely — exactly what the model did on MICRO-1024.
	result, resp := runStruct(t, schema, `{"estimation":"L","plan":["step one"]}`)

	if resp.IsError {
		t.Fatalf("required field with a default must NOT be rejected; got error: %s", resp.Content)
	}
	blockers, ok := result["blockers"].([]any)
	if !ok {
		t.Fatalf("expected `blockers` to be materialized as an array, got %T (%v)", result["blockers"], result["blockers"])
	}
	if len(blockers) != 0 {
		t.Errorf("expected `blockers` == [], got %v", blockers)
	}
	if result["estimation"] != "L" {
		t.Errorf("expected estimation preserved as L, got %v", result["estimation"])
	}
	if plan, _ := result["plan"].([]any); len(plan) != 1 {
		t.Errorf("expected plan preserved with 1 item, got %v", result["plan"])
	}
}

// TestStructOutputTool_Run_RejectsMissingRequiredWithoutDefault ensures a
// genuinely-missing required field (no default to fall back on) is rejected so
// the agent loop re-enters and the model retries, rather than persisting a
// half-filled output.
func TestStructOutputTool_Run_RejectsMissingRequiredWithoutDefault(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
			"score":   map[string]any{"type": "number"},
		},
		"required": []any{"summary", "score"},
	}
	_, resp := runStruct(t, schema, `{"summary":"looks good"}`)

	if !resp.IsError {
		t.Fatalf("expected error response for missing required field `score`, got success: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "score") {
		t.Errorf("error message should name the missing field `score`, got: %s", resp.Content)
	}
	if strings.Contains(resp.Content, "summary") {
		t.Errorf("error message should NOT name the present field `summary`, got: %s", resp.Content)
	}
}

// TestStructOutputTool_Run_MultipleMissingRequiredSorted checks the error lists
// every missing required field, deterministically ordered.
func TestStructOutputTool_Run_MultipleMissingRequiredSorted(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"gamma": map[string]any{"type": "string"},
			"alpha": map[string]any{"type": "string"},
			"beta":  map[string]any{"type": "string"},
		},
		"required": []any{"gamma", "alpha", "beta"},
	}
	_, resp := runStruct(t, schema, `{}`)

	if !resp.IsError {
		t.Fatalf("expected error for all-missing required fields, got: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "alpha, beta, gamma") {
		t.Errorf("expected sorted 'alpha, beta, gamma' in message, got: %s", resp.Content)
	}
}

// TestStructOutputTool_Run_DefaultDoesNotOverridePresent verifies a provided
// value is never clobbered by the schema default.
func TestStructOutputTool_Run_DefaultDoesNotOverridePresent(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"blockers": map[string]any{"type": "array", "default": []any{}, "items": map[string]any{"type": "string"}},
		},
		"required": []any{"blockers"},
	}
	result, resp := runStruct(t, schema, `{"blockers":["needs a dependency build"]}`)

	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	blockers, _ := result["blockers"].([]any)
	if len(blockers) != 1 || blockers[0] != "needs a dependency build" {
		t.Errorf("expected provided blockers to be preserved, got %v", result["blockers"])
	}
}

// TestStructOutputTool_Run_NestedDefaults verifies defaults are filled inside a
// present nested object.
func TestStructOutputTool_Run_NestedDefaults(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"config": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"retries": map[string]any{"type": "number", "default": float64(3)},
					"mode":    map[string]any{"type": "string"},
				},
			},
		},
		"required": []any{"config"},
	}
	// config is present but omits `retries`.
	result, resp := runStruct(t, schema, `{"config":{"mode":"fast"}}`)

	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	cfg, ok := result["config"].(map[string]any)
	if !ok {
		t.Fatalf("expected config object, got %T", result["config"])
	}
	if cfg["retries"] != float64(3) {
		t.Errorf("expected nested default retries=3, got %v", cfg["retries"])
	}
	if cfg["mode"] != "fast" {
		t.Errorf("expected nested mode preserved, got %v", cfg["mode"])
	}
}

// TestApplyDefaults_DoesNotAliasSchema guards against a materialized default
// sharing backing storage with the reused schema map: mutating a filled default
// on one call must not leak into the next.
func TestApplyDefaults_DoesNotAliasSchema(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tags": map[string]any{"type": "array", "default": []any{"seed"}},
		},
	}

	first := map[string]any{}
	applyDefaults(schema, first)
	// Mutate the materialized slice IN PLACE. An in-place element write (not an
	// append, which would reallocate and mask aliasing) propagates to any shared
	// backing array — so this fails if the default was handed out by reference.
	first["tags"].([]any)[0] = "mutated"

	second := map[string]any{}
	applyDefaults(schema, second)
	tags, _ := second["tags"].([]any)
	if len(tags) != 1 || tags[0] != "seed" {
		t.Errorf("schema default was aliased/mutated across calls, got %v", second["tags"])
	}
}

func TestBuildParamsFromSchema_ObjectWithProperties(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
			"age":  map[string]any{"type": "number"},
		},
		"required": []any{"name"},
	}

	params, required := buildParamsFromSchema(schema)

	if len(params) != 2 {
		t.Errorf("expected 2 params, got %d", len(params))
	}
	if len(required) != 1 || required[0] != "name" {
		t.Errorf("expected required=[name], got %v", required)
	}
}

func TestBuildParamsFromSchema_ObjectWithoutProperties(t *testing.T) {
	schema := map[string]any{
		"type": "object",
	}

	params, required := buildParamsFromSchema(schema)

	if _, ok := params["output"]; !ok {
		t.Error("expected 'output' wrapper for object without properties")
	}
	if len(required) != 1 || required[0] != "output" {
		t.Errorf("expected required=[output], got %v", required)
	}
}

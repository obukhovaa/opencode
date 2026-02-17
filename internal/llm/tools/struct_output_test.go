package tools

import (
	"context"
	"encoding/json"
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

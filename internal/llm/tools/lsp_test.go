package tools

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/stretchr/testify/assert"
)

func TestLspTool_Info(t *testing.T) {
	tool := NewLspTool(&noopLspService{})
	info := tool.Info()

	assert.Equal(t, LSPToolName, info.Name)
	assert.NotEmpty(t, info.Description)
	assert.Contains(t, info.Parameters, "operation")
	assert.Contains(t, info.Parameters, "filePath")
	assert.Contains(t, info.Parameters, "line")
	assert.Contains(t, info.Parameters, "character")
	assert.Equal(t, []string{"operation", "filePath", "line", "character"}, info.Required)
}

func TestLspTool_InvalidOperation(t *testing.T) {
	tool := NewLspTool(&noopLspService{})

	input, _ := json.Marshal(LspParams{
		Operation: "invalidOp",
		FilePath:  "/tmp/test.go",
		Line:      1,
		Character: 1,
	})

	resp, err := tool.Run(t.Context(), ToolCall{Input: string(input)})
	assert.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "invalid operation")
}

func TestLspTool_FileNotFound(t *testing.T) {
	tool := NewLspTool(&noopLspService{})

	input, _ := json.Marshal(LspParams{
		Operation: "goToDefinition",
		FilePath:  "/nonexistent/path/file.go",
		Line:      1,
		Character: 1,
	})

	resp, err := tool.Run(t.Context(), ToolCall{Input: string(input)})
	assert.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "file not found")
}

func TestLspTool_NoClients(t *testing.T) {
	svc := &noopLspService{}
	tool := NewLspTool(svc)

	tmpFile := t.TempDir() + "/test.go"
	if err := writeTestFile(tmpFile, "package main"); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(LspParams{
		Operation: "hover",
		FilePath:  tmpFile,
		Line:      1,
		Character: 1,
	})

	resp, err := tool.Run(t.Context(), ToolCall{Input: string(input)})
	assert.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "no LSP server available")
}

func TestLspTool_BadJSON(t *testing.T) {
	tool := NewLspTool(&noopLspService{})

	resp, err := tool.Run(t.Context(), ToolCall{Input: "not json"})
	assert.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "error parsing parameters")
}

func TestLspTool_ValidOperations(t *testing.T) {
	expected := []string{
		"goToDefinition",
		"findReferences",
		"hover",
		"documentSymbol",
		"workspaceSymbol",
		"goToImplementation",
		"prepareCallHierarchy",
		"incomingCalls",
		"outgoingCalls",
	}

	for _, op := range expected {
		assert.True(t, validOperations[op], "operation %q should be valid", op)
	}
	assert.Len(t, validOperations, len(expected))
}

func TestFormatLspResult(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		result    any
		expected  string
	}{
		{
			name:      "nil result",
			operation: "hover",
			result:    nil,
			expected:  "No results found for hover",
		},
		{
			name:      "empty slice",
			operation: "findReferences",
			result:    []string{},
			expected:  "No results found for findReferences",
		},
		{
			name:      "valid result",
			operation: "hover",
			result:    map[string]string{"content": "func main()"},
			expected:  "{\n  \"content\": \"func main()\"\n}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := formatLspResult(tt.operation, tt.result)
			assert.Equal(t, tt.expected, output)
		})
	}
}

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// noopLspService is a minimal LspService for testing
type noopLspService struct{}

func (s *noopLspService) Init(_ context.Context)                         {}
func (s *noopLspService) Shutdown(_ context.Context)                     {}
func (s *noopLspService) ForceShutdown()                                 {}
func (s *noopLspService) Clients() map[string]*lsp.Client                { return nil }
func (s *noopLspService) ClientsCh() <-chan *lsp.Client                  { return nil }
func (s *noopLspService) ClientsForFile(_ string) []*lsp.Client          { return nil }
func (s *noopLspService) NotifyOpenFile(_ context.Context, _ string)     {}
func (s *noopLspService) WaitForDiagnostics(_ context.Context, _ string) {}
func (s *noopLspService) FormatDiagnostics(_ string) string              { return "" }

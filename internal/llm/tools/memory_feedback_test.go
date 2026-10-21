package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type feedbackWriter struct{ BaseTool }

func (feedbackWriter) Info() ToolInfo {
	return ToolInfo{Name: WriteToolName, Description: "ordinary writer", Parameters: map[string]any{"file_path": map[string]any{"type": "string"}}}
}
func (feedbackWriter) Run(context.Context, ToolCall) (ToolResponse, error) {
	return NewTextResponse("Written"), nil
}

func TestMemoryFeedbackPreservesToolsAndWrittenContent(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "projects", "key", "memory", "note.md")
	os.MkdirAll(filepath.Dir(file), 0700)
	text := strings.Repeat("fact\n", 2000)
	os.WriteFile(file, []byte(text), 0600)
	base := feedbackWriter{}
	wrapped := WithMemoryFeedback(base, root, root)
	a, _ := json.Marshal(base.Info())
	b, _ := json.Marshal(wrapped.Info())
	if string(a) != string(b) {
		t.Fatal("tool schema changed")
	}
	input, _ := json.Marshal(map[string]string{"file_path": file})
	result, err := wrapped.Run(context.Background(), ToolCall{Name: WriteToolName, Input: string(input)})
	if err != nil || result.IsError || !strings.Contains(result.Content, "write succeeded") || !strings.Contains(result.Content, "8 KiB") || !strings.Contains(result.Content, "updatedAt") {
		t.Fatalf("%+v %v", result, err)
	}
	data, _ := os.ReadFile(file)
	if string(data) != text {
		t.Fatal("feedback truncated saved facts")
	}
	other := filepath.Join(root, "ordinary.md")
	os.WriteFile(other, []byte(text), 0600)
	input, _ = json.Marshal(map[string]string{"file_path": other})
	result, _ = wrapped.Run(context.Background(), ToolCall{Name: WriteToolName, Input: string(input)})
	if result.Content != "Written" {
		t.Fatal("ordinary file received memory feedback")
	}
}

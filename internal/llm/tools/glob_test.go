package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGlobFiles_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"foo.go", "bar.go", "baz.txt"} {
		os.WriteFile(filepath.Join(dir, name), []byte("content"), 0644)
	}

	t.Run("returns results within timeout", func(t *testing.T) {
		ctx := context.Background()
		files, _, err := globFiles(ctx, "**/*.go", dir, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(files) != 2 {
			t.Errorf("expected 2 files, got %d", len(files))
		}
	})

	t.Run("returns error when context already cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := globFiles(ctx, "**/*.go", dir, 100)
		if err == nil {
			t.Fatal("expected error for cancelled context")
		}
	})
}

func TestGlobTool_Run_ReturnsErrorOnTimeout(t *testing.T) {
	tool := &globTool{}

	// Use an already-cancelled context to simulate timeout
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("content"), 0644)

	input, _ := json.Marshal(GlobParams{Pattern: "**/*.go", Path: dir})
	call := ToolCall{ID: "test", Name: GlobToolName, Input: string(input)}

	_, err := tool.Run(ctx, call)
	if err == nil {
		t.Fatal("expected error from Run with cancelled context")
	}
	if !strings.Contains(err.Error(), "error finding files") {
		t.Errorf("error should mention finding files, got: %v", err)
	}
}

func TestGlobTool_Run_ReturnsResultsNormally(t *testing.T) {
	tool := &globTool{}

	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.go"} {
		os.WriteFile(filepath.Join(dir, name), []byte("content"), 0644)
	}

	input, _ := json.Marshal(GlobParams{Pattern: "*.go", Path: dir})
	call := ToolCall{ID: "test", Name: GlobToolName, Input: string(input)}

	resp, err := tool.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Errorf("response should not be error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "a.go") || !strings.Contains(resp.Content, "b.go") {
		t.Errorf("response should contain both files, got: %s", resp.Content)
	}
}

func TestGlobFiles_ShortTimeout(t *testing.T) {
	// Even a 1ns timeout that fires before work starts should produce an error, not hang
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("x"), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)

	_, _, err := globFiles(ctx, "**/*.go", dir, 100)
	if err == nil {
		t.Fatal("expected error for timed-out context")
	}
}

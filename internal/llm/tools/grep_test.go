package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSearchFiles_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("func main() {}"), 0644)
	os.WriteFile(filepath.Join(dir, "world.go"), []byte("func helper() {}"), 0644)

	t.Run("returns results within timeout", func(t *testing.T) {
		ctx := context.Background()
		matches, _, err := searchFiles(ctx, "func", dir, "", 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(matches) != 2 {
			t.Errorf("expected 2 matches, got %d", len(matches))
		}
	})

	t.Run("returns error when context already cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := searchFiles(ctx, "func", dir, "", 100)
		if err == nil {
			t.Fatal("expected error for cancelled context")
		}
	})
}

func TestGrepTool_Run_ReturnsErrorOnTimeout(t *testing.T) {
	tool := &grepTool{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("func main() {}"), 0644)

	input, _ := json.Marshal(GrepParams{Pattern: "func", Path: dir})
	call := ToolCall{ID: "test", Name: GrepToolName, Input: string(input)}

	_, err := tool.Run(ctx, call)
	if err == nil {
		t.Fatal("expected error from Run with cancelled context")
	}
	if !strings.Contains(err.Error(), "error searching files") {
		t.Errorf("error should mention searching files, got: %v", err)
	}
}

func TestGrepTool_Run_ReturnsResultsNormally(t *testing.T) {
	tool := &grepTool{}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("func main() {}"), 0644)

	input, _ := json.Marshal(GrepParams{Pattern: "func", Path: dir})
	call := ToolCall{ID: "test", Name: GrepToolName, Input: string(input)}

	resp, err := tool.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Errorf("response should not be error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "hello.go") {
		t.Errorf("response should contain file name, got: %s", resp.Content)
	}
}

func TestSearchFilesWithRegex_RespectsContextCancellation(t *testing.T) {
	dir := t.TempDir()
	// Create enough files so the walk takes a non-trivial amount of time
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0755)
	for i := 0; i < 50; i++ {
		os.WriteFile(filepath.Join(sub, fmt.Sprintf("file%d.go", i)), []byte("package main"), 0644)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := searchFilesWithRegex(ctx, "package", dir, "")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestSearchFiles_ShortTimeout(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("hello world"), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)

	_, _, err := searchFiles(ctx, "hello", dir, "", 100)
	if err == nil {
		t.Fatal("expected error for timed-out context")
	}
}

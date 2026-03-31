package shell

import (
	"context"
	"strings"
	"testing"
)

func TestExpandMarkup(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()

	t.Run("no markup returns unchanged", func(t *testing.T) {
		input := "Hello world, no shell here"
		result := ExpandMarkup(ctx, input, cwd)
		if result != input {
			t.Errorf("expected unchanged input, got %q", result)
		}
	})

	t.Run("simple echo command", func(t *testing.T) {
		input := "Output: !`echo hello`"
		result := ExpandMarkup(ctx, input, cwd)
		if !strings.Contains(result, "hello") {
			t.Errorf("expected output to contain 'hello', got %q", result)
		}
	})
}

func TestTruncateMarkupOutput(t *testing.T) {
	t.Run("short output unchanged", func(t *testing.T) {
		input := "hello\nworld"
		result := truncateMarkupOutput(input)
		if result != input {
			t.Errorf("expected unchanged, got %q", result)
		}
	})

	t.Run("empty string unchanged", func(t *testing.T) {
		result := truncateMarkupOutput("")
		if result != "" {
			t.Errorf("expected empty, got %q", result)
		}
	})

	t.Run("many lines get truncated", func(t *testing.T) {
		lines := make([]string, 3000)
		for i := range lines {
			lines[i] = "line"
		}
		input := strings.Join(lines, "\n")
		result := truncateMarkupOutput(input)
		if !strings.Contains(result, "truncated") {
			t.Errorf("expected truncation marker, got length %d", len(result))
		}
	})
}

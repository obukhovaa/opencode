package format

import (
	"context"
	"strings"
	"testing"
)

func TestExpandShellMarkup(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()

	t.Run("no markup returns unchanged", func(t *testing.T) {
		input := "Hello world, no shell here"
		result := ExpandShellMarkup(ctx, input, cwd)
		if result != input {
			t.Errorf("expected unchanged input, got %q", result)
		}
	})

	t.Run("simple echo command", func(t *testing.T) {
		input := "Output: !`echo hello`"
		result := ExpandShellMarkup(ctx, input, cwd)
		if !strings.Contains(result, "hello") {
			t.Errorf("expected output to contain 'hello', got %q", result)
		}
		if strings.Contains(result, "!`") {
			t.Errorf("expected markup to be replaced, got %q", result)
		}
		if strings.Contains(result, "```") {
			t.Errorf("expected no code fence in output, got %q", result)
		}
		if strings.Contains(result, "$ echo hello") {
			t.Errorf("expected no command echo in output, got %q", result)
		}
	})

	t.Run("multiple commands", func(t *testing.T) {
		input := "A: !`echo aaa` B: !`echo bbb`"
		result := ExpandShellMarkup(ctx, input, cwd)
		if !strings.Contains(result, "aaa") {
			t.Errorf("expected 'aaa', got %q", result)
		}
		if !strings.Contains(result, "bbb") {
			t.Errorf("expected 'bbb', got %q", result)
		}
	})

	t.Run("failing command includes exit code", func(t *testing.T) {
		input := "!`ls /nonexistent_path_xyz_12345`"
		result := ExpandShellMarkup(ctx, input, cwd)
		if strings.Contains(result, "!`") {
			t.Errorf("expected markup to be replaced, got %q", result)
		}
		if !strings.Contains(result, "stderr:") && !strings.Contains(result, "command failed") {
			t.Errorf("expected error indicator in output, got %q", result)
		}
	})

	t.Run("pipes work", func(t *testing.T) {
		input := "!`echo hello | tr a-z A-Z`"
		result := ExpandShellMarkup(ctx, input, cwd)
		if !strings.Contains(result, "HELLO") {
			t.Errorf("expected 'HELLO', got %q", result)
		}
	})

	t.Run("preserves surrounding text", func(t *testing.T) {
		input := "Before !`echo mid` After"
		result := ExpandShellMarkup(ctx, input, cwd)
		if !strings.HasPrefix(result, "Before ") {
			t.Errorf("expected to start with 'Before ', got %q", result)
		}
		if !strings.HasSuffix(result, " After") {
			t.Errorf("expected to end with ' After', got %q", result)
		}
	})
}

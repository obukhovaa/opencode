package tools

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestBuildPreview(t *testing.T) {
	t.Run("small content returned as-is", func(t *testing.T) {
		content := "line1\nline2\nline3"
		preview, totalLines := buildPreview(content, 500, 500)
		if preview != content {
			t.Errorf("expected content unchanged, got %q", preview)
		}
		if totalLines != 3 {
			t.Errorf("expected 3 lines, got %d", totalLines)
		}
	})

	t.Run("large content truncated with head and tail", func(t *testing.T) {
		var lines []string
		for i := range 3000 {
			lines = append(lines, fmt.Sprintf("line %d", i))
		}
		content := strings.Join(lines, "\n")

		preview, totalLines := buildPreview(content, 500, 500)
		if totalLines != 3000 {
			t.Errorf("expected 3000 lines, got %d", totalLines)
		}
		if !strings.Contains(preview, "--- First 500 lines ---") {
			t.Error("expected head section header")
		}
		if !strings.Contains(preview, "--- Last 500 lines ---") {
			t.Error("expected tail section header")
		}
		if !strings.Contains(preview, "2000 lines truncated") {
			t.Error("expected truncation count")
		}
		if !strings.Contains(preview, "line 0") {
			t.Error("expected first line in head")
		}
		if !strings.Contains(preview, "line 499") {
			t.Error("expected last head line")
		}
		if !strings.Contains(preview, "line 2999") {
			t.Error("expected last line in tail")
		}
		if strings.Contains(preview, "line 500\n") && strings.Contains(preview, "line 2499\n") {
			t.Error("middle content should not be present")
		}
	})

	t.Run("exactly at head+tail boundary", func(t *testing.T) {
		var lines []string
		for i := range 1000 {
			lines = append(lines, fmt.Sprintf("line %d", i))
		}
		content := strings.Join(lines, "\n")

		preview, totalLines := buildPreview(content, 500, 500)
		if totalLines != 1000 {
			t.Errorf("expected 1000 lines, got %d", totalLines)
		}
		if preview != content {
			t.Error("content at exact boundary should be returned as-is")
		}
	})
}

func TestPersistAndTruncate(t *testing.T) {
	t.Cleanup(func() {
		CleanupTempDir()
	})

	t.Run("empty content returns empty result", func(t *testing.T) {
		result := persistAndTruncate("", "stdout", BashToolName)
		if result.content != "" {
			t.Errorf("expected empty content, got %q", result.content)
		}
		if result.filePath != "" {
			t.Errorf("expected empty filePath, got %q", result.filePath)
		}
	})

	t.Run("small content returned unchanged", func(t *testing.T) {
		result := persistAndTruncate("hello world", "stdout", BashToolName)
		if result.content != "hello world" {
			t.Errorf("expected unchanged content, got %q", result.content)
		}
		if result.filePath != "" {
			t.Errorf("expected no temp file, got %q", result.filePath)
		}
	})

	t.Run("content over line limit creates temp file", func(t *testing.T) {
		var lines []string
		for i := range 3000 {
			lines = append(lines, fmt.Sprintf("line %d", i))
		}
		content := strings.Join(lines, "\n")

		result := persistAndTruncate(content, "stdout", BashToolName)

		if result.filePath == "" {
			t.Fatal("expected temp file to be created")
		}
		if !strings.Contains(result.content, "<stdout truncated: 3000 lines total>") {
			t.Errorf("expected truncation header, got %q", result.content[:100])
		}
		if !strings.Contains(result.content, "Full output saved to:") {
			t.Error("expected file path in output")
		}
		if !strings.Contains(result.content, "--- First 500 lines ---") {
			t.Error("expected head section")
		}
		if !strings.Contains(result.content, "--- Last 500 lines ---") {
			t.Error("expected tail section")
		}

		data, err := os.ReadFile(result.filePath)
		if err != nil {
			t.Fatalf("failed to read temp file: %v", err)
		}
		if string(data) != content {
			t.Error("temp file content does not match original")
		}
	})

	t.Run("content over byte limit creates temp file", func(t *testing.T) {
		line := strings.Repeat("A", 1000)
		var lines []string
		for range 100 {
			lines = append(lines, line)
		}
		content := strings.Join(lines, "\n")

		if len(content) <= MaxOutputBytes {
			t.Skip("test content not large enough")
		}

		result := persistAndTruncate(content, "stderr", BashToolName)

		if result.filePath == "" {
			t.Fatal("expected temp file to be created")
		}
		if !strings.Contains(result.content, "<stderr truncated:") {
			t.Error("expected truncation header with stderr label")
		}
	})

	t.Run("multibyte UTF-8 content preserved correctly", func(t *testing.T) {
		var lines []string
		for i := range 2500 {
			lines = append(lines, fmt.Sprintf("日本語テスト行 %d 🎉", i))
		}
		content := strings.Join(lines, "\n")

		result := persistAndTruncate(content, "stdout", BashToolName)

		if result.filePath == "" {
			t.Fatal("expected temp file to be created")
		}
		if !strings.Contains(result.content, "日本語テスト行 0") {
			t.Error("first line should be preserved intact")
		}
		if !strings.Contains(result.content, "日本語テスト行 2499") {
			t.Error("last line should be preserved intact")
		}
		if !strings.Contains(result.content, "🎉") {
			t.Error("emoji should be preserved intact (no mid-UTF-8 cut)")
		}
	})
}

func TestTruncateToMaxChars(t *testing.T) {
	t.Run("short content unchanged", func(t *testing.T) {
		result := truncateToMaxChars("hello", 100)
		if result != "hello" {
			t.Errorf("expected unchanged, got %q", result)
		}
	})

	t.Run("cuts at line boundary", func(t *testing.T) {
		content := "line1\nline2\nline3\nline4"
		result := truncateToMaxChars(content, 12)
		if result != "line1\nline2" {
			t.Errorf("expected cut at line boundary, got %q", result)
		}
	})

	t.Run("handles content with no newlines", func(t *testing.T) {
		content := strings.Repeat("A", 100)
		result := truncateToMaxChars(content, 50)
		if len(result) != 50 {
			t.Errorf("expected length 50, got %d", len(result))
		}
	})
}

func TestCleanupTempDir(t *testing.T) {
	var lines []string
	for i := range 3000 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	content := strings.Join(lines, "\n")

	result := persistAndTruncate(content, "stdout", BashToolName)
	if result.filePath == "" {
		t.Fatal("expected temp file to be created")
	}

	if _, err := os.Stat(result.filePath); os.IsNotExist(err) {
		t.Fatal("temp file should exist before cleanup")
	}

	CleanupTempDir()

	if _, err := os.Stat(result.filePath); !os.IsNotExist(err) {
		t.Fatal("temp file should be removed after cleanup")
	}
}

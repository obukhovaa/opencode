package tools

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPersistLargeOutput(t *testing.T) {
	t.Run("under cap returned unchanged with no file", func(t *testing.T) {
		content := strings.Repeat("a", 100)
		out, path := PersistLargeOutput(content, "tool", "mcp", 1024)
		if out != content {
			t.Errorf("content changed: got %d bytes, want %d", len(out), len(content))
		}
		if path != "" {
			t.Errorf("expected no file for under-cap content, got %q", path)
		}
	})

	t.Run("maxBytes<=0 disables the cap", func(t *testing.T) {
		content := strings.Repeat("a", 10_000)
		for _, cap := range []int{0, -1} {
			out, path := PersistLargeOutput(content, "tool", "mcp", cap)
			if out != content || path != "" {
				t.Errorf("cap=%d: expected unlimited passthrough, got %d bytes path=%q", cap, len(out), path)
			}
		}
	})

	t.Run("over cap spills to file and previews", func(t *testing.T) {
		head := strings.Repeat("H", 4000)
		mid := strings.Repeat("M", 40_000)
		tail := strings.Repeat("T", 4000)
		content := head + "\n" + mid + "\n" + tail
		out, path := PersistLargeOutput(content, "gitlab_diffs", "mcp", 4096)

		if path == "" {
			t.Fatal("expected a temp file path for over-cap content")
		}
		if len(out) >= len(content) {
			t.Errorf("preview (%d) not smaller than content (%d)", len(out), len(content))
		}
		if len(out) > 4096*3 { // header + ~cap of preview, generous bound
			t.Errorf("preview unexpectedly large: %d bytes", len(out))
		}
		for _, want := range []string{"output truncated", "Full output saved to: " + path, "bytes elided", "grep"} {
			if !strings.Contains(out, want) {
				t.Errorf("preview missing %q; got:\n%s", want, out)
			}
		}
		if !strings.Contains(out, "HHHH") || !strings.Contains(out, "TTTT") {
			t.Errorf("preview should carry head and tail fragments")
		}
		saved, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("saved file unreadable: %v", err)
		}
		if string(saved) != content {
			t.Errorf("saved file should hold the full original output (got %d, want %d bytes)", len(saved), len(content))
		}
	})

	t.Run("single-line payload (minified JSON) is still bounded", func(t *testing.T) {
		// No newlines: the line-based preview would keep everything; the byte-based
		// path must still truncate.
		content := `{"k":"` + strings.Repeat("x", 200_000) + `"}`
		out, path := PersistLargeOutput(content, "tool", "mcp", 8192)
		if path == "" {
			t.Fatal("expected spill to file for single-line payload")
		}
		if len(out) >= len(content) {
			t.Errorf("single-line content not bounded: preview %d vs content %d", len(out), len(content))
		}
	})

	t.Run("preview never splits a UTF-8 rune", func(t *testing.T) {
		// Multi-byte runes packed so cuts land mid-rune unless snapped.
		content := strings.Repeat("héllo—wörld", 5000) // é, —, ö are multi-byte
		out, path := PersistLargeOutput(content, "tool", "mcp", 4096)
		if path == "" {
			t.Fatal("expected spill to file")
		}
		if !utf8.ValidString(out) {
			t.Error("preview is not valid UTF-8 — a rune was split at a cut boundary")
		}
	})
}

func TestBuildBytePreview(t *testing.T) {
	t.Run("short content returned as-is", func(t *testing.T) {
		if got := buildBytePreview("small", 100, 100); got != "small" {
			t.Errorf("got %q, want unchanged", got)
		}
	})

	t.Run("long content keeps head and tail with marker", func(t *testing.T) {
		content := strings.Repeat("A", 5000) + strings.Repeat("B", 5000)
		got := buildBytePreview(content, 1000, 1000)
		if !strings.HasPrefix(got, "AAA") {
			t.Error("preview should start with head fragment")
		}
		if !strings.HasSuffix(got, "BBB") {
			t.Error("preview should end with tail fragment")
		}
		if !strings.Contains(got, "bytes elided") {
			t.Error("preview should contain an elision marker")
		}
		if len(got) >= len(content) {
			t.Errorf("preview (%d) should be smaller than content (%d)", len(got), len(content))
		}
	})
}

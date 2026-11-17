package tools

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/opencode-ai/opencode/internal/llm/tools/shell"
)

func TestBashOutputBounds(t *testing.T) {
	t.Cleanup(CleanupTempDir)
	tests := []struct {
		name      string
		content   string
		truncated bool
	}{
		{"exact byte cap", strings.Repeat("x", MaxOutputBytes), false},
		{"byte cap plus one", strings.Repeat("x", MaxOutputBytes+1), true},
		{"minified JSON", `{"data":"` + strings.Repeat("x", 1024*1024) + `"}`, true},
		{"long final line", strings.Repeat("x\n", 999) + strings.Repeat("y", MaxOutputBytes), true},
		{"long first line", strings.Repeat("y", MaxOutputBytes) + strings.Repeat("\nx", 999), true},
		{"many long lines", strings.Repeat(strings.Repeat("x", 1024)+"\n", 3000), true},
		{"Unicode head and tail", strings.Repeat("日🎉", MaxOutputBytes/2), true},
		{"Unicode trailing newline", strings.Repeat("日🎉", MaxOutputBytes/2) + "\n", true},
		{"line cap", strings.Repeat("x\n", MaxOutputLines-1) + "x", false},
		{"line cap plus one", strings.Repeat("x\n", MaxOutputLines) + "x", true},
		{"line only with trailing newline", strings.Repeat("x\n", MaxOutputLines), true},
		{"blank lines", strings.Repeat("\n", MaxOutputLines), true},
		{"within limits trailing blanks", "hello\n\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := persistAndTruncate(tt.content, "stdout", BashToolName)
			if !tt.truncated {
				if result.content != tt.content || result.filePath != "" {
					t.Fatal("within-limit output must be unchanged without a saved file")
				}
				return
			}
			if result.filePath == "" || !strings.Contains(result.content, "Full output saved to: "+result.filePath+"\n") {
				t.Fatal("truncated output must advertise its saved file")
			}
			if len(result.content) > MaxOutputBytes+1024 || !utf8.ValidString(result.content) {
				t.Fatalf("invalid preview: %d bytes, valid UTF-8=%t", len(result.content), utf8.ValidString(result.content))
			}
			if tt.name == "minified JSON" {
				t.Logf("input_bytes=%d configured_byte_cap=%d response_bytes=%d", len(tt.content), MaxOutputBytes, len(result.content))
			}
			_, preview, ok := strings.Cut(result.content, "\n\n")
			if !ok {
				t.Fatal("missing preview")
			}
			marker := regexp.MustCompile(`\n\n\.\.\. \[\d+ (?:bytes|lines) truncated\] \.\.\.\n\n`)
			parts := marker.Split(preview, -1)
			if len(parts) != 2 {
				t.Fatal("expected exactly one truncation marker")
			}
			head := strings.TrimPrefix(parts[0], "--- First 500 lines ---\n")
			tail := strings.TrimPrefix(parts[1], "--- Last 500 lines ---\n")
			if len(head)+len(tail) > MaxOutputBytes {
				t.Fatalf("excerpts exceed byte cap: %d", len(head)+len(tail))
			}
			if strings.Count(head, "\n")+1 > TruncatedHeadLines || strings.Count(tail, "\n")+1 > TruncatedTailLines {
				t.Fatal("excerpts exceed head/tail line cap")
			}
			if !strings.HasPrefix(tt.content, head) || !strings.HasSuffix(tt.content, tail) {
				t.Fatal("excerpts must preserve original head and tail")
			}
			saved, err := os.ReadFile(result.filePath)
			if err != nil || string(saved) != tt.content {
				t.Fatalf("saved output differs from original: %v", err)
			}
		})
	}
}

func TestBashOutputSaveFailure(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	processTempDirMu.Lock()
	previous := processTempDir
	processTempDir = blocked
	processTempDirMu.Unlock()
	t.Cleanup(func() {
		processTempDirMu.Lock()
		processTempDir = previous
		processTempDirMu.Unlock()
	})
	result := persistAndTruncate(strings.Repeat("x", MaxOutputBytes*2), "stdout", BashToolName)
	if result.filePath != "" || !strings.Contains(result.content, "Could not save") || strings.Contains(result.content, "saved to:") {
		t.Fatal("failed persistence must not advertise a saved file")
	}
	if len(result.content) > MaxOutputBytes+1024 {
		t.Fatal("failed persistence bypassed preview cap")
	}
}

func TestBashOutputPersistenceCap(t *testing.T) {
	t.Cleanup(CleanupTempDir)
	content := strings.Repeat("x", MaxPersistBytes) + "unsaved tail"
	result := persistAndTruncate(content, "stdout", BashToolName)
	if result.filePath == "" || !strings.Contains(result.content, "only the first 104857600 bytes") || strings.Contains(result.content, "Full output saved") {
		t.Fatal("capped persistence must identify a partial saved copy")
	}
	if len(result.content) > MaxOutputBytes+1024 || !strings.HasSuffix(result.content, "unsaved tail") {
		t.Fatal("capped persistence must still bound the preview and preserve its tail")
	}
	f, err := os.Open(result.filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Size() != MaxPersistBytes {
		t.Fatalf("saved size: %v, %v", stat, err)
	}
	buf := make([]byte, 32*1024)
	for {
		n, err := f.Read(buf)
		if strings.Trim(string(buf[:n]), "x") != "" {
			t.Fatal("saved bytes differ from original prefix")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestBashRunBoundedOutputMetadata(t *testing.T) {
	t.Setenv("SHELL", "/bin/bash")
	t.Cleanup(CleanupTempDir)
	dir := t.TempDir()
	stdout := strings.Repeat("O", MaxOutputBytes*2)
	stderr := strings.Repeat("E", MaxOutputBytes*2)
	for name, data := range map[string]string{"out": stdout, "err": stderr} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sh := shell.GetPersistentShell(dir)
	if sh == nil {
		t.Fatal("failed to start shell")
	}
	t.Cleanup(sh.Close)
	for _, tt := range []struct {
		name, command string
		streams       int
		wantSaved     string
	}{
		{"both streams", "cat out; cat err >&2; (exit 7)", 2, stdout},
		{"stderr only", "cat err >&2; (exit 7)", 1, stderr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input, err := json.Marshal(BashParams{Command: tt.command, Workdir: dir, Description: "bounded output"})
			if err != nil {
				t.Fatal(err)
			}
			b := NewBashTool(nil, bashPermissionRegistry{rules: map[string]any{"bash": "allow"}})
			ctx := context.WithValue(context.Background(), SessionIDContextKey, "output-bounds")
			ctx = context.WithValue(ctx, MessageIDContextKey, "msg")
			response, err := b.Run(ctx, ToolCall{ID: "output-bounds", Input: string(input)})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Content) > tt.streams*(MaxOutputBytes+1024) || !strings.HasSuffix(response.Content, "Exit code 7") {
				t.Fatalf("unexpected composed output: %d bytes", len(response.Content))
			}
			var metadata BashResponseMetadata
			if err := json.Unmarshal([]byte(response.Metadata), &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata.ExitCode != 7 || metadata.TempFilePath == "" || metadata.Description != "bounded output" || metadata.StartTime <= 0 || metadata.EndTime < metadata.StartTime {
				t.Fatalf("incorrect metadata: %+v", metadata)
			}
			paths := regexp.MustCompile(`Full output saved to: ([^\n]+)`).FindAllStringSubmatch(response.Content, -1)
			if len(paths) != tt.streams {
				t.Fatalf("saved paths=%d, want %d", len(paths), tt.streams)
			}
			for i, match := range paths {
				want := stderr
				if i == 0 {
					want = tt.wantSaved
				}
				saved, err := os.ReadFile(match[1])
				if err != nil || string(saved) != want {
					t.Fatalf("incorrect saved stream %d: %v", i, err)
				}
			}
			if metadata.TempFilePath != paths[0][1] {
				t.Fatal("metadata must prefer stdout's path, falling back to stderr")
			}
		})
	}
}

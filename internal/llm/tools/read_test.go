package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	mock_lsp "github.com/opencode-ai/opencode/internal/lsp/mocks"
)

// writeLines writes n lines "line-<i>" (1-based) padded to width bytes each.
func writeLines(t *testing.T, n, width int) string {
	t.Helper()
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		l := fmt.Sprintf("line-%d", i)
		sb.WriteString(l + strings.Repeat("x", max(width-len(l), 0)) + "\n")
	}
	path := filepath.Join(t.TempDir(), "big.log")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runRead(t *testing.T, tool BaseTool, params map[string]any) ToolResponse {
	t.Helper()
	raw, _ := json.Marshal(params)
	resp, err := tool.Run(newTestCtx(), ToolCall{ID: "1", Name: ReadToolName, Input: string(raw)})
	if err != nil {
		t.Fatalf("read returned a Go error: %v", err)
	}
	return resp
}

// A file over MaxReadSize: no LSP traffic is expected (gomock fails on any).
func newLargeFileReadTool(t *testing.T) BaseTool {
	return NewReadTool(mock_lsp.NewMockLspService(gomock.NewController(t)), nil, nil)
}

func TestRead_LargeFileWholeReadIsDeclined(t *testing.T) {
	path := writeLines(t, 4000, 100) // ~400KB
	resp := runRead(t, newLargeFileReadTool(t), map[string]any{"file_path": path})
	if !resp.IsError {
		t.Fatal("a whole-file read past MaxReadSize must be declined")
	}
	if !strings.Contains(resp.Content, "offset and limit") {
		t.Errorf("the refusal must point at offset/limit: %s", resp.Content)
	}
}

func TestRead_LargeFilePagedRead(t *testing.T) {
	path := writeLines(t, 4000, 100)
	tests := []struct {
		name      string
		params    map[string]any
		wantFirst string
		wantLast  string
		wantNote  string
	}{
		{name: "limit only", params: map[string]any{"limit": 10}, wantFirst: "     1|line-1x", wantLast: "    10|line-10x", wantNote: "Use offset=10"},
		{name: "offset and limit", params: map[string]any{"offset": 3000, "limit": 5}, wantFirst: "  3001|line-3001x", wantLast: "  3005|line-3005x", wantNote: "Use offset=3005"},
		{name: "offset only takes the default limit", params: map[string]any{"offset": 3500}, wantFirst: "  3501|line-3501x", wantLast: "  4000|line-4000x"},
		{name: "oversized limit is capped", params: map[string]any{"offset": 1, "limit": 100000}, wantFirst: "     2|line-2x", wantLast: "  2001|line-2001x", wantNote: "Use offset=2001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]any{"file_path": path}
			for k, v := range tt.params {
				params[k] = v
			}
			resp := runRead(t, newLargeFileReadTool(t), params)
			if resp.IsError {
				t.Fatalf("paged read declined: %s", resp.Content)
			}
			lines := strings.Split(resp.Content, "\n")
			if !strings.HasPrefix(lines[1], tt.wantFirst) {
				t.Errorf("first line = %q, want prefix %q", lines[1], tt.wantFirst)
			}
			if !strings.Contains(resp.Content, tt.wantLast) {
				t.Errorf("missing last line %q", tt.wantLast)
			}
			if tt.wantNote != "" && !strings.Contains(resp.Content, tt.wantNote) {
				t.Errorf("missing continuation note %q", tt.wantNote)
			}
			if tt.wantNote == "" && strings.Contains(resp.Content, "Use offset=") {
				t.Errorf("unexpected continuation note at EOF")
			}
			if !strings.Contains(resp.Content, "of 4000 total") && tt.wantNote != "" {
				t.Errorf("the note must give the file's total line count")
			}
		})
	}
}

// One call returns at most MaxReadSize bytes of file text, however many
// lines the limit allows: 2000 lines of 1,000 bytes would be ~2MB.
func TestRead_PagedReadIsBoundedByBytes(t *testing.T) {
	path := writeLines(t, 3000, 1000)
	resp := runRead(t, newLargeFileReadTool(t), map[string]any{"file_path": path, "limit": 2000})
	if resp.IsError {
		t.Fatalf("paged read declined: %s", resp.Content)
	}
	if len(resp.Content) > MaxReadSize+64*1024 {
		t.Fatalf("response is %d bytes, want it bounded near MaxReadSize (%d)", len(resp.Content), MaxReadSize)
	}
	if !strings.Contains(resp.Content, "output limit") || !strings.Contains(resp.Content, "Use offset=") {
		t.Errorf("a byte-capped page must say so and say where to continue: %.200s", resp.Content[len(resp.Content)-300:])
	}
}

// A line longer than any read buffer is truncated, not an error, and the
// lines after it are still numbered right.
func TestRead_HugeSingleLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "minified.js")
	content := "first\n" + strings.Repeat("a", 1<<20) + "\nthird\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := runRead(t, newLargeFileReadTool(t), map[string]any{"file_path": path, "limit": 3})
	if resp.IsError {
		t.Fatalf("read failed: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "     2|"+strings.Repeat("a", MaxLineLength)+"...") {
		t.Error("the long line must be cut to MaxLineLength with a marker")
	}
	if !strings.Contains(resp.Content, "     3|third") {
		t.Errorf("line after the long one misnumbered or missing: %.300s", resp.Content[len(resp.Content)-200:])
	}
}

func TestRead_OffsetPastEOF(t *testing.T) {
	path := writeLines(t, 4000, 100)
	resp := runRead(t, newLargeFileReadTool(t), map[string]any{"file_path": path, "offset": 5000, "limit": 10})
	if !resp.IsError || !strings.Contains(resp.Content, "4000 lines") {
		t.Errorf("offset past EOF: %+v", resp)
	}
}

// A paged read still counts as reading the file for the edit tools'
// read-before-edit check.
func TestRead_PagedReadRecordsTheRead(t *testing.T) {
	path := writeLines(t, 4000, 100)
	if !getLastReadTime(path).IsZero() {
		t.Fatal("fresh path already recorded")
	}
	runRead(t, newLargeFileReadTool(t), map[string]any{"file_path": path, "offset": 10, "limit": 10})
	if getLastReadTime(path).IsZero() {
		t.Error("a paged read was not recorded")
	}
}

// Small files keep the whole-file read and the LSP notification.
func TestRead_SmallFileUnchanged(t *testing.T) {
	path := writeLines(t, 3, 10)
	lsp := mock_lsp.NewMockLspService(gomock.NewController(t))
	lsp.EXPECT().NotifyOpenFile(gomock.Any(), path)
	lsp.EXPECT().FormatDiagnostics(path).Return("")
	resp := runRead(t, NewReadTool(lsp, nil, nil), map[string]any{"file_path": path})
	want := "<file>\n     1|line-1xxxx\n     2|line-2xxxx\n     3|line-3xxxx\n</file>\n"
	if resp.IsError || resp.Content != want {
		t.Errorf("content = %q, want %q", resp.Content, want)
	}
}

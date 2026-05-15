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

// helper to run the grep tool with given params
func runGrep(t *testing.T, params GrepParams) (ToolResponse, error) {
	t.Helper()
	tool := &grepTool{}
	input, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal params: %v", err)
	}
	return tool.Run(context.Background(), ToolCall{ID: "test", Name: GrepToolName, Input: string(input)})
}

// helper to create a temp dir with files
func setupGrepTestDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(path), 0755)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("failed to create test file %s: %v", name, err)
		}
	}
	return dir
}

func TestGrepTool_OutputModes(t *testing.T) {
	dir := setupGrepTestDir(t, map[string]string{
		"hello.go":   "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n",
		"world.go":   "package main\n\nfunc helper() {\n\tfmt.Println(\"world\")\n}\n",
		"readme.txt": "This is a readme file.\nNo Go code here.\n",
	})

	tests := []struct {
		name       string
		mode       string
		wantInResp []string // substrings that must appear in output
		wantNotIn  []string // substrings that must NOT appear
	}{
		{
			name:       "files_with_matches mode (default)",
			mode:       "",
			wantInResp: []string{"hello.go", "world.go", "Found 2 files"},
			wantNotIn:  []string{"readme.txt", "func main"},
		},
		{
			name:       "files_with_matches mode (explicit)",
			mode:       "files_with_matches",
			wantInResp: []string{"hello.go", "world.go", "Found 2 files"},
		},
		{
			name:       "content mode",
			mode:       "content",
			wantInResp: []string{"func main", "func helper"},
		},
		{
			name:       "count mode",
			mode:       "count",
			wantInResp: []string{"Found 2 total matches across 2 files"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := runGrep(t, GrepParams{
				Pattern:    "func",
				Path:       dir,
				OutputMode: tt.mode,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.IsError {
				t.Fatalf("response is error: %s", resp.Content)
			}
			for _, want := range tt.wantInResp {
				if !strings.Contains(resp.Content, want) {
					t.Errorf("response missing %q:\n%s", want, resp.Content)
				}
			}
			for _, notWant := range tt.wantNotIn {
				if strings.Contains(resp.Content, notWant) {
					t.Errorf("response should not contain %q:\n%s", notWant, resp.Content)
				}
			}
		})
	}
}

func TestGrepTool_CaseInsensitive(t *testing.T) {
	dir := setupGrepTestDir(t, map[string]string{
		"mixed.go": "package MAIN\nfunc Main() {}\nvar main = true\n",
	})

	t.Run("case sensitive misses uppercase", func(t *testing.T) {
		resp, err := runGrep(t, GrepParams{
			Pattern:    "package main",
			Path:       dir,
			OutputMode: "files_with_matches",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(resp.Content, "mixed.go") {
			t.Error("case-sensitive search should NOT match 'package MAIN'")
		}
	})

	t.Run("case insensitive finds it", func(t *testing.T) {
		resp, err := runGrep(t, GrepParams{
			Pattern:         "package main",
			Path:            dir,
			OutputMode:      "files_with_matches",
			CaseInsensitive: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(resp.Content, "mixed.go") {
			t.Errorf("case-insensitive search should match, got:\n%s", resp.Content)
		}
	})
}

func TestGrepTool_FileType(t *testing.T) {
	dir := setupGrepTestDir(t, map[string]string{
		"app.go":  "func main() {}",
		"app.js":  "function main() {}",
		"app.py":  "def main(): pass",
		"app.txt": "main function here",
	})

	resp, err := runGrep(t, GrepParams{
		Pattern:    "main",
		Path:       dir,
		OutputMode: "files_with_matches",
		FileType:   "go",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "app.go") {
		t.Errorf("should find app.go, got:\n%s", resp.Content)
	}
	if strings.Contains(resp.Content, "app.js") || strings.Contains(resp.Content, "app.py") || strings.Contains(resp.Content, "app.txt") {
		t.Errorf("should NOT find non-go files, got:\n%s", resp.Content)
	}
}

func TestGrepTool_GlobParameter(t *testing.T) {
	dir := setupGrepTestDir(t, map[string]string{
		"src/main.go":        "func main() {}",
		"src/helper.go":      "func helper() {}",
		"src/main.js":        "function main() {}",
		"tests/main_test.go": "func TestMain() {}",
	})

	t.Run("glob filters by extension", func(t *testing.T) {
		resp, err := runGrep(t, GrepParams{
			Pattern:    "main",
			Path:       dir,
			OutputMode: "files_with_matches",
			Glob:       "*.go",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(resp.Content, "main.js") {
			t.Errorf("glob *.go should exclude .js files, got:\n%s", resp.Content)
		}
		if !strings.Contains(resp.Content, "main.go") {
			t.Errorf("should include main.go, got:\n%s", resp.Content)
		}
	})

	t.Run("include alias works", func(t *testing.T) {
		resp, err := runGrep(t, GrepParams{
			Pattern:    "main",
			Path:       dir,
			OutputMode: "files_with_matches",
			Include:    "*.js",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(resp.Content, "main.js") {
			t.Errorf("include *.js should find main.js, got:\n%s", resp.Content)
		}
	})

	t.Run("glob takes priority over include", func(t *testing.T) {
		resp, err := runGrep(t, GrepParams{
			Pattern:    "main",
			Path:       dir,
			OutputMode: "files_with_matches",
			Glob:       "*.go",
			Include:    "*.js",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(resp.Content, "main.js") {
			t.Errorf("glob should override include, got:\n%s", resp.Content)
		}
	})
}

func TestGrepTool_Pagination(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 20; i++ {
		files[fmt.Sprintf("file%02d.go", i)] = fmt.Sprintf("func f%d() {}", i)
	}
	dir := setupGrepTestDir(t, files)

	t.Run("head_limit limits results", func(t *testing.T) {
		limit := 5
		resp, err := runGrep(t, GrepParams{
			Pattern:    "func",
			Path:       dir,
			OutputMode: "files_with_matches",
			HeadLimit:  &limit,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Count non-empty lines after the header
		lines := strings.Split(strings.TrimSpace(resp.Content), "\n")
		fileLines := 0
		for _, l := range lines {
			if strings.HasSuffix(l, ".go") {
				fileLines++
			}
		}
		if fileLines > 5 {
			t.Errorf("expected at most 5 file lines, got %d:\n%s", fileLines, resp.Content)
		}
	})

	t.Run("offset skips entries", func(t *testing.T) {
		limit := 5
		resp1, _ := runGrep(t, GrepParams{
			Pattern:    "func",
			Path:       dir,
			OutputMode: "files_with_matches",
			HeadLimit:  &limit,
			Offset:     0,
		})
		resp2, _ := runGrep(t, GrepParams{
			Pattern:    "func",
			Path:       dir,
			OutputMode: "files_with_matches",
			HeadLimit:  &limit,
			Offset:     5,
		})
		// The two pages should not overlap (different file sets)
		if resp1.Content == resp2.Content {
			t.Error("offset=0 and offset=5 should return different results")
		}
	})

	t.Run("content mode pagination", func(t *testing.T) {
		limit := 3
		resp, err := runGrep(t, GrepParams{
			Pattern:    "func",
			Path:       dir,
			OutputMode: "content",
			HeadLimit:  &limit,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(resp.Content, "Showing") {
			t.Errorf("paginated content should include pagination info, got:\n%s", resp.Content)
		}
	})
}

func TestGrepTool_ContextLines(t *testing.T) {
	dir := setupGrepTestDir(t, map[string]string{
		"code.go": "line1\nline2\ntarget_line\nline4\nline5\n",
	})

	t.Run("after_context", func(t *testing.T) {
		after := 1
		resp, err := runGrep(t, GrepParams{
			Pattern:      "target_line",
			Path:         dir,
			OutputMode:   "content",
			AfterContext: &after,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(resp.Content, "line4") {
			t.Errorf("after_context=1 should include line4, got:\n%s", resp.Content)
		}
	})

	t.Run("before_context", func(t *testing.T) {
		before := 1
		resp, err := runGrep(t, GrepParams{
			Pattern:       "target_line",
			Path:          dir,
			OutputMode:    "content",
			BeforeContext: &before,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(resp.Content, "line2") {
			t.Errorf("before_context=1 should include line2, got:\n%s", resp.Content)
		}
	})

	t.Run("context (both)", func(t *testing.T) {
		ctx := 1
		resp, err := runGrep(t, GrepParams{
			Pattern:    "target_line",
			Path:       dir,
			OutputMode: "content",
			Context:    &ctx,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(resp.Content, "line2") || !strings.Contains(resp.Content, "line4") {
			t.Errorf("context=1 should include line2 and line4, got:\n%s", resp.Content)
		}
	})
}

func TestGrepTool_Multiline(t *testing.T) {
	dir := setupGrepTestDir(t, map[string]string{
		"multi.go": "type Foo struct {\n\tName string\n}\n",
	})

	t.Run("multiline matches across lines", func(t *testing.T) {
		resp, err := runGrep(t, GrepParams{
			Pattern:    `struct \{[\s\S]*?Name`,
			Path:       dir,
			OutputMode: "files_with_matches",
			Multiline:  true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(resp.Content, "multi.go") {
			t.Errorf("multiline search should find multi.go, got:\n%s", resp.Content)
		}
	})

	t.Run("non-multiline does not match across lines", func(t *testing.T) {
		resp, err := runGrep(t, GrepParams{
			Pattern:    `struct \{[\s\S]*?Name`,
			Path:       dir,
			OutputMode: "files_with_matches",
			Multiline:  false,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(resp.Content, "multi.go") {
			t.Errorf("non-multiline search should NOT match across lines, got:\n%s", resp.Content)
		}
	})
}

func TestGrepTool_NoMatches(t *testing.T) {
	dir := setupGrepTestDir(t, map[string]string{
		"empty.go": "package main",
	})

	resp, err := runGrep(t, GrepParams{
		Pattern: "nonexistent_string_xyz",
		Path:    dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "No matches found") {
		t.Errorf("expected 'No matches found', got:\n%s", resp.Content)
	}
}

func TestGrepTool_LiteralText(t *testing.T) {
	dir := setupGrepTestDir(t, map[string]string{
		"special.go": "fmt.Println(\"hello\")\nlog.Error()\n",
	})

	resp, err := runGrep(t, GrepParams{
		Pattern:     "fmt.Println",
		Path:        dir,
		OutputMode:  "files_with_matches",
		LiteralText: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "special.go") {
		t.Errorf("literal_text should find exact match, got:\n%s", resp.Content)
	}
}

func TestGrepTool_DashPattern(t *testing.T) {
	dir := setupGrepTestDir(t, map[string]string{
		"dashes.txt": "--flag-name\nsome-text\n",
	})

	resp, err := runGrep(t, GrepParams{
		Pattern:    "--flag",
		Path:       dir,
		OutputMode: "content",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "--flag-name") {
		t.Errorf("dash-prefixed pattern should work, got:\n%s", resp.Content)
	}
}

func TestGrepTool_ReturnsErrorOnTimeout(t *testing.T) {
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

func TestGrepTool_ReturnsResultsNormally(t *testing.T) {
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
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0755)
	for i := 0; i < 50; i++ {
		os.WriteFile(filepath.Join(sub, fmt.Sprintf("file%d.go", i)), []byte("package main"), 0644)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := searchFilesWithRegex(ctx, "package", dir, "", 0)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestGrepTool_ShortTimeout(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("hello world"), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)

	tool := &grepTool{}
	input, _ := json.Marshal(GrepParams{Pattern: "hello", Path: dir})
	call := ToolCall{ID: "test", Name: GrepToolName, Input: string(input)}

	_, err := tool.Run(ctx, call)
	if err == nil {
		t.Fatal("expected error for timed-out context")
	}
}

func TestGrepTool_ResolveGlob(t *testing.T) {
	tests := []struct {
		name   string
		params GrepParams
		want   string
	}{
		{name: "glob only", params: GrepParams{Glob: "*.go"}, want: "*.go"},
		{name: "include only", params: GrepParams{Include: "*.js"}, want: "*.js"},
		{name: "glob overrides include", params: GrepParams{Glob: "*.go", Include: "*.js"}, want: "*.go"},
		{name: "neither", params: GrepParams{}, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.params.resolveGlob()
			if got != tt.want {
				t.Errorf("resolveGlob() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGrepTool_ResolveHeadLimit(t *testing.T) {
	t.Run("default is 250", func(t *testing.T) {
		p := GrepParams{}
		if got := p.resolveHeadLimit(); got != 250 {
			t.Errorf("resolveHeadLimit() = %d, want 250", got)
		}
	})

	t.Run("explicit zero means unlimited", func(t *testing.T) {
		zero := 0
		p := GrepParams{HeadLimit: &zero}
		if got := p.resolveHeadLimit(); got != 0 {
			t.Errorf("resolveHeadLimit() = %d, want 0", got)
		}
	})

	t.Run("explicit value", func(t *testing.T) {
		v := 42
		p := GrepParams{HeadLimit: &v}
		if got := p.resolveHeadLimit(); got != 42 {
			t.Errorf("resolveHeadLimit() = %d, want 42", got)
		}
	})
}

func TestGrepTool_ResolveOutputMode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "files_with_matches"},
		{"content", "content"},
		{"files_with_matches", "files_with_matches"},
		{"count", "count"},
		{"invalid", "files_with_matches"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			p := GrepParams{OutputMode: tt.input}
			if got := p.resolveOutputMode(); got != tt.want {
				t.Errorf("resolveOutputMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGrepTool_CountMode(t *testing.T) {
	dir := setupGrepTestDir(t, map[string]string{
		"a.go": "func a() {}\nfunc b() {}\nfunc c() {}\n",
		"b.go": "func d() {}\n",
	})

	resp, err := runGrep(t, GrepParams{
		Pattern:    "func",
		Path:       dir,
		OutputMode: "count",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "4 total matches") {
		t.Errorf("expected 4 total matches, got:\n%s", resp.Content)
	}
	if !strings.Contains(resp.Content, "2 files") {
		t.Errorf("expected 2 files, got:\n%s", resp.Content)
	}
}

func TestBuildRipgrepArgs(t *testing.T) {
	t.Run("base args always present", func(t *testing.T) {
		args := buildRipgrepArgs("test", &GrepParams{}, "", "files_with_matches", nil)
		joined := strings.Join(args, " ")
		for _, want := range []string{"--hidden", "--glob !.git", "--glob !node_modules", "--max-columns 500", "--max-columns-preview", "-l"} {
			if !strings.Contains(joined, want) {
				t.Errorf("missing %q in args: %s", want, joined)
			}
		}
	})

	t.Run("content mode with context", func(t *testing.T) {
		ctx := 3
		before := 1
		after := 2
		args := buildRipgrepArgs("test", &GrepParams{
			Context:       &ctx,
			BeforeContext: &before,
			AfterContext:  &after,
		}, "", "content", nil)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-C 3") {
			t.Errorf("missing -C 3 in args: %s", joined)
		}
		if !strings.Contains(joined, "-B 1") {
			t.Errorf("missing -B 1 in args: %s", joined)
		}
		if !strings.Contains(joined, "-A 2") {
			t.Errorf("missing -A 2 in args: %s", joined)
		}
	})

	t.Run("content mode includes field-match-separator", func(t *testing.T) {
		args := buildRipgrepArgs("test", &GrepParams{}, "", "content", nil)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--field-match-separator=") {
			t.Errorf("content mode should include --field-match-separator, got: %s", joined)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		args := buildRipgrepArgs("test", &GrepParams{CaseInsensitive: true}, "", "files_with_matches", nil)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-i") {
			t.Errorf("missing -i in args: %s", joined)
		}
	})

	t.Run("multiline", func(t *testing.T) {
		args := buildRipgrepArgs("test", &GrepParams{Multiline: true}, "", "files_with_matches", nil)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-U") || !strings.Contains(joined, "--multiline-dotall") {
			t.Errorf("missing multiline flags in args: %s", joined)
		}
	})

	t.Run("file type", func(t *testing.T) {
		args := buildRipgrepArgs("test", &GrepParams{FileType: "go"}, "", "files_with_matches", nil)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--type go") {
			t.Errorf("missing --type go in args: %s", joined)
		}
	})

	t.Run("glob param", func(t *testing.T) {
		args := buildRipgrepArgs("test", &GrepParams{}, "*.go", "files_with_matches", nil)
		joined := strings.Join(args, " ")
		// Should contain glob twice: once for !.git exclusion, once for user glob
		if !strings.Contains(joined, "--glob *.go") {
			t.Errorf("missing user --glob in args: %s", joined)
		}
	})

	t.Run("dash pattern uses -e", func(t *testing.T) {
		args := buildRipgrepArgs("--flag", &GrepParams{}, "", "files_with_matches", nil)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-e --flag") {
			t.Errorf("dash pattern should use -e, got: %s", joined)
		}
	})

	t.Run("deny patterns become glob exclusions", func(t *testing.T) {
		deny := []string{"/etc/secrets/*", ".env", ".env.*"}
		args := buildRipgrepArgs("test", &GrepParams{}, "", "files_with_matches", deny)
		joined := strings.Join(args, " ")
		// Absolute patterns keep their prefix
		if !strings.Contains(joined, "--glob !/etc/secrets/*") {
			t.Errorf("absolute deny pattern missing, got: %s", joined)
		}
		// Relative patterns get **/ prefix
		if !strings.Contains(joined, "--glob !**/.env ") {
			t.Errorf("relative deny pattern .env missing, got: %s", joined)
		}
		if !strings.Contains(joined, "--glob !**/.env.*") {
			t.Errorf("relative deny pattern .env.* missing, got: %s", joined)
		}
	})
}

func TestGrepTool_IgnoredDirs(t *testing.T) {
	dir := setupGrepTestDir(t, map[string]string{
		"src/main.go":                 "func main() {}",
		"node_modules/pkg/index.js":   "function main() {}",
		"bower_components/lib/lib.js": "function lib() {}",
		".opencode/state.json":        `{"func": true}`,
	})

	resp, err := runGrep(t, GrepParams{
		Pattern:    "func",
		Path:       dir,
		OutputMode: "files_with_matches",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Content, "main.go") {
		t.Errorf("should find src/main.go, got:\n%s", resp.Content)
	}
	if strings.Contains(resp.Content, "node_modules") {
		t.Errorf("should exclude node_modules, got:\n%s", resp.Content)
	}
	if strings.Contains(resp.Content, "bower_components") {
		t.Errorf("should exclude bower_components, got:\n%s", resp.Content)
	}
	if strings.Contains(resp.Content, ".opencode") {
		t.Errorf("should exclude .opencode, got:\n%s", resp.Content)
	}
}

func TestGrepTool_HiddenFiles(t *testing.T) {
	dir := setupGrepTestDir(t, map[string]string{
		".hidden_config": "SECRET_KEY=abc123\n",
		"visible.go":     "func visible() {}\n",
	})

	resp, err := runGrep(t, GrepParams{
		Pattern:    "SECRET_KEY",
		Path:       dir,
		OutputMode: "files_with_matches",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With --hidden flag, rg should find hidden files
	if !strings.Contains(resp.Content, ".hidden_config") {
		t.Errorf("--hidden should find dotfiles, got:\n%s", resp.Content)
	}
}

func TestGrepTool_RelativePaths(t *testing.T) {
	dir := setupGrepTestDir(t, map[string]string{
		"src/main.go": "func main() {}",
	})

	t.Run("files mode returns relative paths", func(t *testing.T) {
		resp, err := runGrep(t, GrepParams{
			Pattern:    "func",
			Path:       dir,
			OutputMode: "files_with_matches",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(resp.Content, dir) {
			t.Errorf("should use relative paths, found absolute path in:\n%s", resp.Content)
		}
		if !strings.Contains(resp.Content, "src/main.go") {
			t.Errorf("should contain relative path src/main.go, got:\n%s", resp.Content)
		}
	})

	t.Run("content mode returns relative paths", func(t *testing.T) {
		resp, err := runGrep(t, GrepParams{
			Pattern:    "func",
			Path:       dir,
			OutputMode: "content",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(resp.Content, dir) {
			t.Errorf("should use relative paths, found absolute path in:\n%s", resp.Content)
		}
	})

	t.Run("count mode returns relative paths", func(t *testing.T) {
		resp, err := runGrep(t, GrepParams{
			Pattern:    "func",
			Path:       dir,
			OutputMode: "count",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(resp.Content, dir) {
			t.Errorf("should use relative paths, found absolute path in:\n%s", resp.Content)
		}
	})
}

func TestGrepTool_ContentModeMatchCount(t *testing.T) {
	// Context lines containing colons should NOT be counted as matches.
	// This verifies the null-separator based counting is correct.
	dir := setupGrepTestDir(t, map[string]string{
		"code.go": "url: http://example.com\ntarget_match\nanother: colon line\n",
	})

	ctx := 1
	resp, err := runGrep(t, GrepParams{
		Pattern:    "target_match",
		Path:       dir,
		OutputMode: "content",
		Context:    &ctx,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The context lines "url: http://example.com" and "another: colon line"
	// both contain colons but should not be counted as matches.
	// Only "target_match" is a match.
	if !strings.Contains(resp.Content, "target_match") {
		t.Errorf("should contain the match, got:\n%s", resp.Content)
	}
	// Verify context lines are present (proving they were included)
	if !strings.Contains(resp.Content, "url:") {
		t.Errorf("should contain context line with colon, got:\n%s", resp.Content)
	}
}

func TestGrepTool_OffsetPastEnd(t *testing.T) {
	dir := setupGrepTestDir(t, map[string]string{
		"a.go": "func a() {}",
		"b.go": "func b() {}",
	})

	t.Run("files mode offset past end", func(t *testing.T) {
		resp, err := runGrep(t, GrepParams{
			Pattern:    "func",
			Path:       dir,
			OutputMode: "files_with_matches",
			Offset:     100,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should report total files found but show no file entries
		if !strings.Contains(resp.Content, "Found 2 files") {
			t.Errorf("should report total found, got:\n%s", resp.Content)
		}
	})

	t.Run("count mode offset past end", func(t *testing.T) {
		resp, err := runGrep(t, GrepParams{
			Pattern:    "func",
			Path:       dir,
			OutputMode: "count",
			Offset:     100,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should still report totals
		if !strings.Contains(resp.Content, "2 total matches") {
			t.Errorf("should report total matches, got:\n%s", resp.Content)
		}
	})

	t.Run("content mode offset past end", func(t *testing.T) {
		resp, err := runGrep(t, GrepParams{
			Pattern:    "func",
			Path:       dir,
			OutputMode: "content",
			Offset:     100,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should show pagination info
		if !strings.Contains(resp.Content, "Showing") {
			t.Errorf("should show pagination info, got:\n%s", resp.Content)
		}
	})
}

func TestPaginate(t *testing.T) {
	tests := []struct {
		name      string
		total     int
		offset    int
		headLimit int
		wantStart int
		wantEnd   int
		wantTrunc bool
	}{
		{
			name:  "no offset no limit",
			total: 10, offset: 0, headLimit: 0,
			wantStart: 0, wantEnd: 10, wantTrunc: false,
		},
		{
			name:  "limit smaller than total",
			total: 10, offset: 0, headLimit: 5,
			wantStart: 0, wantEnd: 5, wantTrunc: true,
		},
		{
			name:  "limit larger than total",
			total: 3, offset: 0, headLimit: 10,
			wantStart: 0, wantEnd: 3, wantTrunc: false,
		},
		{
			name:  "offset within range",
			total: 10, offset: 3, headLimit: 0,
			wantStart: 3, wantEnd: 10, wantTrunc: false,
		},
		{
			name:  "offset plus limit",
			total: 10, offset: 3, headLimit: 4,
			wantStart: 3, wantEnd: 7, wantTrunc: true,
		},
		{
			name:  "offset at end",
			total: 5, offset: 5, headLimit: 10,
			wantStart: 5, wantEnd: 5, wantTrunc: false,
		},
		{
			name:  "offset past end",
			total: 5, offset: 100, headLimit: 10,
			wantStart: 5, wantEnd: 5, wantTrunc: false,
		},
		{
			name:  "zero total",
			total: 0, offset: 0, headLimit: 10,
			wantStart: 0, wantEnd: 0, wantTrunc: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pg := paginate(tt.total, tt.offset, tt.headLimit)
			if pg.start != tt.wantStart {
				t.Errorf("start = %d, want %d", pg.start, tt.wantStart)
			}
			if pg.end != tt.wantEnd {
				t.Errorf("end = %d, want %d", pg.end, tt.wantEnd)
			}
			if pg.truncated != tt.wantTrunc {
				t.Errorf("truncated = %v, want %v", pg.truncated, tt.wantTrunc)
			}
			if pg.total != tt.total {
				t.Errorf("total = %d, want %d", pg.total, tt.total)
			}
		})
	}
}

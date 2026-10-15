package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/fileutil"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/permission"
)

// permRegistry evaluates read permission with the production chain
// (permission.EvaluateReadToolPermission) against a fixed global permission
// map, so what the tools see is what a configured `permission.read` does.
type permRegistry struct {
	agentregistry.Registry // nil — only the read evaluators are called
	perms                  map[string]any
}

func (r permRegistry) EvaluateReadPermission(_, toolName, input string) permission.Action {
	return permission.EvaluateReadToolPermission(toolName, input, nil, r.perms)
}

func (r permRegistry) ReadDenyPatterns(_, toolName string) []string {
	return permission.ReadDenyPatterns(toolName, nil, r.perms)
}

// noLSP is an LSP service with nothing to report.
type noLSP struct{ lsp.LspService }

func (noLSP) NotifyOpenFile(context.Context, string) {}
func (noLSP) FormatDiagnostics(string) string        { return "" }

// rejectingPermissions refuses every prompt: an "ask" verdict never passes.
type rejectingPermissions struct{ permission.Service }

func (rejectingPermissions) Request(context.Context, permission.CreatePermissionRequest) bool {
	return false
}

type readFixture struct {
	base      string // holds allowed/, workspace/ and outside/
	allowed   string // the only directory the read patterns allow
	sub       string // allowed/sub: a directory `allowed/**` matches
	okFile    string
	okImage   string
	outFile   string // flow-args.json in a fake workspace outside allowed/
	reg       agentregistry.Registry
	perms     permission.Service
	fileLink  string // symlink in allowed/sub -> outside file
	imageLink string // symlink in allowed/sub -> outside image
	dirLink   string // symlink in allowed/sub -> outside dir
}

var tinyPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")

// newReadFixture builds the directory layout under a temp dir that
// ripgrep-based glob does not hide (glob skips paths with a "tmp" component).
func newReadFixture(t *testing.T) *readFixture {
	t.Helper()
	base := t.TempDir()
	if fileutil.SkipHidden(base) {
		dir, err := os.MkdirTemp(".", "readperm-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		if base, err = filepath.Abs(dir); err != nil {
			t.Fatal(err)
		}
	}
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	write := func(p string, data []byte) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	f := &readFixture{base: base, allowed: filepath.Join(base, "allowed")}
	f.sub = filepath.Join(f.allowed, "sub")
	f.okFile = write(filepath.Join(f.sub, "ok.txt"), []byte("hello needle\n"))
	f.okImage = write(filepath.Join(f.sub, "pic.png"), tinyPNG)
	f.outFile = write(filepath.Join(base, "workspace", "flow-args.json"), []byte(`{"token":"needle"}`))
	outImage := write(filepath.Join(base, "outside", "secret.png"), tinyPNG)
	write(filepath.Join(base, "outside", "home.json"), []byte(`{"apiKey":"needle"}`))

	f.fileLink = filepath.Join(f.sub, "link.json")
	f.imageLink = filepath.Join(f.sub, "link.png")
	f.dirLink = filepath.Join(f.sub, "dirlink")
	for link, target := range map[string]string{
		f.fileLink:  f.outFile,
		f.imageLink: outImage,
		f.dirLink:   filepath.Join(base, "outside"),
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}

	f.reg = permRegistry{perms: map[string]any{"read": map[string]any{"*": "deny", f.allowed + "/**": "allow"}}}
	f.perms = rejectingPermissions{}
	return f
}

type readToolCase struct {
	name string
	tool BaseTool
	// input builds the tool input for path.
	input func(path string) string
}

func (f *readFixture) tools() []readToolCase {
	in := func(key string) func(string) string {
		return func(p string) string {
			b, _ := json.Marshal(map[string]string{key: p, "pattern": "needle"})
			return string(b)
		}
	}
	globIn := func(p string) string {
		b, _ := json.Marshal(map[string]string{"path": p, "pattern": "**/*"})
		return string(b)
	}
	return []readToolCase{
		{"read", NewReadTool(noLSP{}, f.reg, f.perms), in("file_path")},
		{"view_image", NewViewImageTool(f.reg, f.perms), in("file_path")},
		{"grep", NewGrepTool(f.reg, f.perms), in("path")},
		{"glob", NewGlobTool(f.reg, f.perms), globIn},
		{"ls", NewLsTool(config.Get(), f.reg, f.perms), in("path")},
	}
}

// With permission.read = {"*": deny, "<allowed>/**": allow}, none of the five
// read-category tools reaches a path outside allowed/ however it is spelled.
func TestReadToolsDenyPathsThatLeaveTheAllowedDirectory(t *testing.T) {
	f := newReadFixture(t)
	rel, err := filepath.Rel(config.WorkingDirectory(), f.outFile)
	if err != nil {
		t.Fatal(err)
	}
	paths := []struct{ name, path string }{
		{"double slash", "//proc/self/environ"},
		{"dot segment", "/./proc/self/environ"},
		{"dot in a workspace path", filepath.Join(f.base, "workspace") + "/./flow-args.json"},
		{"dotdot in a workspace path", filepath.Join(f.base, "workspace") + "/../workspace/flow-args.json"},
		{"dotdot out of the allowed directory", f.allowed + "/../workspace/flow-args.json"},
		{"relative path resolving outside", rel},
		{"file symlink to outside", f.fileLink},
		{"image symlink to outside", f.imageLink},
		{"directory symlink to outside", f.dirLink},
		{"file under a directory symlink", filepath.Join(f.dirLink, "home.json")},
		{"missing path inside allowed", filepath.Join(f.sub, "missing.txt")},
	}
	for _, tc := range f.tools() {
		for _, p := range paths {
			t.Run(tc.name+"/"+p.name, func(t *testing.T) {
				resp, err := tc.tool.Run(context.Background(), ToolCall{ID: "c", Name: tc.name, Input: tc.input(p.path)})
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if !resp.IsError || !strings.Contains(resp.Content, "Permission denied") {
					t.Errorf("not denied: IsError=%v content=%q", resp.IsError, resp.Content)
				}
				if strings.Contains(resp.Content, "needle") || strings.Contains(resp.Content, `"type":"image"`) {
					t.Errorf("response carries file content: %q", resp.Content)
				}
			})
		}
	}
}

// What the patterns allow still works through the canonical path, including a
// path spelled with redundant segments that stays inside allowed/.
func TestReadToolsServeTheAllowedDirectory(t *testing.T) {
	f := newReadFixture(t)
	messy := func(p string) string { return filepath.Dir(p) + "/./../sub/" + filepath.Base(p) }
	run := func(tc readToolCase, path string) ToolResponse {
		t.Helper()
		resp, err := tc.tool.Run(context.Background(), ToolCall{ID: "c", Name: tc.name, Input: tc.input(path)})
		if err != nil {
			t.Fatalf("%s: Run: %v", tc.name, err)
		}
		return resp
	}
	for _, tc := range f.tools() {
		switch tc.name {
		case "read":
			for _, p := range []string{f.okFile, messy(f.okFile)} {
				if resp := run(tc, p); resp.IsError || !strings.Contains(resp.Content, "hello needle") {
					t.Errorf("read %s: %+v", p, resp)
				}
			}
		case "view_image":
			for _, p := range []string{f.okImage, messy(f.okImage)} {
				if resp := run(tc, p); resp.IsError || resp.Type != ToolResponseTypeImage {
					t.Errorf("view_image %s: %+v", p, resp)
				}
			}
		case "ls":
			if resp := run(tc, f.sub); resp.IsError || !strings.Contains(resp.Content, "ok.txt") {
				t.Errorf("ls: %+v", resp)
			}
		case "grep":
			resp := run(tc, f.sub)
			if resp.IsError || !strings.Contains(resp.Content, "ok.txt") {
				t.Errorf("grep: %+v", resp)
			}
			if strings.Contains(resp.Content, "link.json") || strings.Contains(resp.Content, "home.json") {
				t.Errorf("grep returned a file outside allowed/: %q", resp.Content)
			}
		case "glob":
			resp := run(tc, f.sub)
			if resp.IsError || !strings.Contains(resp.Content, "ok.txt") {
				t.Errorf("glob: %+v", resp)
			}
			// rg runs with -L, so it lists through symlinks; the filter
			// drops every match that resolves outside allowed/.
			for _, leaked := range []string{"link.json", "link.png", "secret.png", "home.json", "flow-args.json"} {
				if strings.Contains(resp.Content, leaked) {
					t.Errorf("glob returned %s, which resolves outside allowed/: %q", leaked, resp.Content)
				}
			}
		}
	}
}

// The Go fallback walks a symlinked file by opening it; its matches are
// filtered on the resolved path too.
func TestGrepRegexFallbackDropsSymlinkedMatchesOutsideAllowed(t *testing.T) {
	f := newReadFixture(t)
	readable := readableMatch(context.Background(), f.reg, f.perms, GrepToolName, f.sub)
	if readable == nil {
		t.Fatal("no read filter with a registry configured")
	}
	out, _, err := searchWithRegexFallback(context.Background(), "needle", f.sub, "", "files_with_matches", 250, 0, nil, readable)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ok.txt") {
		t.Errorf("fallback lost the allowed match: %q", out)
	}
	for _, leaked := range []string{"link.json", "dirlink"} {
		if strings.Contains(out, leaked) {
			t.Errorf("fallback returned %s: %q", leaked, out)
		}
	}
	unfiltered, _, err := searchWithRegexFallback(context.Background(), "needle", f.sub, "", "files_with_matches", 250, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unfiltered, "link.json") {
		t.Fatalf("fixture invalid: the unfiltered walk should reach the symlinked file: %q", unfiltered)
	}
}

func TestFilterReadableOutput(t *testing.T) {
	readable := func(p string) bool { return strings.HasPrefix(p, "/ok/") }
	tests := []struct {
		name, mode, raw, want string
	}{
		{"files", "files_with_matches", "/ok/a\n/bad/b\n/ok/c", "/ok/a\n/ok/c"},
		{"count", "count", "/ok/a:2\n/bad/b:1", "/ok/a:2"},
		{
			"content with context before the first match",
			"content",
			"/bad/b\x1e1\x1ectx\n/bad/b\x1f2\x1fneedle\n--\n/ok/a\x1e4\x1ectx\n/ok/a\x1f5\x1fneedle",
			"/ok/a\x1e4\x1ectx\n/ok/a\x1f5\x1fneedle",
		},
		{"a line without a separator is dropped", "content", "stray\n/ok/a\x1f5\x1fneedle", "/ok/a\x1f5\x1fneedle"},
		{
			"context of a file whose name extends an allowed one is dropped",
			"content",
			"/ok/a\x1f1\x1fneedle\n/ok/a\x1e2\x1ectx\n--\n/bad/a-secret\x1e3\x1ectx\n/bad/a-secret\x1f4\x1fneedle",
			"/ok/a\x1f1\x1fneedle\n/ok/a\x1e2\x1ectx",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filterReadableOutput(tt.raw, tt.mode, readable); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// Without a registry nothing is checked and paths are used as given, so the
// tools keep their behaviour outside a permission-configured run.
func TestAuthorizeReadWithoutRegistryChangesNothing(t *testing.T) {
	got, err := authorizeRead(context.Background(), nil, nil, ReadToolName, "/nonexistent/../x")
	if err != nil || got != "/nonexistent/../x" {
		t.Errorf("authorizeRead = %q, %v; want the path untouched", got, err)
	}
}

// A symlink named by a deny rule stays denied even when its target is allowed.
func TestAuthorizeReadHonoursRulesWrittenAgainstTheSymlinkName(t *testing.T) {
	f := newReadFixture(t)
	link := filepath.Join(f.sub, "alias.txt")
	if err := os.Symlink(f.okFile, link); err != nil {
		t.Skip(err)
	}
	reg := permRegistry{perms: map[string]any{"read": map[string]any{"*": "deny", f.allowed + "/**": "allow", link: "deny"}}}
	if _, err := authorizeRead(context.Background(), reg, f.perms, ReadToolName, link); err != permission.ErrorPermissionDenied {
		t.Errorf("authorizeRead(%s) = %v, want denied by the rule on the link's own name", link, err)
	}
	if got, err := authorizeRead(context.Background(), reg, f.perms, ReadToolName, f.okFile); err != nil || got != f.okFile {
		t.Errorf("authorizeRead(target) = %q, %v; want allowed", got, err)
	}
}

// A context line of /w/a-secret that follows a match of /w/a must not be taken
// for a context line of /w/a: the file is read from the separator, never from
// the first dash of the line.
func TestFilterReadableOutputContextOfDashedSiblingIsDropped(t *testing.T) {
	readable := func(p string) bool { return p == "/w/a" }
	raw := "/w/a\x1f1\x1fneedle\n/w/a-secret\x1e1\x1esecret text\n/w/a-secret\x1f2\x1fneedle"
	got := filterReadableOutput(raw, "content", readable)
	if got != "/w/a\x1f1\x1fneedle" {
		t.Errorf("got %q, want only the allowed file's match line", got)
	}
	if strings.Contains(got, "secret") {
		t.Errorf("the denied file's context line leaked: %q", got)
	}
}

// End to end through ripgrep: a denied sibling whose name extends an allowed
// file's name never shows up in -C output, and the allowed file's context is
// still rendered with the usual ":" and "-" separators.
func TestGrepContentContextDoesNotLeakDashedDeniedSibling(t *testing.T) {
	if !isRipgrepAvailable() {
		t.Skip("ripgrep not available")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	allowedFile := filepath.Join(dir, "a")
	deniedFile := filepath.Join(dir, "a-secret")
	if err := os.WriteFile(allowedFile, []byte("before\nneedle\nafter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deniedFile, []byte("TOPSECRET-before\nneedle\nTOPSECRET-after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readable := func(p string) bool { return p == allowedFile }
	ctxLines := 1
	out, meta, err := searchWithRipgrepModes(context.Background(), "needle", dir,
		&GrepParams{Context: &ctxLines}, "", "content", 250, nil, readable)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "TOPSECRET") || strings.Contains(out, "a-secret") {
		t.Errorf("denied sibling leaked into the output: %q", out)
	}
	for _, want := range []string{"a-1-before", "a:2:needle", "a-3-after"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lost %q: %q", want, out)
		}
	}
	if strings.ContainsAny(out, "\x1e\x1f") {
		t.Errorf("internal separators left in the output: %q", out)
	}
	if meta.NumberOfMatches != 1 || meta.NumberOfFiles != 1 {
		t.Errorf("metadata = %+v, want 1 match in 1 file", meta)
	}
}

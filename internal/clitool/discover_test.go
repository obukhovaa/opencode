package clitool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
)

// isolateHome points the global manifest directories (~/.config/opencode/tools,
// ~/.agents/tools) at an empty home so a developer's own manifests cannot leak
// into the discovery assertions.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	clearLimitEnv(t)
}

func TestDiscover_PrecedenceShadowingAndDiagnostics(t *testing.T) {
	isolateHome(t)
	wd := t.TempDir()
	// The working dir is its own worktree root so the walk stops here.
	if err := os.Mkdir(filepath.Join(wd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, wd, ".opencode/tools/say.yaml", "name: say\ndescription: from .opencode\ncommand: /bin/echo\n")
	writeManifest(t, wd, ".agents/tools/say.yaml", "name: say\ndescription: from .agents\ncommand: /bin/echo\n")
	writeManifest(t, wd, ".agents/tools/broken.yaml", "name: broken\ndescription: d\ncommand: /bin/echo\nargs:\n  alow: [x]\n")
	writeManifest(t, wd, ".agents/tools/README.md", "# not a manifest\n")
	extra := t.TempDir()
	writeManifest(t, extra, "extra.yml", "name: extra\ndescription: d\ncommand: /bin/echo\n")

	set := Discover(context.Background(), wd, &config.CLIToolsConfig{Paths: []string{extra}})
	names := []string{}
	for _, m := range set.Manifests {
		names = append(names, m.Name)
	}
	if strings.Join(names, ",") != "extra,say" {
		t.Fatalf("tools = %v", names)
	}
	if set.Manifests[1].Description != "from .opencode" {
		t.Errorf(".opencode/tools must win over .agents/tools, got %q", set.Manifests[1].Description)
	}
	var shadowed, invalid int
	for _, d := range set.Diagnostics {
		if d.Shadowed {
			shadowed++
			if !strings.HasSuffix(d.Path, filepath.Join(".agents", "tools", "say.yaml")) {
				t.Errorf("shadowed path = %s", d.Path)
			}
		} else {
			invalid++
			if !strings.Contains(d.Reason, "alow") {
				t.Errorf("invalid reason = %s", d.Reason)
			}
		}
	}
	if shadowed != 1 || invalid != 1 {
		t.Errorf("diagnostics: shadowed=%d invalid=%d (%+v)", shadowed, invalid, set.Diagnostics)
	}
}

func TestDiscover_Disabled(t *testing.T) {
	isolateHome(t)
	wd := t.TempDir()
	writeManifest(t, wd, ".agents/tools/say.yaml", echoManifest)
	set := Discover(context.Background(), wd, &config.CLIToolsConfig{Disabled: true})
	if !set.Disabled || len(set.Manifests) != 0 {
		t.Errorf("config disabled: %+v", set)
	}
	t.Setenv(DisableEnv, "TRUE")
	set = Discover(context.Background(), wd, nil)
	if !set.Disabled || len(set.Manifests) != 0 {
		t.Errorf("env disabled: %+v", set)
	}
}

func TestDiscover_HelpCapture(t *testing.T) {
	isolateHome(t)
	wd := t.TempDir()
	script := writeScript(t, wd, "h.sh", `echo "usage: h [--flag]"; exit 1`)
	writeManifest(t, wd, ".agents/tools/h.yaml", "name: h\ndescription: d\ncommand: "+script+"\nhelp:\n  args: [\"--help\"]\n  maxBytes: 12\n")
	set := Discover(context.Background(), wd, nil)
	if len(set.Manifests) != 1 {
		t.Fatalf("tools = %d (%+v)", len(set.Manifests), set.Diagnostics)
	}
	m := set.Manifests[0]
	if !strings.HasPrefix(m.HelpText, "usage: h [--") || !strings.Contains(m.HelpText, "truncated at 12 bytes") {
		t.Errorf("help text = %q", m.HelpText)
	}
	if !strings.Contains(m.FullDescription(), "Help output of the installed binary") {
		t.Error("help block missing from description")
	}
}

func TestDiscover_WalksUpToWorktreeRoot(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, root, ".agents/tools/up.yaml", "name: up\ndescription: d\ncommand: /bin/echo\n")
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	set := Discover(context.Background(), nested, nil)
	if len(set.Manifests) != 1 || set.Manifests[0].Name != "up" {
		t.Errorf("expected the root manifest from a nested working dir, got %+v", set.Manifests)
	}
}

// A relative working directory (`opencode tools serve --cwd .`, the form the
// docs' .mcp.json example uses) must neither break cwd confinement nor leave
// relative paths in the result.
func TestDiscover_RelativeWorkingDir(t *testing.T) {
	isolateHome(t)
	wd := t.TempDir()
	for _, d := range []string{".git", "sub"} {
		if err := os.Mkdir(filepath.Join(wd, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest(t, wd, ".agents/tools/say.yaml", "name: say\ndescription: d\ncommand: /bin/echo\ncwd: sub\n")
	t.Chdir(wd)
	set := Discover(context.Background(), ".", nil)
	if len(set.Manifests) != 1 {
		t.Fatalf("tools = %d (%+v)", len(set.Manifests), set.Diagnostics)
	}
	m := set.Manifests[0]
	if !filepath.IsAbs(m.WorkingDir) || !filepath.IsAbs(m.Location) || !filepath.IsAbs(m.ResolvedCwd) {
		t.Errorf("paths must be absolute: workingDir=%q location=%q cwd=%q", m.WorkingDir, m.Location, m.ResolvedCwd)
	}
	if filepath.Base(m.ResolvedCwd) != "sub" {
		t.Errorf("ResolvedCwd = %q, want .../sub", m.ResolvedCwd)
	}
	for _, d := range set.Dirs {
		if !filepath.IsAbs(d) {
			t.Errorf("scanned dir %q is not absolute", d)
		}
	}
}

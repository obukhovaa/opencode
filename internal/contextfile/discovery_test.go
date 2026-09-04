package contextfile

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFileAt(t *testing.T, root, rel, content string) string {
	t.Helper()
	full := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	return full
}

func enabledDiscovery() DiscoveryConfig {
	return DefaultDiscoveryConfig()
}

func TestDiscover(t *testing.T) {
	t.Parallel()

	globalPaths := []string{"AGENTS.md", "CLAUDE.md"}

	t.Run("finds nested files but not the root file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeFileAt(t, root, "AGENTS.md", "root")
		nested := writeFileAt(t, root, "services/auth/AGENTS.md", "auth")

		res := Discover(root, globalPaths, enabledDiscovery())
		assert.Equal(t, []string{nested}, res.Files)
		assert.False(t, res.Truncated)
	})

	t.Run("skips hidden and hardcoded directories", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeFileAt(t, root, ".git/sub/AGENTS.md", "git")
		writeFileAt(t, root, ".opencode/skills/AGENTS.md", "data-dir")
		writeFileAt(t, root, "node_modules/pkg/AGENTS.md", "npm")
		writeFileAt(t, root, "vendor/dep/AGENTS.md", "vendored")
		kept := writeFileAt(t, root, "src/AGENTS.md", "kept")

		res := Discover(root, globalPaths, enabledDiscovery())
		assert.Equal(t, []string{kept}, res.Files)
	})

	t.Run("files under a trailing-slash contextPaths entry stay inline", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeFileAt(t, root, "rules/AGENTS.md", "inlined by the rules/ entry")
		kept := writeFileAt(t, root, "src/AGENTS.md", "kept")

		res := Discover(root, []string{"AGENTS.md", "rules/"}, enabledDiscovery())
		assert.Equal(t, []string{kept}, res.Files)
	})

	t.Run("respects maxDepth", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		shallow := writeFileAt(t, root, "a/b/AGENTS.md", "depth 2")
		writeFileAt(t, root, "a/b/c/AGENTS.md", "depth 3")

		cfg := enabledDiscovery()
		cfg.MaxDepth = 2
		res := Discover(root, globalPaths, cfg)
		assert.Equal(t, []string{shallow}, res.Files)
	})

	t.Run("respects maxFiles and reports truncation", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		for _, d := range []string{"a", "b", "c", "d"} {
			writeFileAt(t, root, filepath.Join(d, "AGENTS.md"), d)
		}

		cfg := enabledDiscovery()
		cfg.MaxFiles = 2
		res := Discover(root, globalPaths, cfg)
		assert.Len(t, res.Files, 2)
		assert.True(t, res.Truncated)
	})

	t.Run("result is cached per workDir", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		first := writeFileAt(t, root, "one/AGENTS.md", "one")

		res := Discover(root, globalPaths, enabledDiscovery())
		require.Equal(t, []string{first}, res.Files)

		// A file created after the first walk must not appear: the walk
		// runs once per process per workDir.
		writeFileAt(t, root, "two/AGENTS.md", "two")
		res = Discover(root, globalPaths, enabledDiscovery())
		assert.Equal(t, []string{first}, res.Files)
	})

	t.Run("disabled discovery returns an empty result", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeFileAt(t, root, "sub/AGENTS.md", "nested")

		res := Discover(root, globalPaths, DiscoveryConfig{Enabled: false})
		assert.Empty(t, res.Files)
		assert.False(t, res.Truncated)
	})

	t.Run("symlink candidate is rejected even when it points outside workDir", func(t *testing.T) {
		t.Parallel()
		outside := t.TempDir()
		secret := filepath.Join(outside, "id_rsa")
		require.NoError(t, os.WriteFile(secret, []byte("PRIVATE KEY MATERIAL"), 0o600))

		root := t.TempDir()
		kept := writeFileAt(t, root, "src/AGENTS.md", "kept")
		require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
		require.NoError(t, os.Symlink(secret, filepath.Join(root, "docs", "AGENTS.md")))
		// A symlink pointing INSIDE workDir is rejected too: candidates
		// must be regular files, full stop.
		require.NoError(t, os.MkdirAll(filepath.Join(root, "alias"), 0o755))
		require.NoError(t, os.Symlink(kept, filepath.Join(root, "alias", "AGENTS.md")))

		res := Discover(root, globalPaths, enabledDiscovery())
		assert.Equal(t, []string{kept}, res.Files, "symlinked candidates must never enter the discovery set")
	})

	t.Run("labels are computed at discovery time", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		withDesc := writeFileAt(t, root, "services/auth/AGENTS.md",
			"---\ndescription: Auth conventions\n---\nbody")
		plain := writeFileAt(t, root, "services/misc/AGENTS.md", "just prose")

		res := Discover(root, globalPaths, enabledDiscovery())
		require.ElementsMatch(t, []string{withDesc, plain}, res.Files)
		assert.Equal(t, "Auth conventions", res.Labels[withDesc])
		_, hasPlain := res.Labels[plain]
		assert.False(t, hasPlain, "label-less files carry no map entry")
	})

	t.Run("configured non-hidden data directory is skipped", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeFileAt(t, root, "opencode-data/skills/AGENTS.md", "data dir")
		kept := writeFileAt(t, root, "src/AGENTS.md", "kept")

		cfg := enabledDiscovery()
		cfg.SkipDirs = []string{"opencode-data"}
		res := Discover(root, globalPaths, cfg)
		assert.Equal(t, []string{kept}, res.Files, "the configured data directory must not be walked")
	})
}

// labelsFor extracts labels the way the discovery walk does, so manifest
// tests exercise the same label logic without going through Discover.
func labelsFor(t *testing.T, files ...string) map[string]string {
	t.Helper()
	labels := make(map[string]string)
	for _, path := range files {
		f, err := os.Open(path)
		require.NoError(t, err)
		if label := extractLabel(f); label != "" {
			labels[path] = label
		}
		f.Close()
	}
	return labels
}

// fsFoldsCase reports whether dir's filesystem treats names
// case-insensitively, by writing a lowercase probe entry and statting an
// uppercase spelling of it. Tests use it to pick the arm that applies to
// the machine they run on (macOS/Windows default: folds; Linux: doesn't),
// so CI on each platform covers one arm.
func fsFoldsCase(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "casefold_probe")
	require.NoError(t, os.WriteFile(probe, []byte("x"), 0o644))
	a, err := os.Stat(probe)
	require.NoError(t, err)
	b, err := os.Stat(filepath.Join(dir, "CASEFOLD_PROBE"))
	if err != nil {
		return false
	}
	return os.SameFile(a, b)
}

func TestRenderManifest(t *testing.T) {
	t.Parallel()

	t.Run("absent when nothing was discovered", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, RenderManifest(nil, nil, t.TempDir(), ManifestConfig{}))
	})

	t.Run("lists relative paths with labels", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		withFrontmatter := writeFileAt(t, root, "services/auth/AGENTS.md",
			"---\nname: auth\ndescription: Auth service conventions\n---\n\n# Ignored heading\nbody")
		withHeading := writeFileAt(t, root, "services/billing/AGENTS.md",
			"# Billing rules\nbody")
		plain := writeFileAt(t, root, "services/misc/AGENTS.md", "just prose, no heading")
		files := []string{withFrontmatter, withHeading, plain}

		got := RenderManifest(files, labelsFor(t, files...), root, ManifestConfig{})
		assert.Contains(t, got, "# Nested Context Files")
		assert.Contains(t, got, "NOT loaded into this prompt")
		assert.Contains(t, got, "- services/auth/AGENTS.md: Auth service conventions")
		assert.Contains(t, got, "- services/billing/AGENTS.md: Billing rules")
		assert.True(t, strings.HasSuffix(got, "- services/misc/AGENTS.md"), "a label-less file gets a path-only line: %q", got)
		assert.NotContains(t, got, "Ignored heading", "frontmatter description wins over the heading")
	})

	t.Run("byte-stable even when the file changes after discovery", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		f := writeFileAt(t, root, "sub/AGENTS.md", "# Sub rules\nbody")

		// The labels ride the cached discovery result, so stability is
		// structural: mutating the file between renders CANNOT change the
		// manifest — RenderManifest never reads the disk.
		res := Discover(root, []string{"AGENTS.md"}, enabledDiscovery())
		require.Equal(t, []string{f}, res.Files)
		first := RenderManifest(res.Files, res.Labels, root, ManifestConfig{})
		require.NoError(t, os.WriteFile(f, []byte("# CHANGED heading\nbody"), 0o644))
		second := RenderManifest(res.Files, res.Labels, root, ManifestConfig{})
		assert.Equal(t, first, second)
		assert.Contains(t, second, "Sub rules")
		assert.NotContains(t, second, "CHANGED")
	})

	t.Run("walk truncation is noted in the header", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		f := writeFileAt(t, root, "sub/AGENTS.md", "body")

		got := RenderManifest([]string{f}, nil, root, ManifestConfig{WalkTruncated: true})
		assert.Contains(t, got, "truncated")
	})

	t.Run("overflow degrades to paths-only then a summary line", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		files := make([]string, 0, 8)
		for _, d := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
			files = append(files, writeFileAt(t, root, filepath.Join(d, "AGENTS.md"),
				"# "+strings.Repeat(d, 60)+"\nbody"))
		}
		labels := labelsFor(t, files...)

		labeled := RenderManifest(files, labels, root, ManifestConfig{})
		require.Contains(t, labeled, "aaaa", "sanity: labels render when the cap allows")

		pathsOnly := RenderManifest(files, labels, root, ManifestConfig{MaxBytes: len(labeled) - 1})
		assert.NotContains(t, pathsOnly, "aaaa", "over the cap the labels are dropped first")
		assert.Contains(t, pathsOnly, "- a/AGENTS.md")
		assert.LessOrEqual(t, len(pathsOnly), len(labeled)-1)

		tiny := RenderManifest(files, labels, root, ManifestConfig{MaxBytes: len(pathsOnly) - 1})
		assert.Contains(t, tiny, "more files not shown")
		assert.LessOrEqual(t, len(tiny), len(pathsOnly)-1)
	})
}

func TestOwnersForPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	servicesFile := filepath.Join(root, "services", "AGENTS.md")
	servicesClaude := filepath.Join(root, "services", "CLAUDE.md")
	authFile := filepath.Join(root, "services", "auth", "AGENTS.md")
	billingFile := filepath.Join(root, "services", "billing", "AGENTS.md")
	discovered := []string{servicesFile, servicesClaude, authFile, billingFile}

	tests := []struct {
		name string
		dir  string
		want []string
	}{
		{
			name: "target dir equal to an owning dir",
			dir:  filepath.Join(root, "services", "auth"),
			want: []string{servicesFile, servicesClaude, authFile},
		},
		{
			name: "target dir strictly inside an owning dir, outermost first",
			dir:  filepath.Join(root, "services", "auth", "internal"),
			want: []string{servicesFile, servicesClaude, authFile},
		},
		{
			name: "sibling subtree owners are not collected",
			dir:  filepath.Join(root, "services", "billing"),
			want: []string{servicesFile, servicesClaude, billingFile},
		},
		{
			name: "workDir itself owns nothing",
			dir:  root,
			want: nil,
		},
		{
			name: "dir outside workDir owns nothing",
			dir:  filepath.Join(filepath.Dir(root), "elsewhere"),
			want: nil,
		},
		{
			name: "dir on an ownerless path owns nothing",
			dir:  filepath.Join(root, "docs"),
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, OwnersForPath(tt.dir, discovered, root))
		})
	}

	t.Run("mixed-case target dir still matches its owners on a case-insensitive fs", func(t *testing.T) {
		if !fsFoldsCase(t, root) {
			t.Skipf("filesystem is case-sensitive; the folding arm does not apply here")
		}
		// The model-supplied path can differ in case from the WalkDir
		// spelling on the default macOS/Windows filesystems; matching
		// must fold case there — and ONLY there.
		mixed := filepath.Join(root, "Services", "AUTH")
		assert.Equal(t, []string{servicesFile, servicesClaude, authFile}, OwnersForPath(mixed, discovered, root))
	})
}

// TestOwnersForPath_CaseSensitiveFS pins the other arm: on a
// case-sensitive filesystem (Linux production), two sibling directories
// differing only by case are DIFFERENT trees, and folding their names
// would inject the wrong tree's context (and mark it injected). Skipped
// where the filesystem folds case.
func TestOwnersForPath_CaseSensitiveFS(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Services"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "services"), 0o755))
	a, err := os.Stat(filepath.Join(root, "Services"))
	require.NoError(t, err)
	b, err := os.Stat(filepath.Join(root, "services"))
	require.NoError(t, err)
	if os.SameFile(a, b) {
		t.Skipf("filesystem folds case; the sibling-directory arm does not apply here")
	}
	owner := filepath.Join(root, "Services", "AGENTS.md")
	require.NoError(t, os.WriteFile(owner, []byte("x"), 0o644))

	assert.Nil(t, OwnersForPath(filepath.Join(root, "services"), []string{owner}, root),
		"a read into the case-differing sibling tree must not activate the other tree's context")
	assert.Equal(t, []string{owner}, OwnersForPath(filepath.Join(root, "Services"), []string{owner}, root),
		"the exact-case directory still owns its file")
}

func TestFilterDiscovered(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	authFile := writeFileAt(t, root, "services/auth/AGENTS.md", "auth")
	billingFile := writeFileAt(t, root, "services/billing/AGENTS.md", "billing")
	docsFile := writeFileAt(t, root, "docs/AGENTS.md", "docs")
	discovered := []string{docsFile, authFile, billingFile}
	vars := TemplateVars{Agent: "coder"}

	t.Run("nil layers pass the set through unchanged", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, discovered, FilterDiscovered(discovered, nil, nil, root, vars))
	})

	t.Run("exact scoped path is subtracted", func(t *testing.T) {
		t.Parallel()
		got := FilterDiscovered(discovered,
			&AgentContext{Paths: []string{"services/auth/AGENTS.md"}, Mode: "append"},
			nil, root, vars)
		assert.Equal(t, []string{docsFile, billingFile}, got)
	})

	t.Run("trailing-slash scoped subtree is subtracted", func(t *testing.T) {
		t.Parallel()
		got := FilterDiscovered(discovered,
			&AgentContext{Paths: []string{"services/"}, Mode: "append"},
			nil, root, vars)
		assert.Equal(t, []string{docsFile}, got)
	})

	t.Run("step subtraction applies on top of the agent layer", func(t *testing.T) {
		t.Parallel()
		got := FilterDiscovered(discovered,
			&AgentContext{Paths: []string{"services/auth/AGENTS.md"}, Mode: "append"},
			&StepContext{Paths: []string{"docs/AGENTS.md"}, Mode: "append"},
			root, vars)
		assert.Equal(t, []string{billingFile}, got)
	})

	t.Run("agent layer dropped by a step replace is NOT subtracted", func(t *testing.T) {
		t.Parallel()
		// With step mode=replace the agent layer never reaches the
		// prompt, so its named file stays a disclosure candidate — the
		// filter must mirror ResolveForAgent's layer participation.
		got := FilterDiscovered(discovered,
			&AgentContext{Paths: []string{"services/auth/AGENTS.md"}, Mode: "append"},
			&StepContext{Paths: []string{"docs/AGENTS.md"}, Mode: "replace"},
			root, vars)
		assert.Equal(t, []string{authFile, billingFile}, got)
	})

	t.Run("case-differing sibling subtree is NOT subtracted on a case-sensitive fs", func(t *testing.T) {
		t.Parallel()
		caseRoot := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(caseRoot, "docs"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(caseRoot, "Docs"), 0o755))
		a, err := os.Stat(filepath.Join(caseRoot, "docs"))
		require.NoError(t, err)
		b, err := os.Stat(filepath.Join(caseRoot, "Docs"))
		require.NoError(t, err)
		if os.SameFile(a, b) {
			t.Skipf("filesystem folds case; the sibling-directory arm does not apply here")
		}
		nested := writeFileAt(t, caseRoot, "docs/AGENTS.md", "docs body")

		got := FilterDiscovered([]string{nested},
			&AgentContext{Paths: []string{"Docs/"}, Mode: "append"},
			nil, caseRoot, vars)
		assert.Equal(t, []string{nested}, got,
			"inlining Docs/ must not silently drop the DIFFERENT docs/ tree's context")
	})

	t.Run("case-differing spelling IS subtracted on a case-insensitive fs", func(t *testing.T) {
		t.Parallel()
		caseRoot := t.TempDir()
		if !fsFoldsCase(t, caseRoot) {
			t.Skipf("filesystem is case-sensitive; the folding arm does not apply here")
		}
		nested := writeFileAt(t, caseRoot, "docs/AGENTS.md", "docs body")

		got := FilterDiscovered([]string{nested},
			&AgentContext{Paths: []string{"Docs/AGENTS.md"}, Mode: "append"},
			nil, caseRoot, vars)
		assert.Empty(t, got,
			"on a folding filesystem Docs/AGENTS.md and docs/AGENTS.md are the same file")
	})
}

// TestOpenBeneath pins the kernel-level beneath-only guarantee the
// activation read and the discovery label extraction rely on: no spelling
// of a relative path — including one raced into a symlink — can open a
// file outside workDir.
func TestOpenBeneath(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	require.NoError(t, os.WriteFile(secret, []byte("SECRET"), 0o600))

	root := t.TempDir()
	inner := writeFileAt(t, root, "sub/AGENTS.md", "inner body")

	t.Run("regular file opens and reads", func(t *testing.T) {
		t.Parallel()
		f, err := OpenBeneath(root, filepath.Join("sub", "AGENTS.md"))
		require.NoError(t, err)
		defer f.Close()
		body, err := io.ReadAll(f)
		require.NoError(t, err)
		assert.Equal(t, "inner body", string(body))
		_ = inner
	})

	t.Run("symlink at the final component is refused", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "linkleaf"), 0o755))
		require.NoError(t, os.Symlink(secret, filepath.Join(root, "linkleaf", "AGENTS.md")))
		_, err := OpenBeneath(root, filepath.Join("linkleaf", "AGENTS.md"))
		assert.Error(t, err)
	})

	t.Run("symlinked intermediate escaping the root is refused", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
		_, err := OpenBeneath(root, filepath.Join("escape", "secret.md"))
		assert.Error(t, err)
	})

	t.Run("dot-dot escape is refused", func(t *testing.T) {
		t.Parallel()
		_, err := OpenBeneath(root, filepath.Join("..", filepath.Base(outside), "secret.md"))
		assert.Error(t, err)
	})
}

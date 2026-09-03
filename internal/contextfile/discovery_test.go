package contextfile

import (
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
}

func TestRenderManifest(t *testing.T) {
	t.Parallel()

	t.Run("absent when nothing was discovered", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, RenderManifest(nil, t.TempDir(), ManifestConfig{}))
	})

	t.Run("lists relative paths with labels", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		withFrontmatter := writeFileAt(t, root, "services/auth/AGENTS.md",
			"---\nname: auth\ndescription: Auth service conventions\n---\n\n# Ignored heading\nbody")
		withHeading := writeFileAt(t, root, "services/billing/AGENTS.md",
			"# Billing rules\nbody")
		plain := writeFileAt(t, root, "services/misc/AGENTS.md", "just prose, no heading")

		got := RenderManifest([]string{withFrontmatter, withHeading, plain}, root, ManifestConfig{})
		assert.Contains(t, got, "# Nested Context Files")
		assert.Contains(t, got, "NOT loaded into this prompt")
		assert.Contains(t, got, "- services/auth/AGENTS.md: Auth service conventions")
		assert.Contains(t, got, "- services/billing/AGENTS.md: Billing rules")
		assert.True(t, strings.HasSuffix(got, "- services/misc/AGENTS.md"), "a label-less file gets a path-only line: %q", got)
		assert.NotContains(t, got, "Ignored heading", "frontmatter description wins over the heading")
	})

	t.Run("byte-stable across repeated calls", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		f := writeFileAt(t, root, "sub/AGENTS.md", "# Sub rules\nbody")

		first := RenderManifest([]string{f}, root, ManifestConfig{})
		second := RenderManifest([]string{f}, root, ManifestConfig{})
		assert.Equal(t, first, second)
	})

	t.Run("walk truncation is noted in the header", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		f := writeFileAt(t, root, "sub/AGENTS.md", "body")

		got := RenderManifest([]string{f}, root, ManifestConfig{WalkTruncated: true})
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

		labeled := RenderManifest(files, root, ManifestConfig{})
		require.Contains(t, labeled, "aaaa", "sanity: labels render when the cap allows")

		pathsOnly := RenderManifest(files, root, ManifestConfig{MaxBytes: len(labeled) - 1})
		assert.NotContains(t, pathsOnly, "aaaa", "over the cap the labels are dropped first")
		assert.Contains(t, pathsOnly, "- a/AGENTS.md")
		assert.LessOrEqual(t, len(pathsOnly), len(labeled)-1)

		tiny := RenderManifest(files, root, ManifestConfig{MaxBytes: len(pathsOnly) - 1})
		assert.Contains(t, tiny, "more files not shown")
		assert.LessOrEqual(t, len(tiny), len(pathsOnly)-1)
	})
}

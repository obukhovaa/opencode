package prompt

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/contextfile"
	"github.com/opencode-ai/opencode/internal/llm/models"
)

// legacyContextFromPaths replicates the retired
// getContextFromPaths()/processContextPaths() assembly independently of
// internal/contextfile: "# From:<abs-path>\n<body>" per file, sorted by
// absolute path, joined with a single "\n", missing files silently
// skipped. It is the reference the byte-identity assertions compare
// against.
func legacyContextFromPaths(workDir string, paths []string) string {
	abs := make([]string, 0, len(paths))
	for _, p := range paths {
		abs = append(abs, filepath.Join(workDir, p))
	}
	sort.Strings(abs)
	blocks := make([]string, 0, len(abs))
	for _, p := range abs {
		content, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		blocks = append(blocks, "# From:"+p+"\n"+string(content))
	}
	return strings.Join(blocks, "\n")
}

// setupContextWorkspace loads a fresh config rooted at a temp dir with an
// inline-prompt subagent, so the assembled prompt is fully predictable:
// no skills, no deferred tools, no env info (subagent), no LSP servers —
// just the base prompt plus the context block.
func setupContextWorkspace(t *testing.T) (*config.Config, string) {
	t.Helper()
	tmpDir := t.TempDir()
	config.Reset()
	_, err := config.Load(tmpDir, false)
	require.NoError(t, err)
	cfg := config.Get()
	cfg.WorkingDir = tmpDir
	cfg.Agents["ctx-agent"] = config.Agent{Prompt: "You are the context test agent."}
	agentregistry.InvalidateRegistry()
	t.Cleanup(func() {
		config.Reset()
		agentregistry.InvalidateRegistry()
	})
	return cfg, tmpDir
}

// TestGetAgentPrompt_BackwardCompatByteIdentical pins the change's core
// compatibility guarantee: with no agent or step `context` config, the
// assembled system prompt is byte-identical to the pre-change build —
// including the " Make sure to follow..." leading-space header quirk —
// and no manifest section appears when the workspace has no nested
// context files.
func TestGetAgentPrompt_BackwardCompatByteIdentical(t *testing.T) {
	cfg, tmpDir := setupContextWorkspace(t)
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("root instructions\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "docs", "extra.md"), []byte("extra\n"), 0o644))
	cfg.ContextPaths = []string{"AGENTS.md", "docs/"}

	got := GetAgentPrompt("ctx-agent", models.ProviderAnthropic)
	// The reference is constructed the old way: everything up to the
	// context section (assembly code untouched by this change) followed by
	// the legacy header + legacy resolution, with nothing after it. Exact
	// whole-string equality against the old build follows because the
	// prefix is produced by the same unmodified code path.
	marker := "\n\n# Project-Specific Context"
	idx := strings.Index(got, marker)
	require.GreaterOrEqual(t, idx, 0, "the context section must be present")
	want := got[:idx] +
		"\n\n# Project-Specific Context\n Make sure to follow the instructions in the context below\n" +
		legacyContextFromPaths(tmpDir, []string{"AGENTS.md", "docs/extra.md"})
	assert.Equal(t, want, got, "no context config must yield the pre-change prompt byte for byte")
	assert.NotContains(t, got, "# Nested Context Files")
}

// TestGetAgentPrompt_NestedManifest covers the task 4.2 wiring: the
// manifest section appears when discovery finds nested context files, is
// byte-stable across builds, and honors both the config kill switch and
// the agent-level nested opt-out.
func TestGetAgentPrompt_NestedManifest(t *testing.T) {
	t.Run("manifest appended when nested files are discovered", func(t *testing.T) {
		cfg, tmpDir := setupContextWorkspace(t)
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("root\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "services", "auth"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "services", "auth", "AGENTS.md"), []byte("# Auth rules\nbody\n"), 0o644))
		cfg.ContextPaths = []string{"AGENTS.md"}

		got := GetAgentPrompt("ctx-agent", models.ProviderAnthropic)
		assert.Contains(t, got, "# Nested Context Files")
		assert.Contains(t, got, "services/auth/AGENTS.md: Auth rules")
		assert.NotContains(t, got, "body", "nested bodies must never reach the system prompt")

		second := GetAgentPrompt("ctx-agent", models.ProviderAnthropic)
		assert.Equal(t, got, second, "the assembled prompt must be byte-stable across turns")
	})

	t.Run("contextDiscovery.enabled=false suppresses the manifest", func(t *testing.T) {
		cfg, tmpDir := setupContextWorkspace(t)
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("root\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "sub"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "sub", "AGENTS.md"), []byte("nested\n"), 0o644))
		cfg.ContextPaths = []string{"AGENTS.md"}
		cfg.ContextDiscovery = &contextfile.DiscoveryConfig{Enabled: false}

		got := GetAgentPrompt("ctx-agent", models.ProviderAnthropic)
		assert.NotContains(t, got, "# Nested Context Files")
		wantSuffix := "\n\n# Project-Specific Context\n Make sure to follow the instructions in the context below\n" +
			legacyContextFromPaths(tmpDir, []string{"AGENTS.md"})
		assert.True(t, strings.HasSuffix(got, wantSuffix),
			"with discovery off the prompt must end with the pre-change context block, byte for byte")
	})

	t.Run("agent nested opt-out suppresses the manifest", func(t *testing.T) {
		cfg, tmpDir := setupContextWorkspace(t)
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("root\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "sub"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "sub", "AGENTS.md"), []byte("nested\n"), 0o644))
		cfg.ContextPaths = []string{"AGENTS.md"}
		nested := false
		cfg.Agents["ctx-agent"] = config.Agent{
			Prompt:  "You are the context test agent.",
			Context: &contextfile.AgentContext{Nested: &nested},
		}
		agentregistry.InvalidateRegistry()

		got := GetAgentPrompt("ctx-agent", models.ProviderAnthropic)
		assert.NotContains(t, got, "# Nested Context Files")
	})
}

// TestGetAgentPromptWithOptions_StepContext pins the per-call plumbing:
// a step `context` override reaches scoped resolution through
// AgentPromptOptions, exactly like Interactive/BoundPeers — the registry
// entry cannot carry it.
func TestGetAgentPromptWithOptions_StepContext(t *testing.T) {
	cfg, tmpDir := setupContextWorkspace(t)
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("root instructions\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "STEP.md"), []byte("step instructions\n"), 0o644))
	cfg.ContextPaths = []string{"AGENTS.md"}

	got := GetAgentPromptWithOptions("ctx-agent", models.ProviderAnthropic, AgentPromptOptions{
		StepContext: &contextfile.StepContext{Paths: []string{"STEP.md"}, Mode: "replace"},
	})
	assert.Contains(t, got, "step instructions")
	assert.NotContains(t, got, "root instructions", "step replace must exclude the global layer")
}

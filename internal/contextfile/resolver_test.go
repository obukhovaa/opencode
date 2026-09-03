package contextfile

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestFiles mirrors the helper the ported prompt-package tests used:
// each file's content is "<relpath>: test content".
func createTestFiles(t *testing.T, tmpDir string, testFiles []string) {
	t.Helper()
	for _, path := range testFiles {
		fullPath := filepath.Join(tmpDir, path)
		if strings.HasSuffix(path, "/") {
			require.NoError(t, os.MkdirAll(fullPath, 0o755))
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
		require.NoError(t, os.WriteFile(fullPath, []byte(path+": test content"), 0o644))
	}
}

// TestResolve ports the processContextPaths cases from
// internal/llm/prompt/prompt_test.go — Resolve with a single layer IS the
// moved function, so every observable behavior must hold unchanged.
func TestResolve(t *testing.T) {
	t.Parallel()

	t.Run("single file", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"a.txt"})

		result := Resolve([]string{"a.txt"}, tmpDir, ModeAppend)
		assert.Contains(t, result, "a.txt: test content")
	})

	t.Run("directory with trailing slash", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"docs/one.txt", "docs/two.txt"})

		result := Resolve([]string{"docs/"}, tmpDir, ModeAppend)
		assert.Contains(t, result, "one.txt: test content")
		assert.Contains(t, result, "two.txt: test content")
	})

	t.Run("symlink to file is deduplicated", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"real.txt"})
		require.NoError(t, os.Symlink(filepath.Join(tmpDir, "real.txt"), filepath.Join(tmpDir, "link.txt")))

		result := Resolve([]string{"real.txt", "link.txt"}, tmpDir, ModeAppend)
		assert.Equal(t, 1, strings.Count(result, "real.txt: test content"), "symlinked file should only appear once")
	})

	t.Run("symlink to directory is deduplicated", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"realdir/file.txt"})
		require.NoError(t, os.Symlink(filepath.Join(tmpDir, "realdir"), filepath.Join(tmpDir, "linkdir")))

		result := Resolve([]string{"realdir/", "linkdir/"}, tmpDir, ModeAppend)
		assert.Equal(t, 1, strings.Count(result, "file.txt: test content"), "file in symlinked directory should only appear once")
	})

	t.Run("same file listed twice is deduplicated", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"dup.txt"})

		result := Resolve([]string{"dup.txt", "dup.txt"}, tmpDir, ModeAppend)
		assert.Equal(t, 1, strings.Count(result, "dup.txt: test content"), "duplicate path should only appear once")
	})

	t.Run("file in directory and explicit path is deduplicated", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"ctx/notes.txt"})

		result := Resolve([]string{"ctx/", "ctx/notes.txt"}, tmpDir, ModeAppend)
		assert.Equal(t, 1, strings.Count(result, "notes.txt: test content"), "file listed both via directory and explicit path should only appear once")
	})

	t.Run("nonexistent path produces no output", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()

		result := Resolve([]string{"does-not-exist.txt"}, tmpDir, ModeAppend)
		assert.Empty(t, result)
	})

	t.Run("empty paths produces no output", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()

		result := Resolve([]string{}, tmpDir, ModeAppend)
		assert.Empty(t, result)
	})

	t.Run("symlink in walked directory is deduplicated with explicit path", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"source.txt"})
		require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "dir"), 0o755))
		require.NoError(t, os.Symlink(filepath.Join(tmpDir, "source.txt"), filepath.Join(tmpDir, "dir", "link.txt")))

		result := Resolve([]string{"source.txt", "dir/"}, tmpDir, ModeAppend)
		assert.Equal(t, 1, strings.Count(result, "source.txt: test content"), "symlink inside directory should be deduplicated against explicit path")
	})

	t.Run("From header format and sort order are preserved", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"b.txt", "a.txt"})

		result := Resolve([]string{"b.txt", "a.txt"}, tmpDir, ModeAppend)
		wantA := "# From:" + filepath.Join(tmpDir, "a.txt") + "\na.txt: test content"
		wantB := "# From:" + filepath.Join(tmpDir, "b.txt") + "\nb.txt: test content"
		assert.Equal(t, wantA+"\n"+wantB, result, "entries must sort by absolute path and join with a single newline")
	})
}

func TestResolve_ByteStabilityAndPointerReuse(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir, []string{"stable.txt"})

	first := Resolve([]string{"stable.txt"}, tmpDir, ModeAppend)
	// Mutate the file AFTER the first resolution: the memoized value must
	// win — same staleness semantics as the retired sync.Once.
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "stable.txt"), []byte("changed"), 0o644))
	second := Resolve([]string{"stable.txt"}, tmpDir, ModeAppend)

	assert.Equal(t, first, second)
	assert.Equal(t, unsafe.StringData(first), unsafe.StringData(second),
		"the cached string must be reused, not recomputed or copied")
}

func TestResolveForAgent(t *testing.T) {
	t.Parallel()

	// Each subtest builds a workspace with a global, an agent, and a step
	// context file so layer membership is observable in the output.
	setup := func(t *testing.T) string {
		t.Helper()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"AGENTS.md", "AGENTS.agent.md", "AGENTS.step.md"})
		return tmpDir
	}
	global := []string{"AGENTS.md"}

	t.Run("no agent or step config resolves the global paths verbatim", func(t *testing.T) {
		t.Parallel()
		tmpDir := setup(t)

		got := ResolveForAgent(global, nil, nil, tmpDir, TemplateVars{Agent: "coder"})
		want := Resolve(global, tmpDir, ModeAppend)
		assert.Equal(t, want, got, "absent config must produce the exact single-layer resolution")
		assert.Contains(t, got, "AGENTS.md: test content")
	})

	t.Run("step replace excludes agent and global layers", func(t *testing.T) {
		t.Parallel()
		tmpDir := setup(t)

		got := ResolveForAgent(global,
			&AgentContext{Paths: []string{"AGENTS.agent.md"}, Mode: "append"},
			&StepContext{Paths: []string{"AGENTS.step.md"}, Mode: "replace"},
			tmpDir, TemplateVars{Agent: "coder"})
		assert.Contains(t, got, "AGENTS.step.md: test content")
		assert.NotContains(t, got, "AGENTS.agent.md: test content")
		assert.NotContains(t, got, "AGENTS.md: test content")
	})

	t.Run("empty mode defaults to replace", func(t *testing.T) {
		t.Parallel()
		tmpDir := setup(t)

		got := ResolveForAgent(global,
			&AgentContext{Paths: []string{"AGENTS.agent.md"}}, nil,
			tmpDir, TemplateVars{Agent: "coder"})
		assert.Contains(t, got, "AGENTS.agent.md: test content")
		assert.NotContains(t, got, "AGENTS.md: test content")
	})

	t.Run("step append over agent replace discards global only", func(t *testing.T) {
		t.Parallel()
		tmpDir := setup(t)

		got := ResolveForAgent(global,
			&AgentContext{Paths: []string{"AGENTS.agent.md"}, Mode: "replace"},
			&StepContext{Paths: []string{"AGENTS.step.md"}, Mode: "append"},
			tmpDir, TemplateVars{Agent: "coder"})
		assert.Contains(t, got, "AGENTS.step.md: test content")
		assert.Contains(t, got, "AGENTS.agent.md: test content")
		assert.NotContains(t, got, "AGENTS.md: test content")
	})

	t.Run("append accumulates all three layers in global-agent-step order", func(t *testing.T) {
		t.Parallel()
		tmpDir := setup(t)

		got := ResolveForAgent(global,
			&AgentContext{Paths: []string{"AGENTS.agent.md"}, Mode: "append"},
			&StepContext{Paths: []string{"AGENTS.step.md"}, Mode: "append"},
			tmpDir, TemplateVars{Agent: "coder"})
		gi := strings.Index(got, "AGENTS.md: test content")
		ai := strings.Index(got, "AGENTS.agent.md: test content")
		si := strings.Index(got, "AGENTS.step.md: test content")
		require.True(t, gi >= 0 && ai >= 0 && si >= 0, "all three layers must contribute: %q", got)
		assert.Less(t, gi, ai, "global layer renders before the agent layer")
		assert.Less(t, ai, si, "agent layer renders before the step layer")
	})

	t.Run("a file named at two layers appears once", func(t *testing.T) {
		t.Parallel()
		tmpDir := setup(t)

		got := ResolveForAgent(global,
			&AgentContext{Paths: []string{"AGENTS.md", "AGENTS.agent.md"}, Mode: "append"},
			nil, tmpDir, TemplateVars{Agent: "coder"})
		assert.Equal(t, 1, strings.Count(got, "AGENTS.md: test content"), "cross-layer dedup must collapse the duplicate")
	})

	t.Run("unrecognized mode fails safe to append", func(t *testing.T) {
		t.Parallel()
		tmpDir := setup(t)

		got := ResolveForAgent(global,
			&AgentContext{Paths: []string{"AGENTS.agent.md"}, Mode: "xyzzy"},
			nil, tmpDir, TemplateVars{Agent: "coder"})
		assert.Contains(t, got, "AGENTS.agent.md: test content")
		assert.Contains(t, got, "AGENTS.md: test content", "a typo must not drop the project's root instructions")
	})

	t.Run("explicitly empty paths with replace yields an empty context block", func(t *testing.T) {
		t.Parallel()
		tmpDir := setup(t)

		// paths: [] IS a declaration — the natural way to give an agent
		// zero context files. Only a nil Paths is undeclared.
		got := ResolveForAgent(global,
			&AgentContext{Paths: []string{}, Mode: "replace"},
			nil, tmpDir, TemplateVars{Agent: "coder"})
		assert.Empty(t, got, "empty declared replace layer must exclude every layer below")
	})

	t.Run("explicitly empty paths with append contributes nothing and continues downward", func(t *testing.T) {
		t.Parallel()
		tmpDir := setup(t)

		got := ResolveForAgent(global,
			&AgentContext{Paths: []string{}, Mode: "append"},
			nil, tmpDir, TemplateVars{Agent: "coder"})
		want := Resolve(global, tmpDir, ModeAppend)
		assert.Equal(t, want, got, "empty append layer must fall through to the global resolution")
	})

	t.Run("empty step replace shields the agent and global layers too", func(t *testing.T) {
		t.Parallel()
		tmpDir := setup(t)

		got := ResolveForAgent(global,
			&AgentContext{Paths: []string{"AGENTS.agent.md"}, Mode: "append"},
			&StepContext{Paths: []string{}, Mode: "replace"},
			tmpDir, TemplateVars{Agent: "coder"})
		assert.Empty(t, got)
	})

	t.Run("agent token resolves a per-agent file", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		createTestFiles(t, tmpDir, []string{"AGENTS.workhorse.md"})

		got := ResolveForAgent(nil,
			&AgentContext{Paths: []string{"AGENTS.${agent}.md"}},
			nil, tmpDir, TemplateVars{Agent: "workhorse"})
		assert.Contains(t, got, "AGENTS.workhorse.md: test content")
	})
}

func TestExpandTokens(t *testing.T) {
	vars := TemplateVars{Agent: "coder", FlowID: "review", FlowStep: "plan"}
	t.Setenv("CONTEXTFILE_TEST_VAR", "fromenv")

	tests := []struct {
		name   string
		entry  string
		vars   TemplateVars
		want   string
		wantOK bool
	}{
		{name: "agent token", entry: "AGENTS.${agent}.md", vars: vars, want: "AGENTS.coder.md", wantOK: true},
		{name: "flow tokens", entry: "${flow.id}/${flow.step}.md", vars: vars, want: "review/plan.md", wantOK: true},
		{name: "env token", entry: "docs/${env.CONTEXTFILE_TEST_VAR}.md", vars: vars, want: "docs/fromenv.md", wantOK: true},
		{name: "no tokens", entry: "AGENTS.md", vars: vars, want: "AGENTS.md", wantOK: true},
		{name: "unknown token skips", entry: "AGENTS.${unknown}.md", vars: vars, wantOK: false},
		{name: "empty flow.id skips", entry: "AGENTS.${flow.id}.md", vars: TemplateVars{Agent: "coder"}, wantOK: false},
		{name: "empty env var skips", entry: "docs/${env.CONTEXTFILE_TEST_UNSET}.md", vars: vars, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := expandTokens(tt.entry, tt.vars)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestContainedInWorkDir_RejectsTraversal(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir, []string{"safe.md"})

	assert.True(t, containedInWorkDir("safe.md", tmpDir))
	assert.False(t, containedInWorkDir("../../../etc/passwd", tmpDir), "a traversal entry must be rejected")
	assert.False(t, containedInWorkDir("..", tmpDir))

	// The rejection must also hold through ResolveForAgent: the escaping
	// entry never reaches the filesystem, so the output is empty.
	got := ResolveForAgent(nil,
		&AgentContext{Paths: []string{"../../../etc/passwd"}},
		nil, tmpDir, TemplateVars{Agent: "coder"})
	assert.Empty(t, got)
}

// TestContainedInWorkDir_RejectsSymlinkEscape pins the design D5 claim
// that symlink chains cannot bypass containment: the check runs on
// EvalSymlinks output, so a lexically-inside entry that resolves outside
// workDir is rejected and never read.
func TestContainedInWorkDir_RejectsSymlinkEscape(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	require.NoError(t, os.WriteFile(secret, []byte("SECRET CONTENT"), 0o600))

	tmpDir := t.TempDir()
	require.NoError(t, os.Symlink(secret, filepath.Join(tmpDir, "evil.md")))

	assert.False(t, containedInWorkDir("evil.md", tmpDir),
		"a symlink resolving outside workDir must fail containment")

	got := ResolveForAgent(nil,
		&AgentContext{Paths: []string{"evil.md"}},
		nil, tmpDir, TemplateVars{Agent: "coder"})
	assert.Empty(t, got, "the escaping symlink must never be resolved into the prompt")
}

// TestNormalizeMode_WarnNamesOwner pins the spec's "Unrecognized mode
// fails safe" THEN clause: the WARN names the misconfigured agent (or
// step) and the raw value, and the once-only dedupe is keyed on that
// identity — a SECOND agent with the same typo still gets its own line.
func TestNormalizeMode_WarnNamesOwner(t *testing.T) {
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&syncWriter{w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir, []string{"AGENTS.md"})

	// Unique typo per test run: modeWarned is process-global and another
	// test using the same (layer, owner, value) would swallow the log.
	const typo = "xyzzy-warn-owner"
	ResolveForAgent([]string{"AGENTS.md"},
		&AgentContext{Paths: []string{"AGENTS.md"}, Mode: typo},
		nil, tmpDir, TemplateVars{Agent: "first-agent"})
	logged := buf.String()
	assert.Contains(t, logged, "first-agent", "the WARN must name the agent")
	assert.Contains(t, logged, typo, "the WARN must name the unrecognized value")

	ResolveForAgent([]string{"AGENTS.md"},
		&AgentContext{Paths: []string{"AGENTS.md"}, Mode: typo},
		nil, tmpDir, TemplateVars{Agent: "second-agent"})
	assert.Contains(t, buf.String(), "second-agent",
		"a different agent with the same typo must still be warned about")

	// Same agent again: deduped, exactly one line naming it.
	before := strings.Count(buf.String(), "first-agent")
	ResolveForAgent([]string{"AGENTS.md"},
		&AgentContext{Paths: []string{"AGENTS.md"}, Mode: typo},
		nil, tmpDir, TemplateVars{Agent: "first-agent"})
	assert.Equal(t, before, strings.Count(buf.String(), "first-agent"),
		"the WARN must fire once per (agent, value)")

	// The step layer names the step ID.
	ResolveForAgent([]string{"AGENTS.md"}, nil,
		&StepContext{Paths: []string{"AGENTS.md"}, Mode: typo},
		tmpDir, TemplateVars{Agent: "first-agent", FlowStep: "review-step"})
	assert.Contains(t, buf.String(), "review-step", "the WARN must name the flow step for a step layer")
}

// syncWriter guards the capture buffer: resolver internals log from
// goroutines.
type syncWriter struct {
	mu sync.Mutex
	w  *strings.Builder
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/contextfile"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// disclosureFixture builds a workDir with the given nested context files
// (relative path -> body) and the shared per-toolset state via the real
// config-gated constructor, so discovery order and cap handling match
// production.
func disclosureFixture(t *testing.T, files map[string]string, discovery *contextfile.DiscoveryConfig) (string, *contextDisclosureState) {
	t.Helper()
	workDir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(workDir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	}
	if discovery == nil {
		discovery = &contextfile.DiscoveryConfig{Enabled: true}
	}
	cfg := &config.Config{
		WorkingDir:       workDir,
		ContextPaths:     []string{"AGENTS.md"},
		ContextDiscovery: discovery,
	}
	return workDir, newContextDisclosureState(&agentregistry.AgentInfo{}, cfg)
}

func sessionCtx(id string) context.Context {
	return context.WithValue(context.Background(), tools.SessionIDContextKey, id)
}

// staticTool is a trigger-named fake whose Run always succeeds with body.
func staticTool(name, body string) *fakeTool {
	return &fakeTool{name: name, parallel: true, runFn: func(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
		return tools.NewTextResponse(body), nil
	}}
}

func TestContextDisclosureWrapper_InjectsOnceOnFirstTouch(t *testing.T) {
	workDir, state := disclosureFixture(t, map[string]string{
		"services/auth/AGENTS.md": "auth rules",
	}, nil)
	require.NotNil(t, state)
	w := &contextDisclosureWrapper{inner: staticTool("read", "file contents"), state: state}
	ctx := sessionCtx("session-a")
	call := tools.ToolCall{ID: "1", Name: "read", Input: `{"file_path":"services/auth/handler.go"}`}

	resp, err := w.Run(ctx, call)
	require.NoError(t, err)
	authPath := filepath.Join(workDir, "services", "auth", "AGENTS.md")
	assert.True(t, strings.HasPrefix(resp.Content, "file contents"), "tool output must come first")
	assert.Contains(t, resp.Content, "<system-reminder>\n# From:"+authPath+"\nauth rules\n</system-reminder>")

	resp, err = w.Run(ctx, call)
	require.NoError(t, err)
	assert.Equal(t, "file contents", resp.Content, "second touch must not re-inject")
}

func TestContextDisclosureWrapper_CrossToolDedup(t *testing.T) {
	_, state := disclosureFixture(t, map[string]string{
		"services/auth/AGENTS.md": "auth rules",
	}, nil)
	require.NotNil(t, state)
	// Two wrappers, ONE shared state — the per-toolset invariant.
	readW := &contextDisclosureWrapper{inner: staticTool("read", "read out"), state: state}
	grepW := &contextDisclosureWrapper{inner: staticTool("grep", "grep out"), state: state}
	ctx := sessionCtx("session-a")

	resp, err := readW.Run(ctx, tools.ToolCall{ID: "1", Name: "read", Input: `{"file_path":"services/auth/handler.go"}`})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "auth rules")

	resp, err = grepW.Run(ctx, tools.ToolCall{ID: "2", Name: "grep", Input: `{"pattern":"x","path":"services/auth"}`})
	require.NoError(t, err)
	assert.Equal(t, "grep out", resp.Content, "grep on the already-injected directory must not re-inject")
}

func TestContextDisclosureWrapper_OutermostFirst(t *testing.T) {
	workDir, state := disclosureFixture(t, map[string]string{
		"services/AGENTS.md":      "services layer",
		"services/auth/AGENTS.md": "auth layer",
	}, nil)
	require.NotNil(t, state)
	w := &contextDisclosureWrapper{inner: staticTool("read", "out"), state: state}

	resp, err := w.Run(sessionCtx("s"), tools.ToolCall{ID: "1", Name: "read", Input: `{"file_path":"services/auth/handler.go"}`})
	require.NoError(t, err)
	outer := strings.Index(resp.Content, "# From:"+filepath.Join(workDir, "services", "AGENTS.md"))
	inner := strings.Index(resp.Content, "# From:"+filepath.Join(workDir, "services", "auth", "AGENTS.md"))
	require.GreaterOrEqual(t, outer, 0)
	require.GreaterOrEqual(t, inner, 0)
	assert.Less(t, outer, inner, "outermost owner must be injected before the inner one")
}

func TestContextDisclosureWrapper_GrepWithoutPathActivatesNothing(t *testing.T) {
	_, state := disclosureFixture(t, map[string]string{
		"services/auth/AGENTS.md": "auth rules",
	}, nil)
	require.NotNil(t, state)
	w := &contextDisclosureWrapper{inner: staticTool("grep", "matches"), state: state}

	resp, err := w.Run(sessionCtx("s"), tools.ToolCall{ID: "1", Name: "grep", Input: `{"pattern":"TODO"}`})
	require.NoError(t, err)
	assert.Equal(t, "matches", resp.Content, "a whole-tree grep resolves to workDir and must activate nothing")
}

func TestContextDisclosureWrapper_FailedCallDoesNotInject(t *testing.T) {
	_, state := disclosureFixture(t, map[string]string{
		"services/auth/AGENTS.md": "auth rules",
	}, nil)
	require.NotNil(t, state)
	ctx := sessionCtx("s")
	call := tools.ToolCall{ID: "1", Name: "read", Input: `{"file_path":"services/auth/missing.go"}`}

	errorResult := &fakeTool{name: "read", runFn: func(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
		return tools.NewTextErrorResponse("file not found"), nil
	}}
	resp, err := (&contextDisclosureWrapper{inner: errorResult, state: state}).Run(ctx, call)
	require.NoError(t, err)
	assert.Equal(t, "file not found", resp.Content)

	hardError := &fakeTool{name: "read", runFn: func(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
		return tools.ToolResponse{}, errors.New("boom")
	}}
	_, err = (&contextDisclosureWrapper{inner: hardError, state: state}).Run(ctx, call)
	require.Error(t, err)

	// The failures must not have consumed the activation: the first
	// SUCCESSFUL touch still injects.
	resp, err = (&contextDisclosureWrapper{inner: staticTool("read", "ok"), state: state}).Run(ctx, call)
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "auth rules")
}

func TestContextDisclosureWrapper_UnreadableFileSkipped(t *testing.T) {
	workDir, state := disclosureFixture(t, map[string]string{
		"services/auth/AGENTS.md": "auth rules",
	}, nil)
	require.NotNil(t, state)
	// Deleted between discovery and activation (design D10).
	require.NoError(t, os.Remove(filepath.Join(workDir, "services", "auth", "AGENTS.md")))
	w := &contextDisclosureWrapper{inner: staticTool("read", "out"), state: state}

	resp, err := w.Run(sessionCtx("s"), tools.ToolCall{ID: "1", Name: "read", Input: `{"file_path":"services/auth/handler.go"}`})
	require.NoError(t, err)
	assert.Equal(t, "out", resp.Content, "unreadable file must be skipped with the tool result unchanged")
}

func TestContextDisclosureWrapper_OversizedFileSkipped(t *testing.T) {
	_, state := disclosureFixture(t, map[string]string{
		"services/auth/AGENTS.md": "this body is larger than the cap",
	}, &contextfile.DiscoveryConfig{Enabled: true, MaxFileBytes: 8})
	require.NotNil(t, state)
	w := &contextDisclosureWrapper{inner: staticTool("read", "out"), state: state}

	resp, err := w.Run(sessionCtx("s"), tools.ToolCall{ID: "1", Name: "read", Input: `{"file_path":"services/auth/handler.go"}`})
	require.NoError(t, err)
	assert.Equal(t, "out", resp.Content, "oversized file must be skipped with the tool result unchanged")
}

func TestContextDisclosureWrapper_BudgetExhaustion(t *testing.T) {
	_, state := disclosureFixture(t, map[string]string{
		"services/auth/AGENTS.md":    "12345678", // 8 bytes
		"services/billing/AGENTS.md": "abcdefgh", // 8 bytes
	}, &contextfile.DiscoveryConfig{Enabled: true, MaxSessionBytes: 10})
	require.NotNil(t, state)
	w := &contextDisclosureWrapper{inner: staticTool("read", "out"), state: state}
	ctx := sessionCtx("budget-session")

	resp, err := w.Run(ctx, tools.ToolCall{ID: "1", Name: "read", Input: `{"file_path":"services/auth/handler.go"}`})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "12345678", "first body fits the budget")

	resp, err = w.Run(ctx, tools.ToolCall{ID: "2", Name: "read", Input: `{"file_path":"services/billing/invoice.go"}`})
	require.NoError(t, err)
	assert.Equal(t, "out", resp.Content, "over-budget body must not be injected")
	state.mu.Lock()
	assert.True(t, state.budgetExhausted["budget-session"], "exhaustion flag records the one-shot INFO log")
	state.mu.Unlock()

	// Budget exhaustion is per session: a fresh session still gets bodies.
	resp, err = w.Run(sessionCtx("other"), tools.ToolCall{ID: "3", Name: "read", Input: `{"file_path":"services/billing/invoice.go"}`})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "abcdefgh")
}

func TestContextDisclosureWrapper_SessionIsolation(t *testing.T) {
	_, state := disclosureFixture(t, map[string]string{
		"services/auth/AGENTS.md": "auth rules",
	}, nil)
	require.NotNil(t, state)
	w := &contextDisclosureWrapper{inner: staticTool("read", "out"), state: state}
	call := tools.ToolCall{ID: "1", Name: "read", Input: `{"file_path":"services/auth/handler.go"}`}

	resp, err := w.Run(sessionCtx("session-a"), call)
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "auth rules")

	// A second session on the SAME wrapper instance (same primary agent)
	// gets its own activation — the model the subagent isolation rule
	// rides on (subagent sessions carry their own session ID).
	resp, err = w.Run(sessionCtx("session-b"), call)
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "auth rules", "sessions must not observe each other's activations")
}

func TestContextDisclosure_DeferredTriggerKeepsDeferredOutermost(t *testing.T) {
	_, state := disclosureFixture(t, map[string]string{
		"services/auth/AGENTS.md": "auth rules",
	}, nil)
	require.NotNil(t, state)

	// The NewToolSet composition: maybeDefer(maybeWrapDisclosure(t)).
	seq := &atomic.Int64{}
	var composed tools.BaseTool = tools.WrapDeferred(
		&contextDisclosureWrapper{inner: staticTool("read", "out"), state: state}, seq)

	// The four existing type-assertion sites all assert on the OUTERMOST
	// type: a tool that is both deferred and a trigger must still be a
	// *tools.DeferredWrapper.
	deferred, ok := composed.(*tools.DeferredWrapper)
	require.True(t, ok, "DeferredWrapper must stay outermost")
	assert.Equal(t, "read", deferred.Info().Name)

	// Both behaviors coexist: deferral activation bookkeeping...
	deferred.Activate("s")
	_, activated := deferred.ActivatedAt("s")
	assert.True(t, activated)

	// ...and disclosure injection through the delegated Run.
	resp, err := composed.Run(sessionCtx("s"), tools.ToolCall{ID: "1", Name: "read", Input: `{"file_path":"services/auth/handler.go"}`})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, "auth rules")
}

func TestNewContextDisclosureState(t *testing.T) {
	nested := map[string]string{"services/auth/AGENTS.md": "auth rules"}
	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name     string
		files    map[string]string
		info     *agentregistry.AgentInfo
		disc     *contextfile.DiscoveryConfig
		wantNil  bool
		wantFile string
	}{
		{
			name:     "nested files and default gating yield a state",
			files:    nested,
			info:     &agentregistry.AgentInfo{},
			wantFile: filepath.Join("services", "auth", "AGENTS.md"),
		},
		{
			name:    "discovery disabled",
			files:   nested,
			info:    &agentregistry.AgentInfo{},
			disc:    &contextfile.DiscoveryConfig{Enabled: false},
			wantNil: true,
		},
		{
			name:    "agent nested opt-out",
			files:   nested,
			info:    &agentregistry.AgentInfo{Context: &contextfile.AgentContext{Nested: boolPtr(false)}},
			wantNil: true,
		},
		{
			name:    "step nested opt-out",
			files:   nested,
			info:    &agentregistry.AgentInfo{StepContext: &contextfile.StepContext{Nested: boolPtr(false)}},
			wantNil: true,
		},
		{
			name:    "no nested files discovered",
			files:   map[string]string{"AGENTS.md": "root only"},
			info:    &agentregistry.AgentInfo{},
			wantNil: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := t.TempDir()
			for rel, body := range tt.files {
				full := filepath.Join(workDir, rel)
				require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
				require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
			}
			disc := tt.disc
			if disc == nil {
				disc = &contextfile.DiscoveryConfig{Enabled: true}
			}
			cfg := &config.Config{WorkingDir: workDir, ContextPaths: []string{"AGENTS.md"}, ContextDiscovery: disc}

			state := newContextDisclosureState(tt.info, cfg)
			if tt.wantNil {
				assert.Nil(t, state, "no wrapper state expected — zero wrappers installed")
				return
			}
			require.NotNil(t, state)
			assert.Equal(t, []string{filepath.Join(workDir, tt.wantFile)}, state.discovered)
			assert.Equal(t, contextfile.DefaultDiscoveryMaxFileBytes, state.maxFileBytes, "unset caps must be defaulted")
			assert.Equal(t, contextfile.DefaultDiscoveryMaxSessionBytes, state.maxSessionBytes)
		})
	}

	t.Run("nil config yields nil", func(t *testing.T) {
		assert.Nil(t, newContextDisclosureState(&agentregistry.AgentInfo{}, nil))
	})
}

package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/opencode-ai/opencode/internal/hooks"
)

// hooksFromMap is the inline in-memory equivalent of reading the `hooks`
// block out of `.opencode.json` — production wires the getter to
// `config.Get().Hooks`; tests skip viper entirely and pass the map directly.
func hooksFromMap(m map[string][]hooks.MatcherGroup) func() map[string][]hooks.MatcherGroup {
	return func() map[string][]hooks.MatcherGroup { return m }
}

// TestAgentFirePreTool_RewritesInputViaSubprocess is the v1 integration
// test: a hook map (the in-memory equivalent of `.opencode.json`'s
// hooks block) + real shell-script hook + real registry must produce a
// mutated tool input as seen by the agent's tool-dispatch code.
func TestAgentFirePreTool_RewritesInputViaSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hooks runner is POSIX-only in v1")
	}
	dir := t.TempDir()
	// A hook that mirrors RTK's contract: it inspects tool_input.command,
	// emits updatedInput with a prefix. No external dependency.
	script := writeScript(t, dir, "rewrite.sh", `#!/bin/sh
cat > /dev/null
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"command":"rtk git status"}}}'
`)
	reg := hooks.NewRegistry(hooksFromMap(map[string][]hooks.MatcherGroup{
		"PreToolUse": {
			{Matcher: "bash", Hooks: []hooks.HookEntry{{Type: "command", Command: script}}},
		},
	}), dir)
	a := &agent{factory: &fakeFactoryWithHooks{reg: reg}}

	originalInput := `{"command":"git status"}`
	got, hc := a.firePreTool(context.Background(), "sess-1", "bash", originalInput)
	if hc.decision.Block {
		t.Fatalf("hook should rewrite, not block; reason=%q", hc.decision.BlockReason)
	}
	if got != `{"command":"rtk git status"}` {
		t.Errorf("rewritten input = %q, want %q", got, `{"command":"rtk git status"}`)
	}
}

func TestAgentFirePreTool_BlockShortCircuitsTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hooks runner is POSIX-only in v1")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, "deny.sh", `#!/bin/sh
cat > /dev/null
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"rm -rf is forbidden"}}'
`)
	reg := hooks.NewRegistry(hooksFromMap(map[string][]hooks.MatcherGroup{
		"PreToolUse": {
			{Matcher: "bash", Hooks: []hooks.HookEntry{{Type: "command", Command: script}}},
		},
	}), dir)
	a := &agent{factory: &fakeFactoryWithHooks{reg: reg}}

	got, hc := a.firePreTool(context.Background(), "sess-1", "bash", `{"command":"rm -rf /"}`)
	if !hc.decision.Block {
		t.Fatalf("expected Block=true, got Block=false")
	}
	if hc.decision.BlockReason != "rm -rf is forbidden" {
		t.Errorf("BlockReason = %q, want %q", hc.decision.BlockReason, "rm -rf is forbidden")
	}
	if got != `{"command":"rm -rf /"}` {
		t.Errorf("input pass-through broken; got %q", got)
	}
}

func TestAgentFirePostTool_RewritesOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hooks runner is POSIX-only in v1")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, "compact.sh", `#!/bin/sh
cat > /dev/null
echo '{"hookSpecificOutput":{"hookEventName":"PostToolUse","updatedToolOutput":"compacted (3 lines)"}}'
`)
	reg := hooks.NewRegistry(hooksFromMap(map[string][]hooks.MatcherGroup{
		"PostToolUse": {
			{Matcher: "bash", Hooks: []hooks.HookEntry{{Type: "command", Command: script}}},
		},
	}), dir)
	a := &agent{factory: &fakeFactoryWithHooks{reg: reg}}

	rewritten, postCtx := a.firePostTool(context.Background(), "sess-1", "bash", `{"command":"cargo test"}`,
		"line1\nline2\n...200 more lines...")
	if rewritten != "compacted (3 lines)" {
		t.Errorf("output not replaced; got %q", rewritten)
	}
	if postCtx != "" {
		t.Errorf("post additionalContext should be empty here; got %q", postCtx)
	}
}

// TestJoinHookContext_HandlesAllShapes locks in the helper's contract.
// joinHookContext is the only code path that decides how PreToolUse and
// PostToolUse additionalContext combine before reaching the agent, so
// regressions here would silently swallow plugin output.
func TestJoinHookContext_HandlesAllShapes(t *testing.T) {
	cases := []struct{ pre, post, want string }{
		{"", "", ""},
		{"only-pre", "", "only-pre"},
		{"", "only-post", "only-post"},
		{"pre", "post", "pre\npost"},
	}
	for _, c := range cases {
		got := joinHookContext(c.pre, c.post)
		if got != c.want {
			t.Errorf("joinHookContext(%q, %q) = %q, want %q", c.pre, c.post, got, c.want)
		}
	}
}

// TestAppendHookContext_HandlesAllShapes locks in the content+context
// composition format. The agent loop calls this on EVERY tool result
// (block path and non-block path), so a change in formatting would
// affect how plugins' notes surface to the agent.
func TestAppendHookContext_HandlesAllShapes(t *testing.T) {
	cases := []struct {
		content, ctx, want string
	}{
		{"", "", ""},
		{"content", "", "content"},
		{"", "ctx", "[hook context: ctx]"},
		{"content", "ctx", "content\n\n[hook context: ctx]"},
	}
	for _, c := range cases {
		got := appendHookContext(c.content, c.ctx)
		if got != c.want {
			t.Errorf("appendHookContext(%q, %q) = %q, want %q", c.content, c.ctx, got, c.want)
		}
	}
}

// TestAgentFirePostTool_PropagatesAdditionalContext locks in the HIGH-
// severity fix: PostToolUse `additionalContext` must reach the caller
// (so the agent loop can append it to the tool result). Before the fix
// it was silently dropped.
func TestAgentFirePostTool_PropagatesAdditionalContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hooks runner is POSIX-only in v1")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, "ctx.sh", `#!/bin/sh
cat > /dev/null
echo '{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"redacted 3 secrets"}}'
`)
	reg := hooks.NewRegistry(hooksFromMap(map[string][]hooks.MatcherGroup{
		"PostToolUse": {
			{Matcher: "bash", Hooks: []hooks.HookEntry{{Type: "command", Command: script}}},
		},
	}), dir)
	a := &agent{factory: &fakeFactoryWithHooks{reg: reg}}

	out, postCtx := a.firePostTool(context.Background(), "sess", "bash", `{"command":"env"}`, "ORIGINAL")
	if out != "ORIGINAL" {
		t.Errorf("with no updatedToolOutput the original must pass through; got %q", out)
	}
	if postCtx != "redacted 3 secrets" {
		t.Errorf("additionalContext not propagated; got %q, want %q", postCtx, "redacted 3 secrets")
	}
}

func TestAgentFirePreTool_NilFactoryDegradesQuietly(t *testing.T) {
	a := &agent{factory: nil}
	got, hc := a.firePreTool(context.Background(), "sess", "bash", `{"command":"ls"}`)
	if hc.registry != nil || hc.decision.Block {
		t.Errorf("expected zero hookCall; got %+v", hc)
	}
	if got != `{"command":"ls"}` {
		t.Errorf("input mutated unexpectedly: %q", got)
	}
}

func TestAgentFirePreTool_NilRegistryDegradesQuietly(t *testing.T) {
	a := &agent{factory: &fakeFactoryWithHooks{reg: nil}}
	got, hc := a.firePreTool(context.Background(), "sess", "bash", `{"command":"ls"}`)
	if hc.registry != nil || hc.decision.Block {
		t.Errorf("expected zero hookCall; got %+v", hc)
	}
	if got != `{"command":"ls"}` {
		t.Errorf("input mutated unexpectedly: %q", got)
	}
}

// --- helpers ------------------------------------------------------------

// fakeFactoryWithHooks satisfies just the bits of AgentFactory that the
// hook helpers touch. The other AgentFactory methods are unreachable
// from firePreTool / firePostTool, so we don't need to stub them.
type fakeFactoryWithHooks struct {
	AgentFactory // embedded so we inherit method set
	reg          *hooks.Registry
}

func (f *fakeFactoryWithHooks) HookRegistry() *hooks.Registry { return f.reg }

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

package hooks

import (
	"context"
	"encoding/json"
	"testing"
)

// hooksGetter returns a getter that always returns the same in-memory
// map. Tests skip the file-loading layer entirely (that's exercised
// indirectly through `internal/config` and the wider integration tests).
func hooksGetter(m map[string][]MatcherGroup) func() map[string][]MatcherGroup {
	return func() map[string][]MatcherGroup { return m }
}

// TestRunPreTool_UpdatedInputChains verifies that a second hook receives
// the FIRST hook's updatedInput as its tool_input. This is the documented
// D6 chaining rule — without it, plugins that compose (e.g. RTK +
// audit-log appender) cannot stack.
func TestRunPreTool_UpdatedInputChains(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	first := writeScript(t, dir, "first.sh", `#!/bin/sh
cat > /dev/null
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"command":"first"}}}'
`)
	// Second hook reads stdin and only rewrites if it saw `command=first`.
	second := writeScript(t, dir, "second.sh", `#!/bin/sh
input=$(cat)
case "$input" in
  *'"command":"first"'*)
    echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"command":"second"}}}'
    ;;
  *)
    echo '{}'  # if the chain broke, emit nothing
    ;;
esac
`)
	reg := NewRegistry(hooksGetter(map[string][]MatcherGroup{
		"PreToolUse": {
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: first}}},
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: second}}},
		},
	}), dir)
	dec := reg.RunPreTool(context.Background(), "sess", dir, "bash", map[string]any{"command": "original"})
	if dec.UpdatedInput == nil {
		t.Fatal("expected chained updatedInput, got nil")
	}
	if got, _ := dec.UpdatedInput["command"].(string); got != "second" {
		t.Errorf("final updatedInput.command = %q, want %q (chain broken)", got, "second")
	}
}

// TestRunPreTool_DenyWinsOverAllow verifies D6 precedence: a later
// hook returning `deny` overrides a prior `allow`.
func TestRunPreTool_DenyWinsOverAllow(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	allowing := writeScript(t, dir, "allow.sh", `#!/bin/sh
cat > /dev/null
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
`)
	denying := writeScript(t, dir, "deny.sh", `#!/bin/sh
cat > /dev/null
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"forbidden"}}'
`)
	reg := NewRegistry(hooksGetter(map[string][]MatcherGroup{
		"PreToolUse": {
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: allowing}}},
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: denying}}},
		},
	}), dir)
	dec := reg.RunPreTool(context.Background(), "sess", dir, "bash", map[string]any{})
	if !dec.Block {
		t.Fatalf("expected Block=true; got Block=%v ExplicitAllow=%v", dec.Block, dec.ExplicitAllow)
	}
	if dec.BlockReason != "forbidden" {
		t.Errorf("BlockReason = %q, want %q", dec.BlockReason, "forbidden")
	}
	if dec.ExplicitAllow {
		t.Errorf("ExplicitAllow must clear when a later hook denies")
	}
}

// TestRunPreTool_OneHookFailsOthersStillRun verifies non-blocking error
// isolation — a hook that exits 1 doesn't prevent the next from running.
func TestRunPreTool_OneHookFailsOthersStillRun(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	failing := writeScript(t, dir, "fail.sh", `#!/bin/sh
cat > /dev/null
echo 'transient' >&2
exit 1
`)
	working := writeScript(t, dir, "work.sh", `#!/bin/sh
cat > /dev/null
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"command":"rewritten"}}}'
`)
	reg := NewRegistry(hooksGetter(map[string][]MatcherGroup{
		"PreToolUse": {
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: failing}}},
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: working}}},
		},
	}), dir)
	dec := reg.RunPreTool(context.Background(), "sess", dir, "bash", map[string]any{})
	if dec.UpdatedInput == nil {
		t.Fatal("second hook never ran; first hook's failure cascaded")
	}
	if got, _ := dec.UpdatedInput["command"].(string); got != "rewritten" {
		t.Errorf("updatedInput.command = %q, want %q", got, "rewritten")
	}
}

// TestRunPreTool_NoMatchingHooksReturnsEmptyDecision verifies that a tool
// outside the matcher set produces a zero-value decision.
func TestRunPreTool_NoMatchingHooksReturnsEmptyDecision(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(hooksGetter(map[string][]MatcherGroup{
		"PreToolUse": {
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: "/never/runs"}}},
		},
	}), dir)
	dec := reg.RunPreTool(context.Background(), "sess", dir, "edit", map[string]any{})
	if dec.Block || dec.UpdatedInput != nil || dec.ExplicitAllow {
		t.Errorf("expected zero decision for non-matching tool; got %+v", dec)
	}
}

// TestRunPostTool_UpdatedOutputApplies verifies the PostToolUse output-
// replacement path used by RTK to compact verbose tool output.
func TestRunPostTool_UpdatedOutputApplies(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	redactor := writeScript(t, dir, "redact.sh", `#!/bin/sh
cat > /dev/null
echo '{"hookSpecificOutput":{"hookEventName":"PostToolUse","updatedToolOutput":"redacted"}}'
`)
	reg := NewRegistry(hooksGetter(map[string][]MatcherGroup{
		"PostToolUse": {
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: redactor}}},
		},
	}), dir)
	dec := reg.RunPostTool(context.Background(), "sess", dir, "bash", map[string]any{"command": "cat large"}, "huge multi-line output")
	if dec.UpdatedOutput == nil {
		t.Fatal("expected UpdatedOutput, got nil")
	}
	if *dec.UpdatedOutput != "redacted" {
		t.Errorf("UpdatedOutput = %q, want %q", *dec.UpdatedOutput, "redacted")
	}
}

// TestRunPostTool_EmptyStringSuppression verifies that a hook can fully
// suppress noisy tool output by emitting `"updatedToolOutput": ""`. Empty
// string must be distinguishable from "field absent" — the JSON schema is
// `*string`, so nil = no change, &"" = replace with empty.
func TestRunPostTool_EmptyStringSuppression(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	suppressor := writeScript(t, dir, "suppress.sh", `#!/bin/sh
cat > /dev/null
echo '{"hookSpecificOutput":{"hookEventName":"PostToolUse","updatedToolOutput":""}}'
`)
	reg := NewRegistry(hooksGetter(map[string][]MatcherGroup{
		"PostToolUse": {
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: suppressor}}},
		},
	}), dir)
	dec := reg.RunPostTool(context.Background(), "sess", dir, "bash", map[string]any{}, "noisy 200-line output")
	if dec.UpdatedOutput == nil {
		t.Fatal("expected UpdatedOutput to be non-nil (explicit empty string), got nil")
	}
	if *dec.UpdatedOutput != "" {
		t.Errorf("UpdatedOutput = %q, want empty string", *dec.UpdatedOutput)
	}
}

// TestRunPostTool_AbsentFieldKeepsOriginal verifies the complementary case:
// when `updatedToolOutput` is OMITTED from the JSON (not just empty), the
// original output passes through unchanged. Guards against a regression
// that would treat absent-and-empty identically.
func TestRunPostTool_AbsentFieldKeepsOriginal(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	noop := writeScript(t, dir, "noop.sh", `#!/bin/sh
cat > /dev/null
echo '{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"just-context"}}'
`)
	reg := NewRegistry(hooksGetter(map[string][]MatcherGroup{
		"PostToolUse": {
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: noop}}},
		},
	}), dir)
	dec := reg.RunPostTool(context.Background(), "sess", dir, "bash", map[string]any{}, "ORIGINAL")
	if dec.UpdatedOutput != nil {
		t.Errorf("absent updatedToolOutput must leave UpdatedOutput nil; got %q", *dec.UpdatedOutput)
	}
	if dec.AdditionalContext != "just-context" {
		t.Errorf("additionalContext = %q, want %q", dec.AdditionalContext, "just-context")
	}
}

// TestRunPostTool_FirstBlockReasonWins verifies the consistency fix:
// PostToolUse multi-hook block precedence matches PreToolUse's
// first-deny-wins (registry.go::applyExit). Without the guard a later
// exit-2 hook silently overwrites the first reason.
func TestRunPostTool_FirstBlockReasonWins(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	first := writeScript(t, dir, "first.sh", `#!/bin/sh
cat > /dev/null
echo 'first reason' >&2
exit 2
`)
	second := writeScript(t, dir, "second.sh", `#!/bin/sh
cat > /dev/null
echo 'second reason' >&2
exit 2
`)
	reg := NewRegistry(hooksGetter(map[string][]MatcherGroup{
		"PostToolUse": {
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: first}}},
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: second}}},
		},
	}), dir)
	dec := reg.RunPostTool(context.Background(), "sess", dir, "bash", map[string]any{}, "original")
	if dec.BlockReason != "first reason" {
		t.Errorf("BlockReason = %q, want %q (first-block-wins)", dec.BlockReason, "first reason")
	}
}

// TestRunPostTool_Exit2ReplacesOutputWithStderr verifies the documented
// PostToolUse "block" semantic: exit-2 substitutes stderr for the tool's
// output.
func TestRunPostTool_Exit2ReplacesOutputWithStderr(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	blocker := writeScript(t, dir, "block.sh", `#!/bin/sh
cat > /dev/null
echo 'sanitization failed' >&2
exit 2
`)
	reg := NewRegistry(hooksGetter(map[string][]MatcherGroup{
		"PostToolUse": {
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: blocker}}},
		},
	}), dir)
	dec := reg.RunPostTool(context.Background(), "sess", dir, "bash", map[string]any{}, "original output")
	if dec.BlockReason != "sanitization failed" {
		t.Errorf("BlockReason = %q, want %q", dec.BlockReason, "sanitization failed")
	}
}

// TestRunPreTool_Exit2BlocksWithStderr verifies the documented PreToolUse
// "block" semantic in the synthesized-deny path: exit-2 with stderr text
// becomes a Block=true decision whose BlockReason is the stderr content.
// Complements the runner-layer exit-2 test by exercising the decision
// synthesis applyExit performs when no JSON is emitted.
func TestRunPreTool_Exit2BlocksWithStderr(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	blocker := writeScript(t, dir, "block.sh", `#!/bin/sh
cat > /dev/null
echo 'command is unsafe' >&2
exit 2
`)
	reg := NewRegistry(hooksGetter(map[string][]MatcherGroup{
		"PreToolUse": {
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: blocker}}},
		},
	}), dir)
	dec := reg.RunPreTool(context.Background(), "sess", dir, "bash", map[string]any{})
	if !dec.Block {
		t.Fatalf("expected Block=true on exit 2; got Block=%v", dec.Block)
	}
	if dec.BlockReason != "command is unsafe" {
		t.Errorf("BlockReason = %q, want %q (stderr verbatim minus trailing newline)", dec.BlockReason, "command is unsafe")
	}
	if dec.ExplicitAllow {
		t.Errorf("ExplicitAllow must stay false on exit-2 block")
	}
}

// TestRunPreTool_PreservesLargeIntegerPrecision verifies cloneMap +
// updatedInput chaining doesn't downcast 64-bit integers to float64
// (the standard map[string]any JSON pitfall). A hook that no-ops on
// `tool_input` and re-emits it as `updatedInput` must round-trip the
// numeric value byte-for-byte — otherwise IDs and timestamps mangle
// past 2^53.
func TestRunPreTool_PreservesLargeIntegerPrecision(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	// Hook reads stdin, extracts tool_input verbatim, and emits it as
	// updatedInput. If json.Number is being used, the integer survives.
	noop := writeScript(t, dir, "noop.sh", `#!/bin/sh
input=$(cat)
ti=$(printf '%s' "$input" | sed -n 's/.*"tool_input":\({[^}]*}\).*/\1/p')
printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":%s}}\n' "$ti"
`)
	reg := NewRegistry(hooksGetter(map[string][]MatcherGroup{
		"PreToolUse": {
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: noop}}},
		},
	}), dir)
	const bigID int64 = 1750000000000000001
	dec := reg.RunPreTool(context.Background(), "sess", dir, "bash", map[string]any{
		"id": bigID,
	})
	if dec.UpdatedInput == nil {
		t.Fatal("expected updatedInput, got nil (no-op hook didn't echo back)")
	}
	// json.Number stringifies as the original literal; convert to int64
	// to compare. A plain float64 would lose precision well before this
	// magnitude.
	gotNum, ok := dec.UpdatedInput["id"].(json.Number)
	if !ok {
		t.Fatalf("expected json.Number, got %T (%v) — round-trip is downcasting to float64", dec.UpdatedInput["id"], dec.UpdatedInput["id"])
	}
	got, err := gotNum.Int64()
	if err != nil {
		t.Fatalf("Int64 conversion failed: %v", err)
	}
	if got != bigID {
		t.Errorf("integer mangled in round-trip: got %d, want %d", got, bigID)
	}
}

// TestRunPreTool_CaseInsensitiveEventKey locks in the registry's
// workaround for viper's key case-folding. The production config
// loader (viper) lowercases `"PreToolUse"` to `"pretooluse"` in
// Config.Hooks. The registry must still resolve the lookup so hooks
// fire. A regression here means hooks silently stop firing when
// loaded from `.opencode.json`.
func TestRunPreTool_CaseInsensitiveEventKey(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "ok.sh", `#!/bin/sh
cat > /dev/null
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"command":"rewritten"}}}'
`)
	// Note the lowercased event key — simulating viper's mangling.
	reg := NewRegistry(hooksGetter(map[string][]MatcherGroup{
		"pretooluse": {
			{Matcher: "bash", Hooks: []HookEntry{{Type: "command", Command: script}}},
		},
	}), dir)
	dec := reg.RunPreTool(context.Background(), "sess", dir, "bash", map[string]any{"command": "orig"})
	if dec.UpdatedInput == nil {
		t.Fatal("hook did not fire — case-insensitive event lookup broken")
	}
	if got, _ := dec.UpdatedInput["command"].(string); got != "rewritten" {
		t.Errorf("updatedInput.command = %q, want %q", got, "rewritten")
	}
}

// TestRunPreTool_NoHooksMapDegradesGracefully verifies the "hooks
// disabled" common case: the getter returns nil → registry returns
// zero decision immediately, no subprocesses spawned.
func TestRunPreTool_NoHooksMapDegradesGracefully(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(func() map[string][]MatcherGroup { return nil }, dir)
	dec := reg.RunPreTool(context.Background(), "sess", dir, "bash", map[string]any{})
	if dec.Block || dec.UpdatedInput != nil || dec.ExplicitAllow {
		t.Errorf("expected zero decision when getter returns nil; got %+v", dec)
	}
}

// TestRunPreTool_NilGetterDegradesGracefully verifies the defensive
// path where a Registry is constructed with a nil getter — exercised
// by app.New when project root resolution fails.
func TestRunPreTool_NilGetterDegradesGracefully(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(nil, dir)
	dec := reg.RunPreTool(context.Background(), "sess", dir, "bash", map[string]any{})
	if dec.Block || dec.UpdatedInput != nil || dec.ExplicitAllow {
		t.Errorf("expected zero decision with nil getter; got %+v", dec)
	}
}

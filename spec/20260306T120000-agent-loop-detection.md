# Agent Tool-Call Loop Detection

**Date**: 2026-03-06
**Status**: Implemented
**Author**: AI-assisted

## Overview

Add loop detection to the agent's `processGeneration` loop to detect when the model repeatedly calls the same tool with the same arguments, and force-terminate the loop with an error message injected as a tool result. Provide a hard turn cap configurable via CLI flag, agent config, and an env variable for the repetition threshold.

## Motivation

### Current State

The agent loop in `internal/llm/agent/agent.go` is an unbounded `for {}`:

```go
cycles := 0
for {
    cycles += 1
    // ...
    agentMessage, toolResults, err = a.streamAndHandleEvents(ctx, sessionID, msgHistory, toolSet)
    // ...
    if agentMessage.FinishReason() == message.FinishReasonToolUse {
        msgHistory = append(msgHistory, agentMessage, *toolResults)
        preserveTail = true
        continue  // ← no exit condition besides finish reason
    }
    return AgentEvent{...}
}
```

The `cycles` counter is logged but never checked against any limit. The only exits are: non-tool-use finish reason, context cancellation, or hard error.

### Problems

1. **Compaction-induced amnesia loop**: When auto-compaction fires, the conversation summary loses the model's plan/intent. The model reads the summary, re-derives the same plan, calls the same tool — endlessly. This was observed in production: a workhorse subagent called `git show HEAD:...Test.kt | grep -B2 -A 15 "coVerify" | head -60` with the identical text "Let me look at the cache library source on GitLab..." over **20+ consecutive cycles** (seq 500→528+), each producing the exact same output.

2. **No safeguard for any repetition pattern**: Even without compaction, a model can get stuck retrying a command that returns unhelpful output (truncated, empty, or not what it expected). Safe read-only commands like `git show` bypass permission checks entirely, so there's no human gate to break the cycle.

3. **Silent cost burn**: Each cycle burns API tokens and wall time with zero progress. Subagents have no timeout, so the parent agent blocks indefinitely on `<-done`.

### Desired State

The agent detects when consecutive tool calls repeat and injects an error tool result that forces the model to change strategy or terminate. Turn limits are configurable at multiple levels with sensible defaults.

## Research Findings

### Observed Loop Pattern

From the DB dump, the loop has these characteristics:

- **Exact repetition**: Every assistant message contains the same text prefix AND the same tool call (same `name` + same `input` JSON, byte-for-byte)
- **Successful execution**: Every tool result returns exit code 0 with the same content — this is not a retry-on-error pattern
- **Post-compaction restart**: The model's text always starts with "Let me look at the cache library source..." — it's re-deriving the same plan from the compacted summary each time
- **High frequency**: ~8-10 seconds per cycle (model inference + tool execution), so 20+ cycles = ~3+ minutes of wasted compute

### How Other Systems Handle This

| System | Approach | Granularity |
|--------|----------|-------------|
| Claude Code | Max tool-use turns limit | Global counter |
| Cursor | Timeout + max iterations | Per-request |
| Aider | Git diff check — no progress = stop | Outcome-based |
| AutoGPT | Cycle budget per task | Global counter |

**Key finding**: Most systems use a simple max-iterations cap. Repetition detection (comparing tool call inputs) is less common but more precise — it avoids cutting off productive long-running loops while catching genuine stuck loops.

**Implication**: A hybrid approach works best — detect repeated identical calls (precise) AND have a hard cycle cap (safety net).

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Detection method | Compare last N tool calls for identical (name, input) pairs | Precise: only triggers on actual repetition, not just long loops |
| Repetition threshold default | 3 consecutive identical tool calls | Generous enough to allow genuine retries (e.g., after a transient error), strict enough to catch loops before they waste too many tokens |
| Repetition threshold override | `OPENCODE_MAX_REPEAT_CALLS` env variable | Allows tuning without config file changes; useful for debugging or CI environments |
| Response to detection | Inject error tool result, continue loop | Gives the model a chance to recover with a different approach rather than hard-terminating |
| Hard turn cap — CLI flag | `--max-turns` (int, default 100) | Global override for all agents in the process; useful for non-interactive/batch runs |
| Hard turn cap — agent config | `maxTurns` field on `AgentInfo` (int, optional, default 100) | Per-agent granularity; subagents can have lower limits than primary agents |
| Turn cap precedence | CLI flag > agent config > default (100) | CLI flag is an explicit user intent and wins; agent config allows per-agent tuning |
| Validation | Must be > 0 (both CLI and config); error on invalid | Prevents accidental infinite loops from misconfiguration |
| Scope | Per-tool-call within a single `processGeneration` invocation | Resets naturally on each user message |
| What to compare | Tool name + tool input (JSON string) | The tool call ID changes each time, so we compare the semantically meaningful fields |
| Multiple tool calls per turn | Track each tool call independently | A turn may invoke multiple tools; detect repetition per-tool-name |

## Architecture

### Configuration Layering

```
Priority (highest → lowest):
┌─────────────────────────────────────┐
│ 1. CLI: --max-turns 50              │  Global override for the process
├─────────────────────────────────────┤
│ 2. Agent config: maxTurns: 75       │  Per-agent in .opencode.json or
│    (AgentInfo / markdown frontmatter)│  agent markdown frontmatter
├─────────────────────────────────────┤
│ 3. Default: 100                     │  Hardcoded constant
└─────────────────────────────────────┘

Repetition threshold:
┌─────────────────────────────────────┐
│ OPENCODE_MAX_REPEAT_CALLS=5         │  Env variable override
├─────────────────────────────────────┤
│ Default: 3                          │  Hardcoded constant
└─────────────────────────────────────┘
```

### Loop Detection Flow

```
processGeneration loop
─────────────────────
CYCLE N:
  1. Model returns tool_use with calls: [{name: "bash", input: "..."}]
  2. For each tool call:
     a. Check: same (name, input) as last N calls for this name?
     b. If repeated >= threshold:
        → Replace tool result with error message
        → Do NOT execute the tool
     c. Otherwise: execute normally, record (name, input) in history
  3. Check: cycles >= effectiveMaxTurns?
     → If yes: inject error, let model produce final response
  4. Continue loop
```

### Data Structure

```go
type callTracker struct {
    lastCall    map[string]string  // toolName → last input
    streakCount map[string]int     // toolName → consecutive identical count
    threshold   int                // from env or default
}
```

When a tool call comes in:
- If `lastCall[name] == input`: increment `streakCount[name]`
- Else: reset `streakCount[name] = 1`, update `lastCall[name] = input`
- If `streakCount[name] >= threshold`: return error instead of executing

### Effective Max Turns Resolution

```go
func resolveMaxTurns(cliMaxTurns int, agentMaxTurns int) int {
    if cliMaxTurns > 0 {
        return cliMaxTurns   // CLI flag wins
    }
    if agentMaxTurns > 0 {
        return agentMaxTurns // Agent config
    }
    return DefaultMaxTurns   // 100
}
```

## Implementation Plan

### Phase 1: Configuration

- [x] **1.1** Add `--max-turns` flag to `cmd/root.go` — `int`, default `0` (meaning "use agent config or default"). Validate > 0 when explicitly set. Pass through to the agent via context or config.
- [x] **1.2** Add `MaxTurns int` field to `AgentInfo` in `internal/agent/registry.go` with `yaml:"maxTurns,omitempty"` and corresponding `json:"maxTurns,omitempty"` in `config.Agent`. Add validation in `validateAgent()`: if set, must be > 0; log warning and reset to default if invalid.
- [x] **1.3** Read `OPENCODE_MAX_REPEAT_CALLS` env variable at startup in agent.go (or config). Parse as int, validate > 0, fall back to `DefaultRepeatThreshold = 3`.

### Phase 2: Loop Detection

- [x] **2.1** Add `callTracker` struct in `internal/llm/agent/loop_detection.go` (or a small helper file) with methods `Track(name, input string) bool` (returns true if loop detected) and `Reset()`.
- [x] **2.2** Integrate into `streamAndHandleEvents` or the tool execution section — before executing each tool call, check `tracker.Track()`. If loop detected, return an error tool result: `"Loop detected: you have called this tool with identical arguments N times consecutively. The output will not change. Please try a different approach, use different arguments, or provide your final response."`
- [x] **2.3** Add effective max turns check: resolve from CLI flag → agent config → default. Check `cycles >= effectiveMaxTurns` in the main loop. If exceeded, inject a tool result telling the model to wrap up, then do one more model call to get a final response.
- [x] **2.4** Add logging: `logging.Warn("Tool call loop detected", "tool", name, "streak", count, "session_id", sessionID)` and `logging.Warn("Max turns reached", "turns", cycles, "max", effectiveMaxTurns, "session_id", sessionID)`

### Phase 3: Tests

- [x] **3.1** Unit test `callTracker`: verify streak counting, reset on different input, independence between tool names, env variable override
- [x] **3.2** Unit test `resolveMaxTurns`: CLI > agent config > default precedence
- [x] **3.3** Validation tests: `maxTurns` rejects negative values in CLI (`--max-turns -1` errors) and config (negative values reset to 0)
- [x] **3.4** Integration-style test: simulated multi-cycle agent loop with multiple tools, verifies loop terminates after threshold; max turns integration test verifies cycle cap

### Phase 4: Schema & Docs

- [x] **4.1** Regenerate `opencode-schema.json` to include `maxTurns` in agent config
- [x] **4.2** Updated `AGENTS.md` agent config docs with new `maxTurns` field

### Phase 5: Observability (optional follow-up)

- [ ] **5.1** Emit a pubsub event on loop detection so TUI can display a warning
- [ ] **5.2** Consider adding loop detection stats to session metadata

## Edge Cases

### Multiple tools in one turn

1. Model returns 3 tool calls: bash (repeated), read (new), bash (repeated with different args)
2. Each tool name tracked independently
3. Only the first bash call triggers loop detection if its streak exceeds threshold

### Compaction resets context but not tracker

1. Auto-compaction fires, model loses memory
2. Model re-derives same plan, calls same tool
3. Tracker persists across compaction (it's a local variable in `processGeneration`), so it correctly detects the repetition
4. This is the exact scenario from the bug report — tracker catches it

### Legitimate retries

1. A tool call fails with a transient error (e.g., network timeout)
2. Model retries the same call — streak count increases
3. With threshold=3, the model gets 2 retries before loop detection kicks in
4. The error message tells it to try a different approach

### MaxTurns reached during productive work

1. Agent is genuinely making progress but hits the turn cap
2. The injected message asks it to wrap up, not hard-terminate
3. Model gets one more turn to produce a summary/final response

### CLI flag vs agent config conflict

1. CLI `--max-turns 50` is set, agent config has `maxTurns: 200`
2. CLI flag wins (value 50) — it's an explicit user override for this run
3. Without CLI flag, agent config value (200) is used

### Invalid maxTurns in config

1. User sets `maxTurns: -1` or `maxTurns: 0` in `.opencode.json` or markdown frontmatter
2. `validateAgent()` logs a warning and resets to default (100)
3. Agent continues with valid configuration

### Invalid --max-turns on CLI

1. User passes `--max-turns 0` or `--max-turns -5`
2. Cobra flag validation rejects with an error message before the app starts

### OPENCODE_MAX_REPEAT_CALLS edge cases

1. Env set to `0` or negative → log warning, use default (3)
2. Env set to `1` → every repeated call triggers immediately (aggressive but valid)
3. Env not set → use default (3)

## Open Questions

1. **Should we also detect near-identical calls (fuzzy matching)?**
   - e.g., same command but with slightly different `head -60` vs `head -80`
   - **Recommendation**: Defer. Exact matching catches the observed pattern. Fuzzy matching adds complexity and false positives.

2. **Should we track tool call hashes instead of full input strings?**
   - Full strings are simpler to debug but use more memory
   - **Recommendation**: Use full strings. Tool inputs are typically small (< 1KB), and we only store 1 per tool name.

3. **How should the CLI flag be threaded to subagents?**
   - Option A: Subagents inherit the CLI flag value from the parent process (natural — it's process-wide)
   - Option B: Subagents always use their own `maxTurns` config, ignoring CLI
   - **Recommendation**: Option A — CLI flag is a process-level safety net. Subagents can still have lower per-agent `maxTurns` if the CLI flag is not set.

## Success Criteria

- [ ] Agent terminates the observed `git show` loop pattern within 3 repeated calls (or custom `OPENCODE_MAX_REPEAT_CALLS` value) instead of running indefinitely
- [ ] Agent terminates any unbounded loop after effective max turns (default 100)
- [ ] `--max-turns` CLI flag overrides agent config and default
- [ ] `maxTurns` agent config field works in `.opencode.json`, markdown frontmatter, and YAML
- [ ] `OPENCODE_MAX_REPEAT_CALLS` env variable adjusts the repetition threshold
- [ ] Validation rejects `maxTurns <= 0` in both CLI and config with clear error/warning
- [ ] Productive multi-tool loops (e.g., edit → test → fix cycles with varying inputs) are unaffected
- [ ] Loop detection message gives the model enough context to recover (try different approach or provide final response)
- [ ] All existing agent tests pass
- [ ] New unit tests cover `callTracker`, `resolveMaxTurns`, and validation logic

## References

- `cmd/root.go` — CLI flag registration (lines 384-415)
- `internal/agent/registry.go` — `AgentInfo` struct, `validateAgent()`
- `internal/config/config.go` — `Agent` config struct, `validateAgent()` (lines 561-711)
- `internal/llm/agent/agent.go` — `processGeneration` loop (lines 274-449), `performSynchronousCompaction` (lines 811-889)
- `internal/llm/agent/prompts/compcation.md` — compaction prompt (contributes to amnesia pattern)
- `internal/llm/tools/bash.go` — safe read-only commands list, output truncation
- `internal/llm/tools/tools.go` — `BaseTool` interface, `ToolResponse` type

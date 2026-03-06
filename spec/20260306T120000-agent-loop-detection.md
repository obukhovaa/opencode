# Agent Tool-Call Loop Detection

**Date**: 2026-03-06
**Status**: Draft
**Author**: AI-assisted

## Overview

Add loop detection to the agent's `processGeneration` loop to detect when the model repeatedly calls the same tool with the same arguments, and force-terminate the loop with an error message injected as a tool result.

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

The agent detects when consecutive tool calls repeat and injects an error tool result that forces the model to change strategy or terminate.

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
| Repetition threshold | 3 consecutive identical tool calls | Generous enough to allow genuine retries (e.g., after a transient error), strict enough to catch loops before they waste too many tokens |
| Response to detection | Inject error tool result, continue loop | Gives the model a chance to recover with a different approach rather than hard-terminating |
| Hard cycle cap | 200 cycles | Safety net for non-repetitive but unproductive loops; existing `cycles` counter already tracked |
| Scope | Per-tool-call within a single `processGeneration` invocation | Resets naturally on each user message |
| What to compare | Tool name + tool input (JSON string) | The tool call ID changes each time, so we compare the semantically meaningful fields |
| Multiple tool calls per turn | Track each tool call independently | A turn may invoke multiple tools; detect repetition per-tool-name |

## Architecture

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
  3. Check: cycles >= maxCycles?
     → If yes: inject error, let model produce final response
  4. Continue loop
```

Data structure for tracking:

```
recentCalls: map[toolName][]toolInput  (ring buffer of last N inputs per tool name)
```

Alternatively, since we only need to detect N consecutive identical calls, a simpler structure:

```
type callTracker struct {
    lastCall  map[string]string  // toolName → last input
    streakCount map[string]int   // toolName → consecutive identical count
}
```

When a tool call comes in:
- If `lastCall[name] == input`: increment `streakCount[name]`
- Else: reset `streakCount[name] = 1`, update `lastCall[name] = input`
- If `streakCount[name] >= threshold`: return error instead of executing

## Implementation Plan

### Phase 1: Loop Detection

- [ ] **1.1** Add `callTracker` struct in `internal/llm/agent/agent.go` (or a small helper file) with methods `Track(name, input string) bool` (returns true if loop detected) and `Reset()`
- [ ] **1.2** Integrate into `streamAndHandleEvents` or the tool execution section — before executing each tool call, check `tracker.Track()`. If loop detected, return an error tool result: `"Loop detected: you have called this tool with identical arguments N times consecutively. The output will not change. Please try a different approach, use different arguments, or provide your final response."`
- [ ] **1.3** Add `MaxCycles = 200` constant and check `cycles >= MaxCycles` in the main loop. If exceeded, inject a system-like tool result telling the model to wrap up, then do one more model call to get a final response.
- [ ] **1.4** Add logging: `logging.Warn("Tool call loop detected", "tool", name, "streak", count, "session_id", sessionID)`

### Phase 2: Tests

- [ ] **2.1** Unit test `callTracker`: verify streak counting, reset on different input, independence between tool names
- [ ] **2.2** Integration-style test: mock a provider that always returns the same tool call, verify the loop terminates after threshold

### Phase 3: Observability (optional follow-up)

- [ ] **3.1** Emit a pubsub event on loop detection so TUI can display a warning
- [ ] **3.2** Consider adding loop detection stats to session metadata

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

### MaxCycles reached during productive work

1. Agent is genuinely making progress but hits 200 cycles
2. The injected message asks it to wrap up, not hard-terminate
3. Model gets one more turn to produce a summary/final response

## Open Questions

1. **Should the repetition threshold be configurable?**
   - Could be an agent-level config field (e.g., `maxRepeatCalls: 5`)
   - **Recommendation**: Start with hardcoded constant (3), make configurable later if needed

2. **Should we also detect near-identical calls (fuzzy matching)?**
   - e.g., same command but with slightly different `head -60` vs `head -80`
   - **Recommendation**: Defer. Exact matching catches the observed pattern. Fuzzy matching adds complexity and false positives.

3. **Should the max cycles limit differ between primary agents and subagents?**
   - Subagents should arguably have a lower limit since they have scoped tasks
   - **Recommendation**: Start with a single constant. Subagent-specific limits can be added via agent config later.

4. **Should we track tool call hashes instead of full input strings?**
   - Full strings are simpler to debug but use more memory
   - **Recommendation**: Use full strings. Tool inputs are typically small (< 1KB), and we only store 1 per tool name.

## Success Criteria

- [ ] Agent terminates the observed `git show` loop pattern within 3 repeated calls instead of running indefinitely
- [ ] Agent terminates any unbounded loop after 200 cycles maximum
- [ ] Productive multi-tool loops (e.g., edit → test → fix cycles with varying inputs) are unaffected
- [ ] Loop detection message gives the model enough context to recover (try different approach or provide final response)
- [ ] All existing agent tests pass
- [ ] New unit tests cover the `callTracker` logic

## References

- `internal/llm/agent/agent.go` — `processGeneration` loop (lines 274-449), `performSynchronousCompaction` (lines 811-889)
- `internal/llm/agent/prompts/compcation.md` — compaction prompt (contributes to amnesia pattern)
- `internal/llm/tools/bash.go` — safe read-only commands list, output truncation
- `internal/llm/tools/tools.go` — `BaseTool` interface, `ToolResponse` type

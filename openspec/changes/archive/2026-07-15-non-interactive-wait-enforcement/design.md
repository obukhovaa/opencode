## Context

The `non-interactive-task-wait` feature (archived 2026-06-29) is fully deployed (opencode `v0.13.5`, c2-agent image `0.1.53`) and its notification write path is correct. Incident CD-4761 (root-caused via a 9-agent investigation workflow, 2026-07-14) showed the *enforcement* half is bypassable: the deterministic wait is only reached at a natural end-of-turn, and a model that keeps issuing tool calls (foreground `bash sleep` polling) never reaches it. This design closes that gap without touching the delivery path.

The runtime facts this design is built on (verified against the deployed code):

- `internal/llm/agent/agent.go`: inner agentic loop `continue`s on `FinishReasonToolUse` (:894); breaks only on a non-tool-use finish (:902); the non-interactive wait (`PendingForSession` :917 → `WaitForActiveTasks` :927 → `injectWaitTimeoutNote` :934) sits AFTER that break, gated by `if !opts.NonInteractive` (:910).
- `opts.NonInteractive` (agent.go:89) is visible only inside the agent; it is NOT on the tool-execution `ctx`, so `bashTool.Run(ctx, call)` cannot see it today.
- Foreground bash is capped at `DefaultTimeout` 2min / `MaxTimeout` 10min (bash.go:56-57); the shell SIGTERMs children on ctx cancel (shell/shell.go:200).
- `task async` subagents run on `context.WithCancel(context.Background())` (agent-tool-async.go:46) — deliberately detached from the parent's per-turn ctx so a turn ending does not kill them, but consequently unbounded by the step deadline.
- The no-poll guidance exists only in `CoderPrompt` (coder.go), which `prompt.go:317` skips whenever `info.Prompt != ""` (custom-prompt agents like `composer-developer`).

## Goals / Non-Goals

**Goals**
- Make the non-interactive hold-the-turn guarantee non-bypassable: a model CANNOT burn wall-clock self-waiting while background tasks are pending.
- Bound detached subagent lifetime by the step deadline.
- Deliver the no-poll contract to every agent regardless of custom prompt.
- Preserve interactive behavior byte-for-byte; no tool-schema or notification-shape changes.

**Non-Goals**
- General poll-pattern detection beyond a leading `sleep`.
- Cross-session waits; subprocess reaping on cancel.
- Changing the notification write path.

## Decisions

### D1. Anti-spin via foreground-`sleep` interception (primary mechanism)

**Decision:** In non-interactive mode, when the session has pending non-monitor background tasks (bash or task), the `bash` tool intercepts a foreground command whose sole effect is a wall-clock wait (leading `sleep`, optionally trailing `echo`) and, instead of executing it, calls `reg.WaitForActiveTasks(ctx, sessionID, WaitOptions{IncludeMonitor: false})`, returning a synthetic bash-style result enumerating the tasks that reached terminal state during the wait. Long-lived monitors are excluded so a stray `sleep` is not converted into a block on a monitor's whole lifetime (that bounding belongs to the end-of-turn drain).

**Why not "wait after every inner cycle"?** Blocking after every tool-use turn would stall legitimate interleaved work (the agent may correctly do other useful things while tasks run). The harm in CD-4761 was specifically the *wall-clock sleep*, which is never useful in non-interactive mode when the runtime already owns waiting. Intercepting `sleep` is targeted, low-risk, and converts the model's exact observed anti-pattern into the correct primitive.

**Why not just block `sleep` with an error?** Returning an error ("do not sleep") leaves the model to choose the next action and may re-spin on `read .out`. Converting the sleep into the real wait makes the model's intent (wait for the work) succeed deterministically and returns it a useful, factual completion summary — so the next turn is productive.

**Detection scope:** the command, after trimming, consists only of `sleep <n>` optionally followed by `;`/`&&` and a single `echo …`. Anything else runs normally. Conservative on purpose — false positives would only ever convert a genuine idle sleep into an equivalent-or-shorter wait, and only when tasks are already pending.

**Duration:** the requested sleep duration is ignored; the wait drains all pending tasks, bounded solely by the caller's ctx (step timeout). This is stronger than the model asked for and is the desired behavior. It is also strictly better than today, where the sleep is killed at `DefaultTimeout` (2min) regardless.

### D2. Propagate the non-interactive signal onto the tool ctx

**Decision:** `processGeneration` wraps the tool-execution ctx with `context.WithValue` carrying a `nonInteractive` marker before dispatching tool calls. The bash tool reads it — together with the sessionID it ALREADY obtains via `GetContextValues(ctx)` (`bash.go:255`, used the same way across ~10 tools) — to decide whether D1 applies and to query `reg.PendingForSession(sessionID)`. Only the `nonInteractive` marker is new; sessionID is not a new channel. Prefer extending the existing `GetContextValues` accessor (or adding a sibling) so the marker travels the same path as sessionID/messageID.

**Alternative rejected:** bake the flag into a per-run tool instance. `RunWith` options vary per call while tool instances are shared across calls, so a ctx value is the least invasive carrier and matches how sessionID/messageID already reach tools.

### D3. Drain-to-empty loop in the agent (not in the registry primitive)

**Decision:** Keep `WaitForActiveTasks` snapshot-at-start (unchanged). In `agent.go`, after the wait returns `nil`, re-read `PendingForSession(sessionID)`; if non-empty (tasks were spawned after the snapshot, e.g. a later fan-out wave), loop and wait again. Bound the loop by the same ctx and the existing `outerCycles`/`effectiveMaxTurns` guard. On `ctx.Err()`, keep the existing `injectWaitTimeoutNote` behavior.

**Why here:** the primitive's snapshot semantics are relied on elsewhere and are correct for a single call. Draining is an agent-loop concern. This makes the guarantee cover the CD-4761 pattern (20 subagents spawned across many turns) without a semantics change to the primitive.

### D4. Step-scoped context for detached subagents

**Decision:** The flow runner already builds a per-step ctx with the timeout (`stepCtx`, from the archived change). Today only the per-turn `RunWith` ctx reaches the async spawn, and the spawn discards it for `context.Background()`. Introduce a *step-scoped* ctx that (a) is NOT cancelled when a single turn ends, but (b) IS cancelled when the step deadline elapses or the step completes. Thread it to `agent-tool-async.go` as the base for `runCtx` (still wrapped with a per-task `WithCancel` for `taskstop`).

**Consequence:** a subagent survives the parent turn ending (preserving the async contract) but a timed-out step cancels all its subagents — no more ~20 unbounded runs outliving the job. Cost rollup + `StatusFailed` completion on cancel are unchanged (agent-tool-async.go already maps a context-cancellation error to `StatusFailed`; `StatusKilled` stays reserved for `taskstop`).

**Care:** the subagent ctx must derive from the step ctx, not the turn ctx, so the existing "turn ending must not cancel the subagent" property (the reason `Background()` was chosen) still holds. This is the crux of the change and needs an explicit test (D4 test in tasks).

### D5. No-poll guidance is prompt-independent

**Decision:** Move the "# Background tasks (event-driven, no polling)" contract out of `CoderPrompt`-only and into a block that `prompt.go` appends unconditionally (alongside `taskToolReportingPrompt`), so custom-prompt agents receive it. Separately, reword the `task async` ack (agent-tool-async.go:79-82): keep `output_file` for resume/inspection semantics but stop framing it as "inspect progress"; state plainly that the runtime blocks the turn until completion and the agent must not sleep/poll.

**Why belt-and-suspenders:** D1 makes polling harmless even if the model tries it, but correct guidance reduces wasted turns and duplicate spawns (the model in CD-4761 also fanned out twice). Guidance alone is insufficient (the model articulated the right intent and polled anyway), which is why D1 is the load-bearing fix and this is defense-in-depth.

## Risks / Trade-offs

- **Over-eager interception:** a legitimate short foreground `sleep` for an unrelated reason, while tasks happen to be pending, becomes a full drain wait. Mitigation: only pure-`sleep(+echo)` commands qualify; the outcome (wait for the pending work, bounded by step timeout) is the correct non-interactive behavior anyway. Interactive mode is never affected.
- **Step-ctx wiring is the highest-risk change** (D4) because it must preserve "turn end does not cancel subagent" while adding "step deadline does cancel subagent." Guarded by a dedicated unit test and the existing e2e harness.
- **Draining could extend step latency** up to `Step.Timeout` — by design; that is the point of a deterministic wait, and the timeout note bounds the worst case.

## Migration / Rollout

No schema or API changes; no data migration. Ships in the opencode fork, is picked up by the next c2-agent image bump (dockerfile pins an opencode tag). Interactive deployments see no behavioral change. Recommend validating on the `composer-developer` workspace by re-running a fan-out flow step and confirming (a) no foreground `sleep` executes while tasks are pending, (b) the step returns `struct_output` after the batch drains, (c) a forced step timeout cancels the subagents and injects the `[wait-timeout]` note.

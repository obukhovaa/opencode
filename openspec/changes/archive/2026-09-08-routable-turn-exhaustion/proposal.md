## Why

A flow step whose agent hits its turn budget (`maxTurns`) is recorded as
**successfully completed**. The agent runtime injects a wrap-up turn with
`struct_output` forced, captures the document, and returns — and nothing in that
result distinguishes "the agent finished" from "the agent was cut off mid-task".
`AgentEvent.TurnsExhausted` already carries the fact, but the flow layer
consumes it only as an *exemption* (skip a re-prompt that has no budget to
spend), never as a step outcome.

The consequence is silent data loss on any step that leaves state on the pod.
Piano job `c1f9c6506caed6cd` (`developer-react-on-jira`, CD-4956) is the
worked example: the `implement` step ran 202 generations against a
`maxTurns: 200` budget, was forced to wrap up, and reported

> "Work is COMMITTED LOCALLY but NOT YET PUSHED and NO MR EXISTS — I ran out of
> tool turns immediately after the last green test run, before `git push` / MR
> creation."

Three commits and an entire uncommitted frontend change existed on a disposable
pod. The step's `fallback.to: salvage-implement` — a step that exists precisely
to push unpushed branches before teardown — never ran, because the step had not
failed. The flow completed, the job was reported green, and the pod took the
work with it. 51 minutes of a 4h budget; no timeout, no crash, no provider
error.

Two things were missing, both of them cheap:

1. **A retry.** The retry re-enters the same session on the same pod with a
   fresh turn budget. The agent had already finished the work — it needed a
   handful of turns to `git push` and open an MR. One retry would have saved
   all of it.
2. **A route.** When retries are spent, exhaustion should reach the safety-net
   step the flow author already wired up.

## What Changes

- **New step key `fallback.on_turns_exhausted`** with values `accept` (default,
  today's behaviour) and `fail`. Validated at flow load time.
- **`fail` makes turn exhaustion a step failure**, which puts it through the
  existing fallback machinery unchanged: it consumes `fallback.retry`, and each
  attempt re-runs the step on the SAME session (same pod, same working tree)
  with a fresh turn budget. Once the budget is spent the step fails and routes
  to `fallback.to`.
- **The wrap-up document is not discarded.** Its top-level fields are merged
  into the args the `fallback.to` step inherits, so a salvage step receives what
  the cut-off run reported (which repos, which links, what it was mid-way
  through) instead of running on the inbound args alone.
- **Turn exhaustion stays out of the transient-provider-error class**, so a step
  with `resume_after` does not park and auto-resume on it. A resume runs in a
  fresh workspace — exactly where the half-finished working tree does not
  exist.

### Non-goals

- **No change to the default.** A step without the key behaves exactly as
  before; every existing flow is unaffected. This is deliberate: converting
  successful steps into failures fleet-wide, silently, is not a change anyone
  can review.
- **Not a `maxTurns` sizing fix.** Budgets remain the flow author's call.
- **No new status on the wire.** Exhaustion reuses the existing `failed` status
  and the existing `flow.step.retrying` transition — the latter published with
  a new reason string when an exhaustion consumes a fallback attempt, so an
  orchestrator is not left watching dead air for the length of a turn budget.
- **The no-output shape is untouched.** A schema-bearing run that produced
  neither a document nor prose was already a retryable failure
  (`missingStructOutputError`, which carries its own turn-exhaustion flag and
  earns a re-prompt). Every other exhausted shape — document, prose-only, or a
  step with no `output.schema` at all — becomes a failure under the opt-in,
  because each of those otherwise completes the step silently, which is the bug.

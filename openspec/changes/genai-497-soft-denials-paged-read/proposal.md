# Soft Permission Denials, Paged Reads, Overflow Recovery, No Headless Titles

## Why

GENAI-497 collects four runtime defects that make unattended runs fail, or cost money, for no gain:

1. **A permission-rule deny ends the whole run.** Every tool returns `permission.ErrorPermissionDenied` from its `case permission.ActionDeny:` branch, and the agent loop treats that error as a human rejecting the prompt: it records `Permission denied`, cancels the turn's sibling calls and finishes the message with `FinishReasonPermissionDenied`, which stops the loop. A configured `deny` is policy, decided before anyone was asked, yet the model never gets to react to it. Workspaces therefore avoid `deny` rules altogether, because "a single denial terminates the entire agent".
2. **`read` refuses any file over 250KB, even with `offset`/`limit`.** The size check runs before the parameters are looked at, so a large log or a tool-output spill cannot be paged through with the tool every agent has. The spill headers had to steer agents to `grep`/`sed` instead (`webfetch-output-limit` design §6, which recorded windowing as a follow-up).
3. **A provider's "prompt is too long" fails the run.** Compaction is decided up front from the context count. When the count undershoots and the provider rejects the call, the loop logs a likely-overflow warning and returns the error, even though one compaction would have let the same turn succeed.
4. **Headless sessions generate titles nobody reads.** `processGeneration` starts a `descriptor` call on every session's first message, flow steps and `opencode -p` runs included: about 6.2k calls a month in Langfuse.

## What Changes

- **Rule denials are tool errors.** A new `permission.RuleDeniedError` (which still matches `ErrorPermissionDenied` under `errors.Is`) is what every `ActionDeny` branch returns. The agent loop reports it to the model as an error tool result that names the tool and the denied input and says the action itself is forbidden, does not cancel sibling calls, and keeps the turn going. A rejected prompt keeps today's behaviour. A per-run circuit breaker ends the run with `permission_denied` after 3 consecutive turns in which every call was rule-denied, or after 10 rule-denied calls in one run.
- **Paged reads of large files.** The 250KB ceiling applies only to a whole-file read (no `offset`, no `limit`), and its refusal says to pass them. A paged read streams the file without holding it or its longest line in memory, and returns at most 250KB of text per call with a note on where to continue. Files over the ceiling skip the LSP, whose `didOpen` sends the whole file. The spill headers and the bash tool description now point at paged reads.
- **One overflow recovery per run.** When the main model call fails with a context-overflow error (the provider says so, or the call failed with the context at 90% of the window or more), auto-compaction is on and the backoff allows it, the turn compacts once and resends the same call. A failed compaction, or a retry that fails again, returns the original error. The retry never repeats within a run and does not count as a turn.
- **No titles for unattended sessions.** Sessions marked unattended (flow steps, headless `-p`, cron jobs, and the subagents linked under them) skip title generation; interactive flow steps and every attended surface keep it.

## Capabilities

### New Capabilities

- `tool-permission-denials`: how a rule denial and a rejected prompt end differently, and the breaker bounding rule denials.
- `read-tool-paging`: the whole-file ceiling, paged reads of any size, and the per-call output bound.
- `session-title-generation`: which sessions generate a title.

### Modified Capabilities

- `auto-compaction`: a model call rejected as too long recovers through one compaction and one retry.
- `webfetch-output-limit`: the spill header's recovery guidance names paged reads.

## Impact

- Modified: `internal/permission` (new `denied.go`), every tool's `ActionDeny` branch (`internal/llm/tools/*.go`, `internal/llm/agent/mcp-tool.go`), `internal/llm/agent/agent.go` (tool-result handling, the breaker, the overflow retry, the title gate), `internal/llm/agent/compaction.go` (overflow classifier), `internal/llm/tools/read.go`, `internal/llm/tools/tempdir.go`, `internal/llm/tools/bash.go`, `cmd/webfetch-e2e`, `scripts/test/webfetch_output_limit.sh`, `docs/tool-permissions.md`, `docs/cli-tools.md`, `docs/telemetry.md`, `README.md`.
- `.opencode.json` public contract: unchanged. The breaker bounds are constants.
- **Behaviour change:** a flow that relied on a `deny` rule stopping a step at its first attempt now sees the step continue until the model reports the block, finishes without the action, or the breaker trips.

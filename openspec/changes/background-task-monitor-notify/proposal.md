## Why

Today every opencode tool is synchronous — `bash` blocks for up to 600s, `task` blocks until its subagent finishes, and there is no way to react to an event mid-execution. When an agent needs to wait on something long (a test suite, a deploy, a chatty log), it either burns the 600s budget on a single tool call or polls in a loop. Polling is expensive in two ways: every status-check tool call costs tokens (input schema + result + reasoning), and every check invalidates the prompt cache. A 30-step poll for a build that takes 8 minutes can cost 15K+ tokens of pure waiting.

Claude Code solved this with an event-driven pattern: tools start work, return an immediate ack with a task ID and an output-file path, and the system pushes a `<task-notification>` block into the agent's next turn when the work completes or hits a notable event. The agent uses the existing Read tool to consume output. Polling tools were deprecated. Opencode already has the transport layer for this (the cron scheduler injects synthetic message pairs into live sessions), but lacks the task lifecycle, the notification envelope, and the bash/task tool surfaces that drive it. We adopt the same model — task spawn returns an ack with `task_id`, completion injects a synthetic Assistant(ToolCall)+Tool(ToolResult) pair that auto-resumes the agent — so the model spends tokens reacting to outcomes instead of asking for them.

## What Changes

- New top-level `monitor` tool: spawns a subprocess, scans its output against a regex `pattern`, fires a per-event notification carrying matched lines (with a per-task coalesce window to bound notification rate).
- `bash` tool gains a `run_in_background: bool` parameter. When true, bash returns an immediate ack (task_id + output_file path) and the subprocess keeps running; output is teed to disk; on exit, a synthetic completion pair is injected into the session.
- `task` tool gains an `async: bool` parameter. When true, the subagent runs in the background and the parent's tool call returns immediately with task_id; on subagent completion, a synthetic pair is injected. Default remains synchronous (no behavior change for existing callers).
- New `tasklist` tool: enumerates in-flight background tasks for the current session (id, kind, started_at, status).
- New `taskstop` tool: terminates an in-flight background task by id. Fires a `status: killed` completion notification.
- New shared primitive `EnqueueTaskCompletion(sessionID, originatingToolCallID, kind, taskID, content, status, …)` that wraps `messages.CreatePair` plus an auto-resume trigger (`agent.Run` on the session if idle). Cron migrates onto this primitive (its existing `writeSyntheticMessages` becomes a thin call into `EnqueueTaskCompletion`).
- New per-process in-memory task registry keyed by `task_id`. Restart loses all in-flight tasks (no recovery; agent observes silence on the original task_id ack and re-investigates).
- Each background task writes to `<config.Data.Directory>/tasks/<task_id>.out`. Orphan files are swept at opencode boot.
- Each completion is dedupe-gated by a per-task `notified` flag — fast tasks whose ack already carried the final output do not get a follow-up notification.
- New `synthetic: bool` flag on stored Assistant messages. Bridge tool-update indicator emission (the 🔧 icons) is suppressed for any Assistant message with this flag.

## Capabilities

### New Capabilities

- `background-tasks`: task lifecycle and identity. Defines task IDs, statuses (`pending`/`running`/`completed`/`failed`/`killed`), the in-memory per-process registry, the output-file path convention under `<data.dir>/tasks/`, orphan cleanup on boot, restart semantics (in-flight tasks are lost; no recovery), and the per-task `notified` dedupe flag.
- `task-notifications`: synthetic completion injection. Defines the `EnqueueTaskCompletion` primitive, the synthetic Assistant(ToolCall)+Tool(ToolResult) shape, the `synthetic: true` flag on Assistant messages, the session-busy-vs-idle policy (queue if busy, auto-resume if idle via `agent.Run`), and integration with the existing `messages.CreatePair` write path.
- `monitor-tool`: process-spawning monitor with pattern matching. Defines the `monitor` tool's input schema (cmd, args, cwd, pattern, min_interval, max_events), the coalesce-window batching rule, per-event notification format with `kind: "monitor-event"`, and the monitor's terminal "stream ended" notification.
- `bash-background-mode`: `run_in_background` parameter on the existing `bash` tool. Defines the ack response shape (task_id + output_file), the disk-write target, the lifecycle handoff to the background-tasks registry, and how the 600s synchronous timeout cap interacts with the background path.
- `task-async-mode`: `async` parameter on the existing `task` tool. Defines the immediate-ack response when async, the subagent-completion hook into `EnqueueTaskCompletion`, and the (preserved) synchronous default.
- `tasklist-taskstop-tools`: the read-only `tasklist` and the kill-action `taskstop` tools. Defines their input schemas, the session-scoped view, and the kill semantics (SIGTERM for subprocesses, ctx.Cancel for subagents) plus the `status: killed` completion notification that follows.

### Modified Capabilities

(No existing main specs cover these areas — they are added as new specs in this change alongside the deltas. See repo policy in change input.)

- `cron-tool`: cron scheduler's completion-write path migrates from local `writeSyntheticMessages` to the shared `EnqueueTaskCompletion` primitive. Cron-fired Assistant(ToolCall) is now marked `synthetic: true`. End-state behavior is unchanged.
- `chat-bridge`: bridge MUST suppress tool-update indicator emission for any Assistant message with `synthetic: true`. Synthetic completion pairs (whether from cron, bash background, task async, or monitor) flow through the message log without triggering the 🔧 chat indicators; only the agent's NEXT assistant message (the human-readable reply) is fanned out to chat.

## Impact

**Code**
- New package: `internal/task/` — registry, lifecycle, output-file helpers, `EnqueueTaskCompletion` primitive.
- Modified: `internal/llm/tools/bash.go` (add `run_in_background`), `internal/llm/agent/agent-tool.go` (add `async` mode).
- New tool files: `internal/llm/tools/monitor.go`, `internal/llm/tools/tasklist.go`, `internal/llm/tools/taskstop.go`.
- Modified: `internal/cron/scheduler.go` (`writeSyntheticMessages` → `EnqueueTaskCompletion`).
- Modified: `internal/bridge/service/` (filter `synthetic` from indicator emission).
- New DB column: `messages.synthetic BOOL DEFAULT 0` (SQLite + MySQL migrations under `internal/db/sql/`). Affects sqlc-generated types and the `message.Message` Go struct.
- Tool descriptions updated to point at the new patterns (bash mentions `run_in_background`; agent prompt mentions auto-resume on background-task completion; sleep/polling guidance discouraged where notifications cover the case).

**APIs**
- New public package surface in `internal/task/` (registry interfaces, `EnqueueTaskCompletion` entry point).
- New tool names registered with the tool registry: `monitor`, `tasklist`, `taskstop`.
- Existing tool input schemas grow (bash, task) — backward compatible: omitting the new fields preserves today's synchronous behavior.

**Dependencies**
- No new external Go modules required. Subprocess management uses `os/exec` as today; regex matching uses stdlib `regexp`.

**Operational**
- Disk usage: `<data.dir>/tasks/` grows during a session; orphan sweep at boot drops files older than the running session. K8s ephemeral pods naturally reset between runs.
- Permissions: `monitor` is a new permission key (default `ask`); `bash` background mode reuses the existing `bash` rule; `task` async reuses the existing `task` rule. Headless `permissionMode: allow` continues to cover all background work.

**Non-goals (explicit, deferred)**
- Restart-recovery of in-flight tasks (chose to drop; future change can add DB-backed task metadata).
- DB-backed output storage (chose disk).
- MCP / kubectl / webhook-source monitors (chose process-spawning only).
- TUI styling of synthetic messages beyond a subtle dim/tag; full transcript redesign is out of scope.
- Removing or deprecating the synchronous bash/task surfaces — they remain the default.

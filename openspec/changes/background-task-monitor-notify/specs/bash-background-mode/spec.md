## ADDED Requirements

### Requirement: `run_in_background` parameter on bash tool
The existing `bash` tool's input schema SHALL gain a new optional boolean parameter `run_in_background` (default `false`). When omitted or `false`, the tool MUST behave exactly as it does today (synchronous, 600s timeout cap, captured stdout/stderr returned in the tool result). When `true`, the tool SHALL spawn the subprocess in the background and return immediately with an ack.

#### Scenario: Default behavior unchanged
- **WHEN** the agent invokes bash with `{command: "echo hi"}`
- **THEN** the tool blocks synchronously, captures output, and returns "hi" in the tool result (no change from prior behavior)

#### Scenario: Background mode returns immediately
- **WHEN** the agent invokes bash with `{command: "sleep 60", run_in_background: true}`
- **THEN** the tool returns within milliseconds with an ack ToolResult; the subprocess continues running

### Requirement: Background spawn ack format
When `run_in_background: true`, the tool's ack ToolResult SHALL contain at minimum:
- The literal phrase "Background task started"
- A `task_id:` line with the task ID assigned by the background-tasks registry
- An `output_file:` line with the absolute path to `<data.dir>/tasks/<task_id>.out`
- A `command:` line with the (possibly truncated) command string
- Guidance text reminding the agent that a synthetic completion will arrive, and that the output file can be read mid-flight with the Read tool

#### Scenario: Ack content
- **WHEN** the agent invokes `{command: "go test ./...", run_in_background: true}`
- **THEN** the ack contains lines matching `^task_id: shell_[A-Z2-7]+$`, `^output_file: .+/tasks/shell_[A-Z2-7]+\.out$`, and `^command: go test \./\.\.\.$` (with reasonable command truncation if longer than 200 chars)

### Requirement: Subprocess lifecycle in background mode
The background spawn SHALL:
1. Allocate the task ID and output file via the background-tasks registry.
2. Open the output file write-only (mode 0o600), set `cmd.Stdout` and `cmd.Stderr` to it.
3. Start the subprocess (`cmd.Start()`); on start-failure, return a tool-execution error and do NOT register the task.
4. Register the task with `Kind: KindBash` and the running `*os.Process`.
5. Launch a monitor goroutine that calls `cmd.Wait()`, then on exit:
   - `Sync()` the output file (see background-tasks spec).
   - Read the file content (capped at the existing bash output-size budget).
   - Invoke `task.EnqueueTaskCompletion` with `Kind: KindBash`, `OriginatingToolName: "bash"`, `Status: StatusCompleted` (exit 0) or `StatusFailed` (exit != 0), `ExitCode`, and `Content` set to the captured output.
6. Return the ack ToolResult to the original tool call.

#### Scenario: Subprocess succeeds
- **WHEN** a background bash subprocess exits with code 0 after 30 seconds
- **THEN** at the 30s mark a synthetic Assistant(ToolCall name=bash, synthetic=true) + Tool(ToolResult) pair appears in the session log; the ToolResult content matches what a synchronous bash with the same command would have produced; an `agent.Run` is started on the session if it was idle

#### Scenario: Subprocess fails
- **WHEN** a background bash subprocess exits with code 2 after 5 seconds
- **THEN** a synthetic pair appears with `Status: StatusFailed`, the captured output is in the ToolResult content, and exit code 2 is recorded in the registry

#### Scenario: Subprocess start fails
- **WHEN** a background bash is invoked with a command not on PATH (e.g., `{command: "nonexistent-cmd", run_in_background: true}`)
- **THEN** the tool returns a regular synchronous tool error (no ack); no task is registered; no output file is created

### Requirement: No 600s timeout in background mode
When `run_in_background: true`, the existing 600s synchronous timeout cap SHALL NOT apply. The subprocess may run indefinitely (until natural exit, `taskstop`, opencode shutdown, or the K8s pod's `activeDeadlineSeconds`). The `timeout` parameter MUST be silently ignored when `run_in_background: true`; the tool MAY emit an informational note in the ack but MUST NOT error.

#### Scenario: Long-running background subprocess
- **WHEN** the agent invokes `{command: "sleep 7200", run_in_background: true}` (2 hours)
- **THEN** the spawn succeeds; the subprocess runs for the full duration; no synchronous-timeout error is produced

#### Scenario: timeout parameter is ignored in background mode
- **WHEN** the agent invokes `{command: "sleep 60", run_in_background: true, timeout: 5000}`
- **THEN** the subprocess runs for 60 seconds (not 5); the `timeout` parameter has no effect

### Requirement: Permission gate uses existing `bash` rule
The spawn-time permission check for `run_in_background: true` SHALL use the existing `bash` permission rule key. There is no separate `bash-background` rule. Once spawn is approved, the background completion notification MUST NOT trigger a fresh permission check.

#### Scenario: bash rule allows
- **WHEN** `permission.rules.bash: {"*": "allow"}` and the agent invokes a background bash
- **THEN** spawn succeeds without a prompt; completion notification fires without a prompt

#### Scenario: bash rule denies
- **WHEN** `permission.rules.bash: {"*": "deny"}` and the agent invokes a background bash
- **THEN** spawn is denied with the same tool-permission error a synchronous bash would produce; no task is registered

### Requirement: Synthetic ToolCall input mirrors the spawn input
The synthetic Assistant(ToolCall) written by the bash background completion path SHALL set its `Input` JSON to the same `BashParams` shape the agent originally sent, with `run_in_background` STRIPPED. This means the renderer formats the synthetic completion as if it were a synchronous bash result of the same command.

#### Scenario: Synthetic input reformatted
- **WHEN** the agent invoked `{command: "go test ./...", run_in_background: true}` and completion fires
- **THEN** the synthetic Assistant ToolCall's input JSON is `{"command": "go test ./..."}` (no `run_in_background` field), and renders identically to a synchronous bash call's input

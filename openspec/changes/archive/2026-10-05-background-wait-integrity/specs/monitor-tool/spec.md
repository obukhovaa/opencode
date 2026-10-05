## MODIFIED Requirements

### Requirement: Monitor spawn and ack
On invocation, the monitor tool SHALL:
1. Validate the input (including compiling `pattern` as `regexp.Compile`); return a validation error if the regex is invalid.
2. Allocate a task ID with `monitor_` prefix and an output file under `<data.dir>/tasks/<task_id>.out`.
3. Start the subprocess with stdout and stderr both redirected through a tee that (a) writes every byte to the output file and (b) line-scans against the compiled pattern.
4. Register the task in the background-tasks registry with `Kind: KindMonitor` and `Proc: cmd.Process`.
5. Start the coalesce ticker.
6. Return an ack ToolResult containing the `task_id`, `output_file` path, `pattern`, `min_interval_ms`, `max_events`, and the yield contract below.

The ack SHALL state the same yield contract the background-bash ack carries. The existing "Do NOT poll — the events arrive automatically" instruction is necessary but not sufficient: it tells the model what not to do without telling it what happens if it does nothing, leaving `sleep` as the only apparent way to wait. The ack guidance text SHALL therefore state:

- that matched lines arrive as synthetic monitor-event notifications;
- that a terminal notification fires on subprocess exit, `max_events`, or `taskstop`;
- that the agent MUST NOT poll **and MUST NOT `sleep`** while waiting;
- that in a non-interactive (flow) step the runtime holds the turn open, so ending the turn without a tool call is the correct way to wait, and sleeping cannot observe an event sooner.

#### Scenario: Successful spawn
- **WHEN** a valid monitor call is made
- **THEN** the tool returns within milliseconds with an ack containing the task_id; the subprocess continues running in the background

#### Scenario: Invalid regex
- **WHEN** `pattern` is `[unclosed`
- **THEN** the tool returns a tool-validation error; no subprocess is spawned; no task is registered

#### Scenario: Spawn failure
- **WHEN** `cmd` does not exist on PATH
- **THEN** the tool returns a tool-execution error; no task is registered

#### Scenario: Ack states the no-sleep yield contract
- **WHEN** the agent invokes `monitor` with a valid `cmd` and `pattern`
- **THEN** the ack contains a "do NOT sleep" instruction
- **AND** it states that a non-interactive step holds the turn open until an event or terminal notification

#### Scenario: Ack retains its existing identifying lines
- **WHEN** a monitor is spawned
- **THEN** the ack still contains `task_id:`, `output_file:`, `cmd:`, `pattern:`, `min_interval_ms:` and `max_events:` lines

## ADDED Requirements

### Requirement: A healthy silent monitor is distinguishable from a dead one

A monitor whose pattern has not yet matched emits nothing, and silence is its healthy
state. With no feedback channel, an agent cannot tell a working monitor from a broken one
and will re-spawn or fall back to polling — the observed failure mode was a second monitor
on the same file 16s after the first, two `tasklist` calls, then a `sleep`.

`tasklist` output for a `kind=monitor` task SHALL therefore include a count of lines
scanned since spawn, alongside the existing state and event information. The counter is
observability only: it MUST NOT affect stall detection, MUST NOT bound the monitor's
lifetime, and MUST NOT trigger any notification.

#### Scenario: tasklist reports scanned lines for a silent monitor

- **WHEN** a monitor has consumed input lines but matched none
- **AND** `tasklist` is called
- **THEN** its row reports a non-zero scanned-line count with `state=running`

#### Scenario: Scanned-line count does not bound the monitor

- **WHEN** a monitor's scanned-line count grows without any pattern match
- **THEN** the monitor is not killed, not flagged stalled, and emits no notification

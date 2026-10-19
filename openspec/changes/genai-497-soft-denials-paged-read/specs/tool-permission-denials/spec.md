## ADDED Requirements

### Requirement: A permission-rule denial is a tool error and the run continues

When a configured permission rule (an agent's `permission`, the global `permission.rules`, or a workspace CLI tool's manifest default) evaluates to `deny` for a tool call, the system SHALL return an error tool result to the model and SHALL continue the agentic loop. The result SHALL name the tool and the input the rule was evaluated against (an input over 512 bytes cut, with a marker), SHALL state that retrying will be denied again, and SHALL state that reaching the same effect through another command, tool or script is not allowed. Other tool calls of the same turn SHALL keep running. The tool-level error SHALL still satisfy `errors.Is(err, permission.ErrorPermissionDenied)`.

#### Scenario: Denied command, then a different approach

- **WHEN** the model calls `bash` with a command a `deny` rule matches, and its next turn ends without tools
- **THEN** the model receives an error result naming `bash` and the command, a second model call is made, and the run ends with `end_turn`

#### Scenario: Parallel sibling keeps running

- **WHEN** one call of a parallel group is denied by a rule while another is still running
- **THEN** the other call completes and its result is recorded

#### Scenario: Read-category tool

- **WHEN** a `deny` rule matches a `read`, `view_image`, `grep`, `glob` or `ls` call
- **THEN** the tool returns a `Permission denied` text result, the run continues, and the denial does not count towards the circuit breaker

### Requirement: A rejected permission prompt still ends the run

When a human (or the chat bridge's `permissionMode: deny` on their behalf) rejects an `ask` prompt, the system SHALL record `Permission denied`, SHALL cancel the turn's remaining calls, and SHALL end the run with finish reason `permission_denied`, as before.

#### Scenario: Rejected prompt

- **WHEN** a tool call's prompt is rejected
- **THEN** exactly one model call is made and the run ends with `permission_denied`

### Requirement: Repeated rule denials end the run

The system SHALL end the run with finish reason `permission_denied` after 3 consecutive turns without progress, the first of them with a rule denial, or once 10 tool calls of one run were denied by rules. A turn makes progress when at least one of its calls ran and was not denied by a rule; such a turn SHALL reset the consecutive count. A call answered without running (loop detected, tool not found, blocked by a PreToolUse hook) SHALL NOT count as progress. The counts SHALL start fresh with every agent run. PreToolUse hook blocks and `skill` permission denials SHALL NOT count as denials. A turn that delivers an accepted `struct_output` SHALL NOT be ended by the breaker, so its output is kept.

#### Scenario: A model that only calls denied tools

- **WHEN** every turn of a run makes one call that a rule denies
- **THEN** the run ends with `permission_denied` after the third model call

#### Scenario: Identical denied call repeated

- **WHEN** every turn repeats the same call with the same input, a rule denies it, and loop detection answers it from the third turn on
- **THEN** the run ends with `permission_denied` after the third model call

#### Scenario: Denials mixed with progress

- **WHEN** every turn makes one denied call and one allowed call
- **THEN** the run ends with `permission_denied` after the tenth turn

#### Scenario: Denied calls alternating with hook-blocked calls

- **WHEN** a run's turns alternate a call that a rule denies and a call that a PreToolUse hook blocks
- **THEN** the run ends with `permission_denied` after the third model call

#### Scenario: Run total reached on the struct_output turn

- **WHEN** the tenth rule-denied call of a run is in the same turn as an accepted `struct_output`
- **THEN** the run finishes with that `struct_output` as its output

#### Scenario: One turn of parallel denials

- **WHEN** a single turn makes three parallel calls that a rule denies, and the next turn ends without tools
- **THEN** the run is not ended by the breaker

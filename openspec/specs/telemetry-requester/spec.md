# telemetry-requester Specification

## Purpose
Defines how every trace is attributed to the person a run works for: the `requester` metadata field, its precedence (flow arg, per-turn requester, static `telemetry.requester`), and how chat-bridge turns, cron jobs, background-task resumes and detached work carry it.
## Requirements
### Requirement: Trace requester precedence

Every Langfuse trace SHALL carry a `requester` metadata field (namespaced like every other custom key) when a requester is known, chosen in this order: a `requester` flow arg extracted through `telemetry.flowArgs`; the requester carried by the run context; the configured `telemetry.requester`. An empty or whitespace-only flow arg SHALL count as absent. The field SHALL be omitted when none is known.

#### Scenario: Flow arg wins

- **WHEN** a flow step runs with a `requester` flow arg and the run context also carries a requester
- **THEN** the trace's `requester` is the flow arg value

#### Scenario: Blank flow arg falls through

- **WHEN** a flow step runs with an empty or whitespace-only `requester` flow arg
- **THEN** the trace's `requester` is the run context's requester, else `telemetry.requester`, else the field is omitted

#### Scenario: Per-turn requester beats static config

- **WHEN** the run context carries a requester and `telemetry.requester` is set
- **THEN** the trace's `requester` is the run context's value

#### Scenario: Static fallback

- **WHEN** neither a flow arg nor a run-context requester is present and `telemetry.requester` is set
- **THEN** the trace's `requester` is `telemetry.requester`

#### Scenario: Nothing known

- **WHEN** no source yields a requester
- **THEN** the trace carries no `requester` field

#### Scenario: Namespaced key

- **WHEN** `telemetry.metadataNamespace` is set (e.g. `piano`)
- **THEN** the requester is exported as `piano.requester` and no flat `requester` key is written

### Requirement: Chat turns are attributed to the message author

The chat bridge SHALL run each inbound turn with the message author as the run context's requester, resolved to an email address when the adapter implements `bridge.UserEmailResolver` and the platform returns one, else the raw author id. The Slack resolver SHALL look up workspace (`U…`) and Enterprise Grid (`W…`) user ids. Resolved and email-less results SHALL be cached per channel, identity and author for one hour. A lookup SHALL be bounded by a timeout; a failed lookup SHALL fall back to the author id and SHALL be cached for a short period (5 minutes), so a permanent failure costs at most one platform call and one warning per author per period.

#### Scenario: Shared thread with two authors

- **WHEN** two people post in the same bound session
- **THEN** each turn's traces carry that turn's author

#### Scenario: Lookup fails

- **WHEN** the platform lookup returns an error
- **THEN** the turn runs with the raw author id as requester, further messages from that author within the failure period reuse that fallback without a lookup, and the first message after it retries the lookup

#### Scenario: Enterprise Grid author

- **WHEN** a Slack message's author id starts with `W`
- **THEN** the author is resolved via `users.info` like a `U…` id

### Requirement: Scheduled jobs carry their creator's requester

A cron job SHALL store a requester when created: the explicit `CreateParams.Requester`, else a non-blank `requester` flow arg on the creating context, else the requester on the creating context — the order the creating turn's trace uses. The stored value SHALL be clamped to 320 bytes without splitting a UTF-8 sequence. Every run of the job SHALL carry the stored requester on its run context. Jobs created without one SHALL run without a run-context requester.

#### Scenario: Job created during a chat turn

- **WHEN** an agent creates a cron job during a turn attributed to a person
- **THEN** every later run of that job is attributed to the same person

#### Scenario: Job created inside a flow step

- **WHEN** an agent creates a cron job during a flow step whose `requester` flow arg is extracted and whose context carries no per-turn requester
- **THEN** the job stores the flow arg value and every later run is attributed to it

#### Scenario: Overlong requester

- **WHEN** the resolved requester is longer than 320 bytes
- **THEN** the job is still created, with the requester clamped to 320 bytes, on SQLite and MySQL alike

### Requirement: Detached work inherits the requester

Work that outlives or is detached from the turn that started it SHALL keep that turn's requester: a detached async subagent SHALL run with the requester of the turn that spawned it; the first turn's session-title generation SHALL carry the turn's requester, resolved as the turn's own trace resolves it (a non-blank `requester` flow arg, else the run context's requester); a chat-bridge `/compact` SHALL run its compaction with the command author's requester, resolved as for a chat turn; and a background task (async `task`, `bash` with `run_in_background`, `monitor`) SHALL record its spawning turn's requester so the turn its completion auto-resumes runs with it.

#### Scenario: Background task

- **WHEN** a turn attributed to a person spawns an async subagent
- **THEN** the subagent's traces carry that person as requester

#### Scenario: Auto-resumed turn

- **WHEN** a background task spawned by a turn attributed to a person completes while its session is idle
- **THEN** the auto-resumed turn's traces carry that person as requester

#### Scenario: Title generation

- **WHEN** the first turn of a session is attributed to a person
- **THEN** the title-generation trace carries that person as requester

#### Scenario: Title generation in a flow step

- **WHEN** the first turn of a session is a flow step with a non-blank `requester` flow arg
- **THEN** the title-generation trace carries the flow arg value as requester

#### Scenario: Bridge compaction

- **WHEN** a person runs `/compact` in a bound chat
- **THEN** the compaction's summarizer trace carries that person as requester


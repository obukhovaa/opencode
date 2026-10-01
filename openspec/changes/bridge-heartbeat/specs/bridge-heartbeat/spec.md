# bridge-heartbeat

## ADDED Requirements

### Requirement: Heartbeats are off until the human turns them on

The bridge SHALL NOT run heartbeat turns for a binding until a peer of that binding runs `/heartbeat on`. The heartbeat state of a binding SHALL be one of `unset`, `on` or `off`, stored in the `bridge_heartbeats` table keyed by the binding key, so that it survives process restarts, redeploys and `/reset`.

#### Scenario: A fresh binding does not beat

- **WHEN** a daemon starts with a binding that has no heartbeat row
- **THEN** no heartbeat turn is queued for that binding

#### Scenario: Turning it on survives a restart

- **WHEN** a peer runs `/heartbeat on` and the daemon is then restarted
- **THEN** the binding's heartbeat is still `on` after the restart and beats resume without any message from the human

### Requirement: The /heartbeat command configures the heartbeat without a model call

The bridge SHALL handle `/heartbeat` as a chat command, without starting an agent run. It SHALL accept:

- no argument or `status`: report the state, interval, active hours, days, model, agenda file, last beat (time and outcome) and next beat;
- `on` and `off`;
- `now`: queue one beat immediately without changing the schedule;
- `every <duration>` with a duration between 10 minutes and 24 hours (`30m`, `1h`, `2h30m`);
- `hours <HH>-<HH>` or `hours <HH:MM>-<HH:MM>` in UTC, start inclusive and end exclusive, wrapping midnight when the end is before the start, or `hours all`;
- `days weekdays` or `days all`;
- `model <id>` for a supported model, or `model default`;
- `file <path>` relative to the working directory, or `file default`.

Several of these MAY be combined in one command (`/heartbeat on every 30m hours 05-21 days weekdays`). An invalid argument SHALL leave every setting unchanged and reply with the reason and a usage line. Every successful change SHALL reply with the resulting status. Times SHALL be shown in UTC with a `Z` suffix.

#### Scenario: Turn on with a schedule

- **WHEN** a peer runs `/heartbeat on every 30m hours 05-21 days weekdays`
- **THEN** the heartbeat is `on`, beats every 30 minutes from 05:00Z to 21:00Z on weekdays, and the reply names the next beat time

#### Scenario: Invalid interval

- **WHEN** a peer runs `/heartbeat every 2m`
- **THEN** no setting changes and the reply says the interval must be between 10m and 24h

#### Scenario: Tuning while off

- **WHEN** the state is `unset` and a peer runs `/heartbeat every 2h`
- **THEN** the interval is stored, the state stays `unset`, and no beat is scheduled

### Requirement: Beats run in the bound session through the session dispatcher

A heartbeat turn SHALL run in the binding's session through that session's dispatcher, as a synthetic inbound marked as a heartbeat, so that it never overlaps another run on the session and its final reply reaches the bound peers. The heartbeat marker SHALL NOT be settable through `/router/inbound`.

#### Scenario: Reply to a beat

- **WHEN** a beat posts a report and the human replies to it
- **THEN** the reply is handled in the same session that produced the report

### Requirement: Beats follow the schedule, defer when busy and coalesce missed beats

A beat SHALL become due at `next_beat_at`. Slots SHALL lie on a grid of the interval anchored at 00:00 UTC, restricted to the active hours and days. When a beat fires, the next slot SHALL be computed from the current time, so that beats missed while the process was down or the session was busy collapse into one. A due beat SHALL wait, without being queued, while the session is running or another beat for the session is queued or running.

#### Scenario: Busy session

- **WHEN** a beat comes due while the agent is answering a human message
- **THEN** the beat is queued only after that run ends, and the human's message is never delayed by a beat

#### Scenario: Missed beats after downtime

- **WHEN** the daemon was down for three hours with an hourly heartbeat and starts again inside the active hours
- **THEN** exactly one catch-up beat runs, and the next beat after it is the next slot on the grid

#### Scenario: Outside active hours

- **WHEN** the active hours are `05-21` and the current time is 22:30Z
- **THEN** the next beat is 05:00Z on the next active day

### Requirement: An empty agenda skips the beat

Each beat's prompt SHALL name the agenda file (default `HEARTBEAT.md` in the working directory) and the current UTC time, and SHALL tell the agent to reply exactly `HEARTBEAT_OK` when nothing needs the human's attention. If the agenda file is missing, or holds only whitespace, markdown headings and empty list items, the beat SHALL be skipped without a model call and recorded as `skipped`.

#### Scenario: No agenda file

- **WHEN** a beat comes due and `HEARTBEAT.md` does not exist
- **THEN** no agent run starts, nothing is posted, and `/heartbeat status` shows the last beat as skipped

### Requirement: Heartbeat turns are quiet unless there is something to say

While a heartbeat turn runs, the bridge SHALL NOT post a queued acknowledgement, a progress card or tool-call cards for it. If the final reply is `HEARTBEAT_OK`, or a reply of at most 300 characters whose first or last line is `HEARTBEAT_OK`, nothing SHALL be posted and the beat SHALL be recorded as `silent`. Any other reply SHALL be posted with a header line `💓 Heartbeat HH:MMZ`. A terminal error SHALL post one line naming the failure and record it as `error` with the reason.

#### Scenario: Nothing new

- **WHEN** the agent ends a beat with `HEARTBEAT_OK`
- **THEN** nothing appears in the chat

#### Scenario: Something to report

- **WHEN** the agent ends a beat with a two-line report
- **THEN** the chat shows the header `💓 Heartbeat 14:00Z` followed by the report, and no progress card or tool cards were posted during the beat

### Requirement: A model override is optional

When a heartbeat `model` is set, beats SHALL run on the primary agent with that model as a per-instance override. When it is not set, beats SHALL run on the active agent unchanged.

#### Scenario: Cheaper model for beats

- **WHEN** a peer runs `/heartbeat model <id>`
- **THEN** later beats run on that model while human turns keep the agent's configured model

### Requirement: An unset heartbeat is offered at most once a week

When a daemon's adapter for an identity is registered, the bridge SHALL post a setup reminder to each binding of that identity whose heartbeat state is `unset` and whose last reminder is absent or at least seven days old, explaining `/heartbeat on` and `/heartbeat off`. It SHALL record the reminder time before posting. Bindings whose session has no messages SHALL be skipped. A binding whose state is `on` or `off` SHALL never be reminded.

#### Scenario: Weekly, not on every restart

- **WHEN** a daemon with an unset heartbeat is redeployed twice in one day
- **THEN** the human receives one reminder that day, and the next one no sooner than seven days later

#### Scenario: Declined

- **WHEN** the human runs `/heartbeat off`
- **THEN** no reminder is ever posted for that binding again

### Requirement: Heartbeats exist only in daemon mode

The heartbeat scheduler and the setup reminder SHALL run only when `serve` runs as a daemon (neither `--flow` nor `--pool-mode`). In any other mode `/heartbeat` SHALL reply that heartbeats are not available and change nothing.

#### Scenario: Flow runner

- **WHEN** a flow runner pod with a bound interactive step receives `/heartbeat on`
- **THEN** the reply says heartbeats are not available in this mode and nothing is stored

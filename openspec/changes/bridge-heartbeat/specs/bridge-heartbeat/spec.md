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
- `now`: queue one beat without changing the schedule. When the session is busy, including when the `heartbeat` tool's `now` is used inside the session's own turn, the beat SHALL be queued behind the current turn and the reply SHALL say that it runs when the current turn ends. A manual beat that then finds the session held by another actor SHALL wait for it like a message, and SHALL NOT be dropped;
- `every <duration>` with a duration between 10 minutes and 24 hours (`30m`, `1h`, `2h30m`);
- `hours <HH>-<HH>` or `hours <HH:MM>-<HH:MM>` in UTC, start inclusive and end exclusive, wrapping midnight when the end is before the start, or `hours all`;
- `days weekdays` or `days all`;
- `model <id>` for a supported model whose provider is configured and enabled, or `model default`;
- `file <path>` relative to the working directory, or `file default`.

Several of these MAY be combined in one command (`/heartbeat on every 30m hours 05-21 days weekdays`). An invalid argument SHALL leave every setting unchanged and reply with the reason and a usage line. Every successful change SHALL reply with the resulting status. Times SHALL be shown in UTC with a `Z` suffix. A change SHALL NOT be undone by a scheduler pass or a beat that read the binding's row before the change: those write only the fields they own, and nothing when the state or settings changed since they read it.

#### Scenario: Turn on with a schedule

- **WHEN** a peer runs `/heartbeat on every 30m hours 05-21 days weekdays`
- **THEN** the heartbeat is `on`, beats every 30 minutes from 05:00Z to 21:00Z on weekdays, and the reply names the next beat time

#### Scenario: Invalid interval

- **WHEN** a peer runs `/heartbeat every 2m`
- **THEN** no setting changes and the reply says the interval must be between 10m and 24h

#### Scenario: Tuning while off

- **WHEN** the state is `unset` and a peer runs `/heartbeat every 2h`
- **THEN** the interval is stored, the state stays `unset`, and no beat is scheduled

#### Scenario: Beat now while the agent is working

- **WHEN** a peer runs `/heartbeat now` while the agent is answering a message, or the agent calls the `heartbeat` tool with `now`
- **THEN** the reply says the beat is queued and runs when the current turn ends, and it does

#### Scenario: Model of an unconfigured provider

- **WHEN** a peer runs `/heartbeat model <id>` for a supported model whose provider is not configured or is disabled
- **THEN** the model is not stored and the reply names the provider

### Requirement: /heartbeat accepts natural language through the agent

When the arguments of `/heartbeat` are not the exact form, and the active agent has the `heartbeat` tool, the bridge SHALL hand the request to the agent as an ordinary turn whose prompt carries the request and the current heartbeat status. The agent SHALL apply it with the `heartbeat` tool, whose typed settings (`state`, `every`, `hours`, `days`, `model`, `file`, `now`) SHALL be validated exactly like the exact form and SHALL apply to every chat binding of the calling session. When the active agent lacks the tool, the bridge SHALL reply with the parse error and the exact grammar instead.

The `heartbeat` tool SHALL be registered only for agents that explicitly enable it, and SHALL reach the bridge even when the agent's tool set was built before the bridge started.

#### Scenario: Schedule in the human's words

- **WHEN** a peer runs `/heartbeat every half hour on weekdays, 7 to 23 Oslo time` and the agent has the tool
- **THEN** the agent receives the request with the current status, sets `every 30m`, the matching UTC hours and `days weekdays` with the tool, and replies with the resulting schedule

#### Scenario: Checks in the human's words

- **WHEN** the request also says "keep an eye on my open merge requests"
- **THEN** the agent writes that item into the agenda file, so the next beat is not skipped

#### Scenario: Agent without the tool

- **WHEN** the active agent does not enable the `heartbeat` tool and a peer runs `/heartbeat every half hour`
- **THEN** the bridge replies with the parse error and the exact grammar, and no agent run starts

### Requirement: Beats run in the bound session through the session dispatcher

A heartbeat turn SHALL run in the binding's session through that session's dispatcher, as a synthetic inbound marked as a heartbeat, so that it never overlaps another run on the session and its final reply reaches the bound peers. The heartbeat marker SHALL NOT be settable through `/router/inbound`.

#### Scenario: Reply to a beat

- **WHEN** a beat posts a report and the human replies to it
- **THEN** the reply is handled in the same session that produced the report

### Requirement: Beats follow the schedule, defer when busy and coalesce missed beats

A beat SHALL become due at `next_beat_at`. Each day's slots SHALL start at the start of the active hours (00:00 UTC without them) and repeat every interval while inside them, on active days only. Active hours and days SHALL be UTC and SHALL NOT follow daylight saving time. When a beat fires, the next slot SHALL be computed from the current time, so that beats missed while the process was down or the session was busy collapse into one. A due beat SHALL fire only while the current time is inside the active hours of an active day; a beat that comes due late outside them (a catch-up after downtime, or one a busy session held back) SHALL move to the next allowed slot without firing. A due beat SHALL wait, without being queued, while the session is running, a message is queued for it, an interactive flow step owns it, or another beat for the session is queued or running. A session bound to several chats SHALL get at most one beat per slot: when one of its bindings' beats fires or is skipped, the other due bindings of that session SHALL move to their next slot, and the report SHALL reach every bound chat like any reply.

#### Scenario: Busy session

- **WHEN** a beat comes due while the agent is answering a human message
- **THEN** the beat is queued only after that run ends, and the human's message is never delayed by a beat

#### Scenario: Missed beats after downtime

- **WHEN** the daemon was down for three hours with an hourly heartbeat and starts again inside the active hours
- **THEN** exactly one catch-up beat runs, and the next beat after it is the next slot on the grid

#### Scenario: Daily beat at a fixed time

- **WHEN** the settings are `every 24h hours 07-08`
- **THEN** the heartbeat beats once a day at 07:00Z

#### Scenario: Outside active hours

- **WHEN** the active hours are `05-21` and the current time is 22:30Z
- **THEN** the next beat is 05:00Z on the next active day

#### Scenario: Catch-up outside active hours

- **WHEN** the active hours are `07-23`, a beat was due at 22:00Z, and the daemon starts again at 03:00Z after an overnight redeploy
- **THEN** no beat fires at 03:00Z and the next beat is 07:00Z

#### Scenario: Interactive flow step

- **WHEN** a beat comes due on a session an interactive flow step owns
- **THEN** no beat is queued and the beat keeps its due time

#### Scenario: Session bound to two chats

- **WHEN** a session is bound to two chats that both have the heartbeat on with the same schedule
- **THEN** one beat runs per slot, its report reaches both chats, and both bindings move to the next slot

### Requirement: An empty agenda skips the beat

Each beat's prompt SHALL name the agenda file (default `HEARTBEAT.md` in the working directory) and the current UTC time, and SHALL tell the agent to reply exactly `HEARTBEAT_OK` when nothing needs the human's attention. If the agenda file is missing, or holds only whitespace, markdown headings and empty list items, the beat SHALL be skipped without a model call and recorded as `skipped`.

When scheduled beats start being skipped for a reason (the binding's last outcome was not a skip for the same reason), the bridge SHALL post one notice to that binding naming the reason and how to fix it or turn the heartbeat off. Later skips for the same reason SHALL post nothing.

#### Scenario: No agenda file

- **WHEN** a beat comes due and `HEARTBEAT.md` does not exist
- **THEN** no agent run starts, `/heartbeat status` shows the last beat as skipped, and the chat gets one `💓 Heartbeat HH:MMZ skipped: HEARTBEAT.md does not exist. …` notice

#### Scenario: Still no agenda file

- **WHEN** the next beat comes due and `HEARTBEAT.md` still does not exist
- **THEN** the beat is skipped and nothing is posted

### Requirement: Heartbeat turns are quiet unless there is something to say

While a heartbeat turn runs, the bridge SHALL NOT post a queued acknowledgement, a progress card, tool-call cards or intermediate assistant text for it. Quietness SHALL belong to the heartbeat run itself: a part event of the heartbeat run SHALL stay quiet when it is handled after the run ended, and a part event of the run before the beat SHALL be rendered as usual when it is handled while the beat runs. If the final reply is `HEARTBEAT_OK`, or a reply of at most 300 characters whose first or last line is `HEARTBEAT_OK`, nothing SHALL be posted and the beat SHALL be recorded as `silent`. Any other reply SHALL be posted with a header line `💓 Heartbeat HH:MMZ`. A terminal error other than a cancellation SHALL post one line naming the failure and record it as `error` with the reason.

#### Scenario: Nothing new

- **WHEN** the agent ends a beat with `HEARTBEAT_OK`
- **THEN** nothing appears in the chat

#### Scenario: Something to report

- **WHEN** the agent ends a beat with a two-line report
- **THEN** the chat shows the header `💓 Heartbeat 14:00Z` followed by the report, and no progress card or tool cards were posted during the beat

#### Scenario: Late tool event of the previous turn

- **WHEN** a tool event of the human turn before a beat is handled while the beat runs
- **THEN** its tool card is posted as usual

### Requirement: A message preempts a running beat

When a human message is queued on a session whose heartbeat turn is in flight, the bridge SHALL cancel the beat's run, on the agent instance the beat runs on, so the message is handled without waiting for the beat. A beat that has not started its run yet SHALL NOT start. A beat cancelled this way SHALL post nothing and SHALL be recorded as `skipped` with the reason `preempted by a message`, and its `next_beat_at` SHALL NOT move back. A beat cancelled in any other way (for example `/abort`) SHALL NOT post the failure line either. Cancelling SHALL NOT stop another actor's run on the session.

#### Scenario: Message during a beat

- **WHEN** a human sends a message while a beat is running
- **THEN** the beat is cancelled, nothing is posted for it, the message is answered, and `/heartbeat status` shows the last beat as skipped, preempted by a message

### Requirement: A model override is optional

When a heartbeat `model` is set, beats SHALL run on the primary agent with that model as a per-instance override. When it is not set, beats SHALL run on the active agent unchanged.

#### Scenario: Cheaper model for beats

- **WHEN** a peer runs `/heartbeat model <id>`
- **THEN** later beats run on that model while human turns keep the agent's configured model

### Requirement: An unset heartbeat is offered at most once a week

When a daemon's adapter for an identity is registered, the bridge SHALL post a setup reminder to each binding of that identity whose heartbeat state is `unset` and whose last reminder is absent or at least seven days old, explaining `/heartbeat on` and `/heartbeat off`. It SHALL record the reminder time before posting. Bindings whose session has no messages SHALL be skipped. A binding whose state is `on` or `off` SHALL never be reminded.

The reminder SHALL reach only conversations this daemon serves: top-level direct messages with one person, as the adapter reports them, of an adapter that is inbound-active in this process. It SHALL NOT be posted to a thread peer, a channel or group chat, the `external` relay, any binding of an adapter whose inbound is disabled (mediated), a session whose root is another session (a flow step's or a subagent's), or a session an interactive flow step owns. An adapter that cannot tell a direct message SHALL get no reminder. `router.heartbeatReminder: false` SHALL turn the reminder off for the whole process; it defaults to on.

#### Scenario: Weekly, not on every restart

- **WHEN** a daemon with an unset heartbeat is redeployed twice in one day
- **THEN** the human receives one reminder that day, and the next one no sooner than seven days later

#### Scenario: Declined

- **WHEN** the human runs `/heartbeat off`
- **THEN** no reminder is ever posted for that binding again

#### Scenario: Shared project

- **WHEN** a daemon shares its database project with flow and pool pods, and its identity's bindings include channel threads, @-mention threads, other pods' review threads and a mediated bot's chats
- **THEN** only the daemon's own top-level direct messages are reminded

#### Scenario: Switched off

- **WHEN** `.opencode.json` sets `router.heartbeatReminder: false`
- **THEN** no reminder is posted, and `/heartbeat` still works

### Requirement: Only the process that owns a bot schedules its beats

The scheduler SHALL queue beats only for bindings of an adapter that is registered and inbound-active in this process, so the identity lock that admits one such adapter per identity also admits one scheduler per heartbeat. Bindings of a mediated adapter (inbound disabled, which takes no identity lock and may be held by several processes) and of the `external` relay SHALL NOT be scheduled. Turning such a binding's heartbeat on SHALL reply that scheduled beats do not run for it; `/heartbeat now` SHALL still run a beat.

#### Scenario: Mediated daemon

- **WHEN** a daemon's only bot is mediated by the orchestrator and a peer runs `/heartbeat on`
- **THEN** the reply says scheduled beats do not run for this chat, and no scheduled beat is ever queued for it

### Requirement: Heartbeats exist only in daemon mode

The heartbeat scheduler and the setup reminder SHALL run only when `serve` runs as a daemon (neither `--flow` nor `--pool-mode`). In any other mode `/heartbeat` SHALL reply that heartbeats are not available and change nothing.

#### Scenario: Flow runner

- **WHEN** a flow runner pod with a bound interactive step receives `/heartbeat on`
- **THEN** the reply says heartbeats are not available in this mode and nothing is stored

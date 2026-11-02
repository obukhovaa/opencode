## MODIFIED Requirements

### Requirement: An empty agenda skips the beat

Each beat's prompt SHALL name the agenda file (default `HEARTBEAT.md` in the working directory) and the current UTC time, and SHALL tell the agent to reply exactly `HEARTBEAT_OK` when nothing needs the human's attention. If the agenda file is missing, or holds only whitespace, markdown headings and empty list items, the beat SHALL be skipped without a model call and recorded as `skipped`.

When scheduled beats start being skipped for a reason (the binding's last outcome was not a skip for the same reason), the bridge SHALL post one notice to that binding naming the reason and how to fix it or turn the heartbeat off. Later skips for the same reason SHALL post nothing.

#### Scenario: No agenda file

- **WHEN** a beat comes due and `HEARTBEAT.md` does not exist
- **THEN** no agent run starts, `/heartbeat status` shows the last beat as skipped, and the chat gets one `Heartbeat due at HH:MMZ skipped: HEARTBEAT.md does not exist. …` notice

#### Scenario: Still no agenda file

- **WHEN** the next beat comes due and `HEARTBEAT.md` still does not exist
- **THEN** the beat is skipped and nothing is posted

### Requirement: Heartbeat turns are quiet unless there is something to say

While a heartbeat turn runs, the bridge SHALL NOT post a queued acknowledgement, a progress card, tool-call cards or intermediate assistant text for it. Quietness SHALL belong to the heartbeat run itself: a part event of the heartbeat run SHALL stay quiet when it is handled after the run ended, and a part event of the run before the beat SHALL be rendered as usual when it is handled while the beat runs. If the final reply is `HEARTBEAT_OK`, or a reply of at most 300 characters whose first or last line is `HEARTBEAT_OK`, nothing SHALL be posted and the beat SHALL be recorded as `silent`. Any other reply SHALL be posted with a plain-text header line `Heartbeat started at HH:MMZ and ran`, where `HH:MMZ` is when the beat started. The header SHALL carry no emoji, so the first line of the agent's own reply sets the tone of the report. A terminal error other than a cancellation SHALL post one line, `Heartbeat started at HH:MMZ and failed: <reason>`, and record it as `error` with the reason.

#### Scenario: Nothing new

- **WHEN** the agent ends a beat with `HEARTBEAT_OK`
- **THEN** nothing appears in the chat

#### Scenario: Something to report

- **WHEN** the agent ends a beat with a two-line report
- **THEN** the chat shows the header `Heartbeat started at 14:00Z and ran` followed by the report, and no progress card or tool cards were posted during the beat

#### Scenario: Late tool event of the previous turn

- **WHEN** a tool event of the human turn before a beat is handled while the beat runs
- **THEN** its tool card is posted as usual

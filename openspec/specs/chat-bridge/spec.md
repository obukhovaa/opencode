# Chat Bridge

## Purpose

Defines the chat bridge's orchestrator core: conditional startup gated on `cfg.Router`, per-identity isolation under `defer recover()`, workspace pinning to `config.WorkingDirectory()`, peer-to-session resolution semantics, multi-peer many-to-one bindings, outbound fan-out across bound peers (text + attachments uniformly, with bounded parallelism), inbound attribution envelope, direct in-process `agent.Run` / `permission.Service` / `question.Service` invocation (no HTTP loopback), the per-session dispatcher's dual-channel select (inbound NEVER-drop, parts drop-oldest), chat commands, the `FILE:` outbound protocol, typing/tool-update indicators, and the platform-native question UI strategy.
## Requirements
### Requirement: Conditional bridge startup

The bridge SHALL start at `opencode serve` boot iff `config.Get().Router != nil` and at least one channel under `cfg.Router.Channels` has `enabled: true` with at least one enabled identity. Otherwise the bridge MUST remain silently disabled and MUST NOT block opencode's other subsystems (API server, TUI) from starting.

#### Scenario: Router section absent

- **WHEN** `.opencode.json` has no `router` key
- **THEN** opencode boots normally, the bridge does not start, and `/health` reports `bridge: {status: "disabled"}`

#### Scenario: Router present but all channels disabled

- **WHEN** `cfg.Router.Channels.Telegram.Enabled == false` and `cfg.Router.Channels.Slack.Enabled == false` and `cfg.Router.Channels.Mattermost.Enabled == false`
- **THEN** opencode boots normally, the bridge does not start, and `/health` reports `bridge: {status: "disabled"}`

#### Scenario: Single channel enabled

- **WHEN** `cfg.Router.Channels.Telegram.Enabled == true` with at least one enabled bot identity
- **THEN** the bridge starts the Telegram adapter; Slack and Mattermost adapters remain inactive; `/health` reports per-identity status

#### Scenario: Misconfigured router

- **WHEN** `.opencode.json` contains a `router` section with an invalid value (unknown channel kind, missing required field on an identity)
- **THEN** opencode boots normally, the bridge logs the validation error once, and `/health` reports `bridge: {status: "error", error: "<message>"}`

### Requirement: Per-identity startup isolation

Per-identity startup failure (bad token, auth rejected, transport error) MUST NOT prevent other identities or other channels from coming up. Each adapter goroutine and the orchestrator's run handler MUST wrap work in `defer recover()` and log panics without propagating, so a single bridge failure domain cannot take down the opencode API server.

#### Scenario: One identity has a bad token

- **WHEN** the bridge starts with Slack `default` (valid token) and Slack `secondary` (invalid token) and Telegram `default` (valid token)
- **THEN** Slack `default` and Telegram `default` come up and accept messages; Slack `secondary` is marked disabled with a clear auth-rejected error in `/health.bridge.adapters["slack:secondary"]`

#### Scenario: Adapter goroutine panics mid-run

- **WHEN** an adapter goroutine panics during inbound dispatch
- **THEN** the panic is recovered, logged at error level, and the adapter continues processing subsequent events; opencode's API server is unaffected

### Requirement: Workspace pinning

The bridge SHALL operate exclusively in `config.WorkingDirectory()`. The bridge MUST NOT honor per-identity `directory` fields, per-peer binding rows, or a `/dir` chat command. One opencode process equals one workspace.

#### Scenario: Inbound message resolves to working directory

- **WHEN** an inbound chat message arrives on any adapter
- **THEN** the orchestrator uses `config.WorkingDirectory()` as the workspace for session resolution; no per-peer binding lookup occurs

### Requirement: Peer-to-session resolution

For each inbound message the orchestrator SHALL resolve the peer (identified by `(project_id, channel, identity_id, peer_id)`) to an opencode session via `bridge_sessions`. Resolution rules:

1. If a binding row exists and `session_id IS NOT NULL`, use that session.
2. If the row exists but `session_id IS NULL` (opencode garbage-collected the underlying session via FK `ON DELETE SET NULL`), create a fresh session and `UPDATE` the row.
3. If no row exists for this peer:
   - If the bridge is in **router-initiated mode for this peer** (a session expects this peer but it hasn't been bound yet — see `chat-bridge-router-initiated` spec), the row is created at bind time, not at inbound time.
   - Otherwise (user-initiated DM with no prior context), create a new opencode session AND a new `bridge_sessions` row pointing at it.

#### Scenario: First message from a new peer (user-initiated)

- **WHEN** an inbound message arrives from a peer with no `bridge_sessions` row AND no prior `/router/bind` call has reserved this peer for a session
- **THEN** the orchestrator creates a new opencode session via `internal/session` and inserts the `bridge_sessions` row with the new `session_id`

#### Scenario: Pointer NULLed by opencode session GC

- **WHEN** the `bridge_sessions` row exists but its `session_id` is `NULL`
- **THEN** the orchestrator treats it as a fresh peer, creates a new opencode session, and `UPDATE`s the row to point at the new `session_id`

#### Scenario: Inbound matches a pre-bound peer

- **WHEN** an inbound message arrives from a peer that was bound via `/router/bind` earlier (so a row exists with `session_id` set to the bound session)
- **THEN** the orchestrator resolves to the bound `session_id` — no new session is created

### Requirement: Multi-peer session bindings (many-to-one)

A single opencode `session_id` SHALL be allowed to appear in multiple `bridge_sessions` rows, one per `(channel, identity_id, peer_id)` triple. Multi-reviewer interactive flow steps depend on this — a step's session can be bound to several reviewers concurrently.

#### Scenario: Two reviewers bound to one session

- **WHEN** `/router/bind` is called with `{sessionId: "S", peers: [{channel:"slack",..., peerId:"D1"}, {channel:"telegram",..., peerId:"12345"}]}`
- **THEN** two `bridge_sessions` rows exist, both with `session_id == "S"`, distinguished by their `(channel, identity_id, peer_id)` PKs

#### Scenario: Inbound from any bound reviewer resolves correctly

- **WHEN** inbound arrives from Telegram `12345` while session `S` is also bound to Slack `D1`
- **THEN** the inbound resolves to session `S` (looking up by Telegram peerId); the Slack binding is untouched

### Requirement: Outbound fan-out across all bound peers

When the agent produces output for a session bound to N peers, the bridge SHALL fan the output out to every bound peer. Fan-out MUST cover **both text and any attachments uniformly** — a single agent turn that emits `FILE:/path/to/foo.pdf` alongside its text MUST result in every bound peer receiving both the text AND the file (subject to each platform's per-peer size limits). The fan-out MUST use a bounded parallel worker pool (cap 4) so that one slow peer cannot stall delivery to others. Per-peer failures (DM closed, user blocked, transport error, oversize file for one platform) MUST be logged with `lastError` + `lastFailureAt` on the corresponding `/router/health` identity entry and MUST NOT prevent delivery to remaining peers. The agent MUST NOT be informed of per-peer failures during the conversation.

#### Scenario: Three reviewers, one fails

- **WHEN** the agent emits a message for a session bound to 3 reviewers and the second reviewer's DM channel was deleted by the user
- **THEN** reviewers 1 and 3 receive the message; reviewer 2's failure is logged and surfaced in `/router/health`; the agent's next turn proceeds as normal

#### Scenario: Same-thread reviewers de-duplicate to one delivery

- **WHEN** two reviewer entries point at the same Slack thread `(C123|ts456)` (same destination)
- **THEN** the bridge creates ONE `bridge_sessions` row (the second insert is a no-op or merge), and outbound delivers ONE message per turn to that thread

#### Scenario: Files fan out alongside text

- **WHEN** the agent emits a turn containing both text and a `FILE:/workspace/.opencode/bridge/media/report.pdf` line, for a session bound to three peers across Slack, Telegram, and Mattermost
- **THEN** each peer receives both the surrounding text AND the report.pdf attachment via its platform's native upload path; each upload is bounded by that platform's size limit independently

#### Scenario: One peer's platform size limit rejects the file

- **WHEN** the agent emits a 200 MB file for a session bound to peers on Telegram (50 MB limit) and Slack (1 GB limit)
- **THEN** the Slack peer receives the file; the Telegram peer's delivery is logged with `lastError` mentioning the size limit; the agent is not informed; the next turn proceeds normally

### Requirement: Inbound attachments pinned to the source peer

When inbound from one bound peer carries a file attachment (Slack `file_share`, Mattermost `post.files`, Telegram photo/document/audio/video), the bridge SHALL:

1. Download the file to `<config.Data.Directory>/bridge/media/` (existing media store).
2. Push the inbound onto the per-session dispatcher with the local file path included as a `message.Attachment` alongside the attribution-enveloped text.
3. NOT re-broadcast the file to other bound peers. The file is visible only to the agent (via the local path) and to the source peer (where it originated). Other peers see only the text — if any was sent.

If the agent's subsequent turn wants to share the file with other reviewers, it MUST do so explicitly via a `FILE:` line or the `router_send` tool — both of which route through the standard outbound fan-out path.

#### Scenario: Alice DMs the bot a PDF; Bob and Carol stay quiet

- **WHEN** Alice (Slack DM `D012ALICE`) sends "Please review this" with a PDF attachment to a session also bound to Bob and Carol
- **THEN** the agent receives the inbound as `[<@U01ALICE> via slack]: Please review this` with `attachments: [/workspace/.opencode/bridge/media/abc.pdf]`; Bob and Carol's DMs are unchanged

#### Scenario: Agent redistributes the file in its next turn

- **WHEN** the agent's next turn emits `FILE:/workspace/.opencode/bridge/media/abc.pdf` with surrounding text "Sharing what Alice sent"
- **THEN** the standard outbound fan-out delivers the file to ALL bound peers (including back to Alice — platforms tolerate echo); this is the agent's explicit decision, not an automatic bridge action

### Requirement: Reviewer attribution envelope on inbound

When inbound arrives from a peer bound to a session with multiple peers, the bridge SHALL prepend an attribution envelope to the text before pushing to the per-session dispatcher: `[<mention_handle or peerId> via <channel>]: <raw text>`. When the session has only one bound peer, the envelope MAY be omitted (single-reviewer flows don't need attribution; reviewer identity is unambiguous). The bridge's prompt-builder MUST strip echoed envelopes from outbound text so the attribution does not appear in agent-emitted messages.

#### Scenario: Attribution prepended in multi-reviewer case

- **WHEN** Bob (mention_handle `<@U07BOB>`) replies "Looks good" in a session bound to 3 reviewers
- **THEN** the agent receives the inbound as `[<@U07BOB> via slack]: Looks good`

#### Scenario: Envelope stripped from outbound

- **WHEN** the agent's outbound text incidentally contains `[<@U07BOB> via slack]: ...` (echoed from history)
- **THEN** the bridge strips the envelope before posting to chat platforms so reviewers don't see the attribution syntax in agent messages

#### Scenario: Single-peer session may omit envelope

- **WHEN** a session has exactly one bound peer and that peer sends an inbound message
- **THEN** the bridge MAY pass the raw text to the dispatcher without an attribution envelope (implementation choice — consistency with multi-peer case is also acceptable)

### Requirement: Inbound dispatch via direct in-process calls

The bridge SHALL invoke `app.ActiveAgent().Run(ctx, sessionID, content, maxTurnsOverride, attachments...)` directly. The bridge MUST NOT perform any HTTP loopback to a `session.prompt` endpoint or any other opencode internal endpoint. Permission and question replies MUST be delivered via direct `permission.Service` and `question.Service` method calls; the bridge MUST NOT use SSE event subscriptions or HTTP `POST /question/{id}/reply` for these flows.

#### Scenario: Inbound message triggers a run

- **WHEN** an inbound chat message resolves to session `S` and is dispatched to the agent
- **THEN** `app.ActiveAgent().Run(ctx, "S", content, ...)` is invoked directly with no intervening HTTP call

#### Scenario: Question reply from chat

- **WHEN** a peer answers a question that was posed via the chat surface
- **THEN** the answer is delivered through `question.Service.Reply(...)` (or equivalent), not via SSE-event subscription or HTTP

### Requirement: Subscribe-before-Run for part events

The orchestrator MUST call `messages.SubscribeParts(ctx)` BEFORE invoking `agent.Run` for any inbound message dispatch, and MUST drain the resulting channel for the lifetime of the run. The order is load-bearing because `internal/message/message.go:76-89`'s `PublishPart` has a zero-subscribers fast path: events emitted before any subscriber attaches are dropped at the publisher and not buffered for late subscribers.

#### Scenario: Ordering preserved across runs

- **WHEN** the orchestrator dispatches a run for session `S`
- **THEN** the parts subscription is established before `agent.Run` is called, ensuring the first `ToolCall pending` events are delivered to the bridge

#### Scenario: Run completes before drain finishes

- **WHEN** `agent.Run`'s terminal `AgentEvent` channel closes
- **THEN** the orchestrator continues draining the parts subscription until its context is cancelled, so trailing `completed` parts are not dropped

### Requirement: Per-`sessionId` dispatch goroutine with dual-channel select

For each actively-bound `sessionId` the bridge SHALL run exactly **one** dispatcher goroutine that owns both inbound message dispatch and parts demultiplexing for that session. The dispatcher MUST use a single `select{}` over two channels:

| Channel | Capacity | Drop policy | Source |
|---|---|---|---|
| inbound  | 16 | NEVER drop — overflow to per-session slice when full (non-starvation requirement); back-pressure propagates at `s.inboundCh` level | per-peer adapter goroutines via runInboundLoop |
| parts    | 64 | drop-oldest with rate-limited log | broker-receive goroutine non-blocking forward |

The dispatcher MUST call `agent.Run` serially — only one in-flight Run per session at a time. It MUST consume the Run channel's terminal `AgentEvent` before processing the next inbound message.

**`ErrSessionBusy` from cross-actor holders is a legitimate outcome and MUST be handled by content-preserving retry, not by discarding.** The session-run ledger is process-global (`session-run-exclusivity` spec): any holder — a flow step's own agent instance, a cron sentinel lock, a task auto-resume — makes `agent.Run` return `ErrSessionBusy`. The single-dispatcher serialization only prevents bridge-vs-bridge collisions; it cannot prevent cross-actor collisions. When `agent.Run` returns `ErrSessionBusy`, the bridge MUST:

1. Retain the inbound message content — it MUST NOT be discarded.
2. Retry with a short backoff (≤ 200 ms per attempt) for a bounded per-attempt budget.
3. On budget exhaustion, re-queue the message for a later attempt rather than discarding. Content preservation is unconditional and budget-independent.
4. Inform the sender (via `bridge-queue-visibility` if enabled) that their message is waiting behind a run it does not own.

The per-attempt budget is intentionally distinct from the TUI drain worker's unbounded retry: the TUI uses a per-session goroutine isolated from other sessions; the bridge `handleInbound` uses a bounded budget so other items queued behind it can make progress between reattempts, with the message re-entering the queue rather than being discarded.

The dispatcher's lifecycle is tied to the binding: created on first `Bind(sessionId, ...)`, torn down on `Unbind(sessionId)` or when the bridge observes `session_id == NULL` (opencode GC'd the session via FK `ON DELETE SET NULL`).

#### Scenario: Multi-reviewer simultaneous inbound serializes cleanly

- **WHEN** Alice and Bob both reply within milliseconds for the same bound session
- **THEN** their messages land on the per-session inbound channel in arrival order; the dispatcher processes Alice's full agent turn (terminal AgentEvent received) before pulling Bob's message; no `ErrSessionBusy` ever surfaces from bridge-internal serialization

#### Scenario: Inbound queue back-pressures one adapter without dropping

- **WHEN** the per-session inbound buffer (capacity 16) is full and a Telegram adapter's message for that session reaches the shared inbound loop
- **THEN** the message is appended to the session's overflow slice (NOT dropped) and the shared loop moves on without blocking; back-pressure on the adapter applies only when the shared `s.inboundCh` (capacity 64) is full, where Telegram's own server-side buffering absorbs the stall; the bridge MUST NOT lose user messages

#### Scenario: Cross-actor `ErrSessionBusy` is retried, not discarded

- **GIVEN** a flow step's agent instance holds the session slot for session S
- **WHEN** an inbound message M from peer P reaches the dispatcher and `agent.Run` returns `ErrSessionBusy`
- **THEN** M is retained and retried with backoff; M is NOT discarded and the user is NOT told to resend; if the per-attempt budget expires, M is re-queued for a subsequent attempt via the overflow mechanism, never dropped

#### Scenario: Budget expires; message re-queued, not discarded

- **GIVEN** a flow step holds session S's slot for longer than the per-attempt retry budget
- **WHEN** the budget expires without the slot freeing
- **THEN** M's content is preserved and re-queued (via the overflow slice or equivalent); the session's dispatcher may process other pending messages before retrying M; M is eventually delivered when the slot frees; the sender's queued-ack is updated

#### Scenario: Retry succeeds when the competing run finishes

- **GIVEN** M was re-queued after an `ErrSessionBusy` from a flow-step holder
- **WHEN** the flow step's run completes and releases the session slot
- **THEN** the dispatcher's next attempt of `agent.Run` succeeds; M's full content is delivered to the agent as if it had arrived after the competing run

#### Scenario: Parts overflow drops oldest, logs once per session per minute

- **WHEN** a session emits more than 64 part events while its sender is blocked on outbound IO
- **THEN** the oldest part is dropped, the newest appended, and a warn-level overflow log is emitted (rate-limited to once per session per minute, formatted `bridge: part-queue overflow session=<id> dropped=<n>`)

#### Scenario: Broker receive loop never blocks

- **WHEN** the Mattermost adapter wedges in a slow `chat.postMessage` round-trip and the parts queue fills
- **THEN** the process-wide broker receive goroutine continues to drain (non-blocking forward to the per-session queue with drop-oldest fallback); TUI rendering for other sessions is unaffected

### Requirement: Per-peer adapter inbound serialization

Each adapter MAY use a per-`(channel, identity, peerKey)` goroutine for adapter-internal inbound handling (e.g., de-duplication of platform retries, mention extraction). This per-peer goroutine MUST push attributed inbound onto the per-session dispatcher's inbound channel — it MUST NOT call `agent.Run` directly. The agent.Run invocation is the dispatcher's sole responsibility.

#### Scenario: Adapter receives platform-retry duplicate

- **WHEN** Mattermost re-delivers the same `posted` event due to a transient WebSocket reconnect
- **THEN** the per-peer goroutine de-duplicates and only one inbound message reaches the per-session dispatcher

### Requirement: Chat command surface

The bridge SHALL recognize the following chat commands in inbound messages and handle them in-process via direct service calls: `/agent`, `/model`, `/sessions`, `/session`, `/reset`, `/pair`, `/skip`, `/help`. The bridge MUST NOT implement `/dir` (workspace is fixed). Each command's behavior matches the TS bridge's `bridge.ts` chat-command implementation.

#### Scenario: `/model <id>` switches the active model

- **WHEN** a peer sends `/model claude-sonnet-4-5`
- **THEN** the bridge invokes the agent/model selection path in-process and confirms the switch in the chat surface

#### Scenario: `/dir` command rejected

- **WHEN** a peer sends `/dir /some/path`
- **THEN** the bridge replies that workspace switching is not supported in this deployment

### Requirement: Outbound FILE: protocol

The bridge SHALL recognize the FILE: outbound convention in agent messages and route detected file references through the media store at `<config.Data.Directory>/bridge/media/`. The parser behavior matches the TS bridge's outbound parser.

#### Scenario: Agent emits FILE: reference

- **WHEN** the agent output contains a `FILE:<path>` token
- **THEN** the bridge uploads the file via the platform-appropriate adapter call (Telegram `sendDocument`, Slack `files.upload`, Mattermost multipart) and emits the surrounding text as a separate message

### Requirement: Per-session typing/reporting indicators

The bridge SHALL emit platform-appropriate typing indicators while a run is in flight for a session, and SHALL surface tool-call activity to the chat surface when `cfg.Router.ToolUpdatesEnabled` is true — as one progress card per run at `compact` verbosity, or as one card per tool call at `full`. Indicator emission MUST NOT block the inbound dispatch loop.

A heartbeat turn (see the `bridge-heartbeat` capability) is the exception: the bridge SHALL NOT surface tool-call activity for it — no progress card and no per-call cards — whatever `cfg.Router.ToolUpdatesEnabled` and the verbosity are. Whether a part event belongs to a heartbeat turn SHALL be decided by the run that received it, so a heartbeat run's late event stays quiet and a human run's late event handled while a beat runs is surfaced.

#### Scenario: Tool updates enabled

- **WHEN** `cfg.Router.ToolUpdatesEnabled == true` and a tool transitions from `pending` to `running`
- **THEN** the bridge reflects the transition on the chat surface — on the run's progress card at `compact`, which names the most recently started unfinished tool, or as a `🔧 <tool>#<id>` per-call card at `full`

#### Scenario: Tool updates enabled at compact

- **WHEN** `cfg.Router.ToolUpdatesEnabled == true`, the live verbosity is `compact`, and a tool call completes
- **THEN** the run's progress card is updated in place to reflect the new completion count; no separate message is posted for the call

#### Scenario: Tool updates enabled at full

- **WHEN** `cfg.Router.ToolUpdatesEnabled == true`, the live verbosity is `full`, and a tool call completes
- **THEN** the bridge sends a per-call status card reflecting the transition, carrying the argument summary and a rune-capped result body

#### Scenario: Tool updates disabled

- **WHEN** `cfg.Router.ToolUpdatesEnabled == false`
- **THEN** the bridge posts no progress card and no per-call cards, but still emits typing indicators, the intermediate assistant text of a bridge-dispatched run (see "Intermediate assistant text relay"), the final agent reply, and a one-line reason for any failed tool call

#### Scenario: Heartbeat turn

- **WHEN** `cfg.Router.ToolUpdatesEnabled == true` and a tool of a heartbeat turn transitions
- **THEN** nothing is sent for the transition

### Requirement: Compact tool updates are one progress card per run, updated in place

A tool update is a PROGRESS INDICATOR, not a transcript. In the default `compact` verbosity, when `cfg.Router.ToolUpdatesEnabled` is true, the bridge SHALL post exactly one progress message per agent run and SHALL update that message in place as the run proceeds. It SHALL NOT post a message per tool call.

The card SHALL be posted when the run starts, reading `Thinking...`. Each subsequent update SHALL carry the real number of tool calls completed so far in the run (`5 tool calls done`, `8 tool calls done`, …), the elapsed time since the card was created, and, when a call is known to be in flight, the name of the most recently started unfinished tool. When one or more calls have failed the card SHALL also carry the failure count and the most recent failure's reason, flattened to one line and rune-capped. When the run ends the card SHALL be updated one final time to a terminal state that names the total tool-call count and elapsed time, distinguishing a run that ended normally from one whose agent errored.

In `compact` the bridge SHALL NOT send tool ARGUMENTS or successful result BODIES to the chat surface; both are durably recorded in the session store (`messages.parts`) and in the telemetry backend, which is where investigation belongs.

Card updates SHALL be serialised so that a later count never overwrites an earlier one out of order, and SHALL be paced: completions arriving within the pacing interval collapse into one update carrying the latest counts. In-place editing SHALL use the platform's native edit call (Slack `chat.update`, Telegram `editMessageText`, Mattermost post update) through the optional `MessageEditor` adapter capability. If an edit fails the bridge SHALL post the card fresh to that peer and continue editing the new message.

The card SHALL be created only if the live verbosity is `compact` when the run starts; once created it SHALL track every completion of that run regardless of later verbosity switches.

A run that starts at `full` has no card, so a mid-run switch to `compact` leaves it with neither. For the remainder of such a run the bridge SHALL stay silent about calls STARTED after the switch — the reviewer asked for less noise — but SHALL still emit the result of any call whose pending per-call card was already posted. Adapters pair a result to its call card by tool-call ID and edit it in place, so an unemitted result strands a `🔧` card that reads as a tool still running. Failures surface either way.

`cfg.Router.ToolUpdateVerbosity` selects the level: `compact` (default) or `full`, where `full` posts one card per tool call carrying the argument summary and a rune-capped result body. The values `verbose` and `debug` SHALL be accepted as aliases of `full`. An absent or unrecognised value SHALL resolve to `compact` — the quiet option is the fail-safe — and an unrecognised value SHALL be logged once at WARN.

#### Scenario: Run start posts the card

- **WHEN** a bound peer's message starts an agent run with `toolUpdatesEnabled: true` at `compact`
- **THEN** one message reading `⏳ Thinking...` is posted to every bound peer whose adapter implements `MessageEditor`, before any tool call completes

#### Scenario: Successful multi-call run updates one message

- **WHEN** the run makes eight tool calls that all succeed and then ends normally
- **THEN** the chat surface shows one progress message, updated in place through states such as `⏳ 5 tool calls done · 1m12s` and ending as `✓ Done · 8 tool calls · 3m40s`; no per-call message exists, and neither arguments nor result bodies appear in chat

#### Scenario: Failed call folds into the card

- **WHEN** the third call of the run fails with a multi-line error body
- **THEN** the card shows `1 failed` and a second line `✗ <tool>#<id> · <reason>` with the body flattened to one line and truncated to the failure-reason cap; no separate failure message is posted to peers whose adapter edits in place

#### Scenario: Burst of completions is coalesced

- **WHEN** ten tool calls complete within one pacing interval
- **THEN** the card receives fewer than ten edits and the last edit reads `10 tool calls done`

#### Scenario: Agent error ends the card as failed

- **WHEN** the agent run terminates with an error event after four tool calls
- **THEN** the card's final state reads `✗ Run failed · 4 tool calls · <elapsed>`

#### Scenario: Unset verbosity resolves to compact

- **WHEN** `.opencode.json` sets `router.toolUpdatesEnabled: true` and omits `router.toolUpdateVerbosity` (or sets it to an unknown value such as `"chatty"`)
- **THEN** the bridge renders the progress card; the unknown value additionally produces one WARN naming the configured value and the mode actually used

#### Scenario: Switching to compact mid-run still closes open call cards

- **WHEN** a run starts at `full`, posts a `🔧 <tool>#<id>` card for a call, and a reviewer sends `/verbosity compact` before that call completes
- **THEN** the call's completion is still emitted, resolving the pending card; a call started after the switch produces no message at all, and any failure still surfaces

#### Scenario: Verbose and debug are aliases of full

- **WHEN** `router.toolUpdateVerbosity` is `"verbose"` or `"debug"`
- **THEN** the bridge renders per-call cards with arguments and result bodies, and reports the live level as `full`

### Requirement: Reviewers can switch tool-update verbosity at runtime

The bridge SHALL expose a `/verbosity` chat command that reports the live verbosity and switches it between `compact` and `full`, accepting `verbose` and `debug` as aliases of `full`. The switch SHALL take effect for every bound session without a restart, and SHALL NOT be written back to `.opencode.json` — a reviewer enabling detail to watch one run MUST NOT silently reconfigure the deployment. A restart therefore returns to the configured value. An unknown mode SHALL be rejected with a usage reply and leave the live value unchanged.

#### Scenario: Reviewer asks for detail mid-run

- **WHEN** a reviewer sends `/verbosity full` on a bound peer while a run is in flight
- **THEN** the bridge replies with the applied mode, and every subsequent tool call in that process — including on other bound sessions — renders a per-call card with argument and result detail until the mode is switched back or the process restarts; a progress card already open for the run keeps counting to its terminal state

#### Scenario: Reviewer lists the modes

- **WHEN** a reviewer sends `/verbosity` with no argument
- **THEN** the bridge lists `compact` and `full` with one-line descriptions and marks the live one active

#### Scenario: Unknown mode is rejected

- **WHEN** a reviewer sends `/verbosity chatty`
- **THEN** the bridge replies with the accepted values and the live verbosity is unchanged

### Requirement: Bridge restart loses in-flight runs

When `opencode serve` restarts, any in-flight agent runs MUST be considered lost. The bridge MUST NOT attempt to resume runs across process restarts. On the next inbound message from a peer with a NULLed or stale pointer, the bridge creates a fresh session.

#### Scenario: opencode restart mid-run

- **WHEN** opencode is restarted while a run for session `S` is in flight
- **THEN** the run is lost when the process exits, and the next inbound message from `S`'s peer either continues the same `bridge_sessions` pointer (if the session row survived) or creates a new session

### Requirement: Platform-native question UI

When `cfg.Router.QuestionMode == "interactive"` AND a question request from `question.Service` has exactly one prompt AND the prompt has at least one option, the bridge SHALL attempt to render the question using platform-native interactive UI:

- **Slack**: `chat.postMessage` with an actions block containing one button per option.
- **Telegram**: `sendMessage` with `reply_markup.inline_keyboard` carrying one row per option.
- **Mattermost**: NOT supported in v1. Interactive attachments require a publicly-reachable webhook URL the bridge does not host. Mattermost peers always use the numbered-options fallback regardless of `QuestionMode`.

Per-platform adapters that support platform-native question UI satisfy an optional `InteractiveQuestionSender` interface. Adapters that do NOT satisfy it (Mattermost in v1) and adapters that satisfy it but fail at send time (e.g. Slack scope missing) trigger per-peer fallback to the numbered-options text rendering. Button click callbacks (Slack `block_actions`, Telegram `callback_query`) are normalized into the same `bridge.Inbound` shape as text replies — `Inbound.Text` is the canonical option label — so the inbound reply parser (`parseQuestionAnswers`) handles them via the same code path as numbered text.

When `cfg.Router.QuestionMode != "interactive"` (default / empty / "disabled"), or when the question has more than one prompt, or when no options are present, the bridge SHALL skip the interactive path entirely and use the numbered-options text rendering for every peer.

#### Scenario: Slack peer + QuestionMode=interactive + single-option prompt

- **WHEN** the agent calls the `question` tool with a single prompt that has 2 options, and the session is bound to a Slack peer with `QuestionMode == "interactive"`
- **THEN** the bridge calls Slack `chat.postMessage` with an actions block containing two buttons (one per option); the reviewer's click is normalized into an inbound whose Text equals the chosen option's label; `question.Service.Reply` is invoked with that label

#### Scenario: Mattermost peer always falls back to text

- **WHEN** the agent calls the `question` tool against a session bound to a Mattermost peer, regardless of `QuestionMode`
- **THEN** the bridge sends a numbered-options text message via the standard `Send` path; the reviewer's numeric reply is parsed back to the chosen option

#### Scenario: Slack interactive send fails → fallback to text

- **WHEN** the Slack adapter's `SendInteractiveQuestion` returns an error (missing `chat:write` scope, deprecated block kind, etc.) for a peer
- **THEN** the bridge logs the failure at info level and falls back to the numbered-options text rendering for that peer only; other bound peers continue using their respective UI

#### Scenario: Multi-prompt question always uses text

- **WHEN** the agent's question request carries more than one prompt
- **THEN** the bridge bypasses the interactive path for every peer (the actions-block widget can't represent multi-prompt) and renders the prompts as numbered-options text

### Requirement: Bridge suppresses tool-update indicators for synthetic messages

The bridge's per-session tool-update indicator emission path SHALL skip any `PartEvent` whose `Synthetic` flag is `true`. Synthetic Assistant messages produced by `task.EnqueueTaskCompletion` (background bash completions, async task completions, monitor events, cron-fired completions) MUST NOT trigger any outbound chat indicator activity. This requirement covers ALL synthetic message sources uniformly — the bridge filter is keyed off the `Synthetic` flag, not off the originating tool name.

#### Scenario: Cron-fired completion does not emit a tool indicator

- **WHEN** a cron job fires and `EnqueueTaskCompletion` writes a synthetic Assistant(ToolCall name=task) + Tool(ToolResult) pair
- **THEN** the bridge's parts demux observes the Assistant message, sees `Synthetic = true`, and does NOT emit a 🔧 task indicator to the bound chat platform; the next REAL assistant message (the agent's reply to the synthetic ToolResult) DOES fan out to chat as a normal text reply

#### Scenario: Background bash completion does not emit a tool indicator

- **WHEN** a background bash subprocess exits and `EnqueueTaskCompletion` writes a synthetic pair
- **THEN** the bridge does NOT emit a 🔧 bash indicator; the agent's human-readable reaction to the completion DOES flow to chat

#### Scenario: Real (non-synthetic) tool calls still emit indicators

- **WHEN** the agent invokes a real tool call (e.g., a synchronous bash, a read, a grep) — the PartEvent's `Synthetic` flag is `false`
- **THEN** the bridge emits the appropriate 🔧 indicator as it does today; no behavior change for non-synthetic messages

### Requirement: Intermediate assistant text relay

During a bridge-dispatched run, the bridge SHALL relay to every peer bound to the
session the text of each assistant message that ended in `tool_use`, not only the
run's terminal message. The relayed message SHALL start with a header line
`⌛ <tool names>`, listing the message's tool calls in call order with duplicate names
kept, followed by a newline and the message text. `FILE:` tokens SHALL be handled as
they are for the terminal reply.

The bridge SHALL read the message from the session store when it receives the
`ToolCall` part that carries the call's merged input (`Finished` and non-empty
`Input`), and SHALL send the text before it emits anything else for that call. Parts
from descendant (subagent) sessions, synthetic parts and streaming parts without
input SHALL NOT trigger the relay.

The relay SHALL cover only the bridge-dispatched run itself: parts published before
the run started — by another actor's run that held the session while the bridge
waited for it — SHALL NOT be relayed. A part SHALL be checked against the run that
received it, even when it is handled after that run ended.

A heartbeat turn (see the `bridge-heartbeat` capability) SHALL NOT have its
intermediate text relayed: only its final reply, under the heartbeat header, or its
one-line failure reaches the chat. As above, a part SHALL be checked against the run
that received it.

Each assistant message SHALL be posted at most once per run, whichever of the
terminal path, the intermediate path or the question flush reaches it first. The
terminal reply SHALL carry no header. Before posting it, the bridge SHALL wait for
intermediate posts of the run that are still in flight, for at most 5 seconds.

One intermediate post SHALL be bounded to 10 seconds, or 60 seconds when it carries
attachments, so a chat API that does not answer cannot stall the session's tool
updates.

This relay SHALL run at every tool-update verbosity, including with tool updates
disabled. It SHALL NOT change how tool calls, tool results or the progress card are
rendered.

#### Scenario: Text before a tool call

- **WHEN** an assistant message with text ends in `tool_use` with one `bash` call
- **THEN** the bridge posts `⌛ bash` followed by the text, once

#### Scenario: Several tool calls

- **WHEN** the message calls `bash` and then `question`
- **THEN** the header reads `⌛ bash, question`

#### Scenario: No text

- **WHEN** the `tool_use` message has no text and no attachments
- **THEN** nothing extra is posted

#### Scenario: Final reply

- **WHEN** the run's terminal message ends the turn with text
- **THEN** it is posted once, with no header

#### Scenario: Terminal message already relayed

- **WHEN** the terminal event carries a message the intermediate path already posted, or the intermediate trigger arrives for a message the terminal path already posted
- **THEN** no second post is made

#### Scenario: Text above the full-verbosity call card

- **WHEN** the live verbosity is `full` and tool updates are enabled
- **THEN** the intermediate text is sent before the call's 🔧 card

#### Scenario: Subagent, synthetic and streaming parts

- **WHEN** the triggering part belongs to a subagent session, is synthetic, or is a streaming publish with empty input
- **THEN** nothing is posted and the store is not read

#### Scenario: No bridge run in flight

- **WHEN** a part was not received by a bridge-dispatched run (e.g. a self-started turn), or a question arrives while no bridge-dispatched run is in flight
- **THEN** nothing is posted

#### Scenario: Another actor's run held the session

- **WHEN** the bridge retried `ErrSessionBusy` while another actor's run on the session completed a `tool_use` message with text
- **THEN** that message is not relayed, and the bridge's own run's messages are

#### Scenario: Late part of a finished run

- **WHEN** a part of a run is handled after that run ended, whether or not the next run has started
- **THEN** it is checked against the run that received it: a message that run already posted is not posted again, and one it had not posted yet is posted

#### Scenario: Final reply after an in-flight intermediate post

- **WHEN** the terminal reply is ready while an intermediate post of the same run is still being sent
- **THEN** the terminal reply is posted after that post returns, or after 5 seconds

#### Scenario: Heartbeat turn

- **WHEN** an assistant message of a heartbeat turn ends in `tool_use` with text
- **THEN** nothing is posted for it

### Requirement: Question widget follows its text

Before posting a `question` prompt for a session with a bridge-dispatched run in
flight, the bridge SHALL make sure the text of the session's latest assistant message
has been relayed. If no path has posted it yet, the question router SHALL claim it and
send it without blocking the router on the send. Whether the router sends it or
another path is already sending it, the router SHALL wait for that post to finish, for
at most 5 seconds, then post the widget. A send still running at that point SHALL NOT
be cancelled; the text is still delivered, once. The router SHALL NOT create a
dispatcher to do so.

#### Scenario: Text then widget

- **WHEN** the agent writes text and then calls `question`
- **THEN** the chat shows `⌛ question` with the text, and then the question widget

#### Scenario: Slow chat API

- **WHEN** the send of the pre-question text takes longer than 5 seconds
- **THEN** the widget is posted after 5 seconds and the text is still delivered, once

### Requirement: Inbound dispatch MUST NOT starve other sessions (cross-session non-starvation)

The bridge's shared `runInboundLoop` (`service.go:426-438`) processes all sessions through
a single goroutine, calling `dispatchInbound` synchronously for each message. To prevent
one stalled session from blocking dispatch to all others, `dispatchInbound`'s push to any
per-session `d.inbound` channel MUST be non-blocking from the shared loop's perspective.
When a session's `d.inbound` channel is full, the message SHALL be appended to a per-session
in-memory overflow slice on `sessionDispatch` rather than blocking `runInboundLoop`. The
per-session `run()` goroutine SHALL drain the overflow slice after each `handleInbound` call
completes, before reading the next message from `d.inbound`, preserving per-session FIFO.

This non-starvation requirement is independent of `ErrSessionBusy` handling — it applies to
the push path regardless of why a session's channel is full. "NEVER drop (back-pressure
adapter instead)" refers to back-pressuring the per-adapter pump goroutine at the
`s.inboundCh` (cap 64) level; it never meant the shared loop should block per-session.

#### Scenario: Session A full; session B dispatched without delay

- **GIVEN** session A's `d.inbound` channel is full (a long-running agent turn is in flight)
- **WHEN** messages arrive for both session A and session B via `runInboundLoop`
- **THEN** session B's message is dispatched immediately; session A's message is stored in
  A's overflow slice and delivered after A's current turn completes; neither message is
  dropped; `runInboundLoop` is NOT blocked

#### Scenario: Overflow slice drains in FIFO order

- **GIVEN** session A has 3 messages in its overflow slice (accumulated while channel was
  full) and A's current run just completed
- **WHEN** `run()` exits `handleInbound` and checks the overflow slice
- **THEN** the 3 overflow messages are transferred to `d.inbound` in arrival order;
  subsequent reads from `d.inbound` deliver them before any newly arriving messages

### Requirement: No-active-agent drop is audible

When `handleInbound` is reached but `ActiveAgent()` returns nil — meaning no agent is
registered in the process — the bridge MUST NOT silently discard the inbound. The bridge
SHALL reply to the sender's peer with a brief error explaining that no agent is available,
so the sender knows their message was not processed and can retry or escalate.

#### Scenario: Inbound arrives when no agent is configured

- **GIVEN** `app.ActiveAgent()` is nil (e.g. the agent service has not initialized or
  has been torn down)
- **WHEN** an inbound message arrives from peer P for session S
- **THEN** the bridge sends a reply to P explaining the message could not be processed
  due to no active agent; the message is not delivered silently to /dev/null

### Requirement: Interactive-flow buffer overflow is audible

When `QuestionRouter.BufferInbound` evicts the oldest buffered message because the
per-session interactive buffer is full (`interactiveInboundBufferCap`, currently 16), the
bridge MUST reply to the peer whose message was dropped informing them that their input was
not retained and they should resend it after the current interactive step concludes. A
warn-level log entry remains required; the user-visible reply is additive.

#### Scenario: Interactive-buffer overflow evicts message 17

- **GIVEN** session S is in an interactive flow step with 16 messages already buffered and
  no question pending
- **WHEN** a 17th inbound message arrives from peer P
- **THEN** the oldest buffered message (message 1) is evicted; the bridge sends a reply to
  that message's peer notifying them that their input was dropped and should be resent; the
  warn-level log is also emitted

### Requirement: Queued messages logged at WARN on shutdown; in-chat notification not required

When `Service.Stop` tears down a `sessionDispatch` that has messages queued in its inbound
channel or per-session overflow slice, the bridge SHALL drain those messages BEFORE closing
the channel and SHALL emit one WARN log entry per dropped message (session ID, peer ID) plus
a per-session summary count.

In-chat notification to senders at shutdown is NOT required and MUST NOT be specced as even
advisory. `Service.Stop()` cancels the service context before `tearDownDispatchers()` runs;
any adapter API call using that cancelled context returns immediately without reaching the
platform. Speccing unreliable behavior produces tests that pass in timing-lucky runs and
fail silently in production.

Durability across process restarts is explicitly out of scope for this change.

#### Scenario: Shutdown with queued messages

- **GIVEN** session S's dispatcher has 3 messages in its inbound channel when
  `Service.Stop` is called
- **THEN** before closing `d.inbound`, the bridge drains the channel; for each item it logs
  at WARN: `"bridge: shutdown lost queued inbound session=<S> peer=<P>"`; a final WARN
  carries the total count; no in-chat reply is sent or attempted

#### Scenario: Clean shutdown with empty queues

- **GIVEN** all session dispatchers have empty inbound channels and overflow slices at
  shutdown time
- **THEN** no shutdown-loss warn is emitted; shutdown is silent as before

### Requirement: Optional MessageEditor per-adapter capability

An adapter MAY implement `MessageEditor`: `SendEditable` posts a plain-text message and returns an opaque token; `EditMessage` replaces that message's text in place. The three production adapters (Slack, Telegram, Mattermost) SHALL implement it, and their queued-acknowledgement methods SHALL be built on it so each platform has one edit path. The `external` relay adapter does not implement it.

A peer whose adapter does not implement `MessageEditor` SHALL receive nothing for the run's progress card, except that a tool failure no earlier update has delivered SHALL reach it once as the failure's one-line reason in plain text, so a failed call surfaces on text-only peers exactly as before.

#### Scenario: Relay channel receives no progress frames

- **WHEN** a session is bound to a Slack peer and an `external` relay peer, and its run's progress card is updated
- **THEN** the Slack message is edited in place and the relay receives no frame for the update

#### Scenario: Relay channel still receives failures

- **WHEN** the same run's tool call fails
- **THEN** the relay receives one text frame carrying `✗ <tool>#<id> · <reason>`, and the Slack card is edited to show the failure

#### Scenario: Edit failure recovers with a fresh post

- **WHEN** the card's message was deleted and the next update's edit fails
- **THEN** the bridge posts the card as a new message to that peer and edits that message from then on

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
| inbound  | 16 | NEVER drop (back-pressure adapter instead) | per-peer adapter goroutines pushing attributed inbound |
| parts    | 64 | drop-oldest with rate-limited log | broker-receive goroutine non-blocking forward |

The dispatcher MUST call `agent.Run` serially — only one in-flight Run per session at a time. Because all reviewer fan-in goes through this single dispatcher, `agent.Run` MUST NEVER return `ErrSessionBusy` from within the bridge's invocation path. The dispatcher MUST consume the Run channel's terminal `AgentEvent` before processing the next inbound message.

The dispatcher's lifecycle is tied to the binding: created on first `Bind(sessionId, ...)`, torn down on `Unbind(sessionId)` or when the bridge observes `session_id == NULL` (opencode GC'd the session via FK `ON DELETE SET NULL`).

#### Scenario: Multi-reviewer simultaneous inbound serializes cleanly

- **WHEN** Alice and Bob both reply within milliseconds for the same bound session
- **THEN** their messages land on the per-session inbound channel in arrival order; the dispatcher processes Alice's full agent turn (terminal AgentEvent received) before pulling Bob's message; no `ErrSessionBusy` ever surfaces

#### Scenario: Inbound queue back-pressures one adapter without dropping

- **WHEN** the per-session inbound buffer (capacity 16) is full and a Telegram adapter tries to push another message
- **THEN** the adapter's push goroutine blocks (NOT drops); Telegram's own server-side buffering absorbs the stall; the bridge MUST NOT lose user messages

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

The bridge SHALL emit platform-appropriate typing indicators while a run is in flight for a session, and SHALL surface tool-transition status (`[tool] pending|running|completed`) to the chat surface when `cfg.Router.ToolUpdatesEnabled` is true. Indicator emission MUST NOT block the inbound dispatch loop.

#### Scenario: Tool updates enabled

- **WHEN** `cfg.Router.ToolUpdatesEnabled == true` and a tool transitions from `pending` to `running`
- **THEN** the bridge sends a short status message to the chat surface reflecting the transition

#### Scenario: Tool updates disabled

- **WHEN** `cfg.Router.ToolUpdatesEnabled == false`
- **THEN** the bridge suppresses per-tool transition messages but still emits typing indicators and the final agent reply

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

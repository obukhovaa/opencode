# Chat Bridge

The chat bridge connects `opencode serve` to external chat platforms — Telegram, Slack, and Mattermost — so an agent can be driven from any of them and replies fan back through the same channels. The bridge runs **in-process** inside `opencode serve` (no separate router process), and HTTP routes are mounted under `/router/*` on the existing API port.

The bridge replaces the legacy out-of-process `opencode-router` Node service. Migration guide: [interoperability/openwork/DEPLOY.md → Cutover from the TS bridge](../interoperability/openwork/DEPLOY.md#cutover-from-the-ts-bridge).

## Quick Start

Add a `router` section to `.opencode.json`:

```json
{
  "router": {
    "questionMode": "interactive",
    "permissionMode": "allow",
    "toolUpdatesEnabled": true,
    "channels": {
      "telegram": {
        "enabled": true,
        "bots": [
          {
            "id": "default",
            "token": "<BOT_TOKEN>",
            "enabled": true,
            "access": "private",
            "pairingCodeHash": "<SHA256_HEX>"
          }
        ]
      },
      "slack": {
        "enabled": true,
        "apps": [
          {
            "id": "default",
            "botToken": "xoxb-...",
            "appToken": "xapp-...",
            "enabled": true
          }
        ]
      },
      "mattermost": {
        "enabled": true,
        "instances": [
          {
            "id": "local",
            "serverUrl": "https://mm.example.com",
            "accessToken": "<TOKEN>",
            "enabled": true
          }
        ]
      }
    }
  }
}
```

Start the server. The bridge boots automatically when `router` is present and at least one channel has an enabled identity. The startup banner shows per-adapter status:

```
opencode serve --hostname 127.0.0.1 --port 3456

  ⌬ OpenCode HTTP Server
  ──────────────────────
  Listening:  http://127.0.0.1:3456
  Version:    v0.9.5
  Auth:       none
  CORS:       *
  Router:     ok
                mattermost:local         running    0 sessions
                slack:default            running    0 sessions
                telegram:default         running    1 session
```

Health snapshot: `curl http://127.0.0.1:3456/router/health` (per-adapter `status`/`lastError`/`lastInboundAt`/`lastFailureAt`/`boundSessions`).

## Top-level `router` fields

| Field | Values | Description |
|---|---|---|
| `questionMode` | `"interactive"` \| `"disabled"` | When `interactive`, the agent's `question` tool renders Slack actions blocks / Telegram inline keyboards with a numbered-text fallback. When unset or `"disabled"`, the question tool isn't initialized. |
| `questionNudgeIntervalSeconds` | `int` | Idle gap after which the bridge re-posts a "still waiting for your answer" nudge to a session's bound peers when a `question` is outstanding, and the spacing between subsequent nudges. Re-surfaces an answer lost in transit (e.g. a misrouted bridge reply) instead of letting the step hang to the job's hard deadline. `0` → built-in default (5 min); `<0` → disable nudging. |
| `questionNudgeMax` | `int` | Caps how many nudges are sent for a single pending question (so a walked-away reviewer can't be pinged forever). `0` → built-in default (3); `<0` → unlimited (bounded in practice by the job deadline). |
| `permissionMode` | `"allow"` \| `"deny"` \| `"ask"` \| empty | How the bridge resolves agent permission requests on bridge-bound sessions. `allow`/`deny` auto-resolve; `ask`/empty defer to opencode's default UI (will hang headless). Unrecognised values fail-safe to deny with a one-shot WARN log. |
| `toolUpdatesEnabled` | `bool` | Show tool-call progress in chat. The shape is set by `toolUpdateVerbosity`. Failures surface regardless of this flag. |
| `toolUpdateVerbosity` | `"compact"` (default) \| `"full"` | `compact` posts **one progress message per run and edits it in place**: `⏳ Thinking...` when the run starts, then `⏳ 5 tool calls done · running bash · 1m12s` as calls complete (real counts, the tool in flight, elapsed time), and a final `✓ Done · 12 tool calls · 3m40s` (or `✗ Run failed · …`) when the run ends. A failed call adds `· 1 failed` and a second line `✗ <tool>#<id> · <reason>`. No per-tool-call messages; arguments and result bodies stay out of chat (they're in the session store and Langfuse). Edits are paced to one every 2s and coalesced. `full` posts one card per tool call — `🔧 <tool>#<id> · <args>` updated in place to `✓ <tool>#<id> · <duration> · <body>` — with the argument summary and a truncated result body; `verbose` and `debug` are accepted as aliases. Unrecognised values fall back to `compact` with a one-shot WARN. Flip it live with `/verbosity`; switching a run from `full` to `compact` mid-run silences calls started after the switch but still closes any tool card already posted. Peers on the `external` relay channel receive no progress card; they still get a failed call's one-line reason as text. |
| `queueAcknowledgementsEnabled` | `bool` | When `true`, sends an in-place-editable `⏳ queued` acknowledgement to a sender whose message is enqueued behind an in-flight agent run. The ack is edited as the queue drains and resolved to `▶ Processing your message now…` the moment the run starts. Requires 2 seconds of queuing before sending, to avoid a pointless flash for sub-second waits. Default: `false`. All three production adapters (Telegram, Slack, Mattermost) support in-place edit; the external adapter silently skips acks. |
| `channels.{telegram,slack,mattermost,external}` | object | Per-platform configuration; see below. |

## Per-channel configuration

### Telegram

```json
"telegram": {
  "enabled": true,
  "bots": [
    {
      "id": "default",
      "token": "<BOT_TOKEN>",
      "enabled": true,
      "access": "private",
      "pairingCodeHash": "<SHA256_HEX>",
      "groupsEnabled": false
    }
  ]
}
```

- `access: "private"` requires a peer to `/pair <code>` before any inbound is accepted. `pairingCodeHash` is the SHA-256 of the secret code (uppercased, non-alphanumeric stripped). Generate with:
  ```bash
  echo -n "MY-SECRET" | tr '[:lower:]' '[:upper:]' | tr -cd 'A-Z0-9' | shasum -a 256 | cut -d' ' -f1
  ```
- `access: "public"` accepts any DM.
- `groupsEnabled: true` accepts messages in group chats (requires the bot to be @mentioned).
- Peer ID format: numeric `chat_id` (never `@username`).

### Slack

```json
"slack": {
  "enabled": true,
  "apps": [
    {
      "id": "default",
      "botToken": "xoxb-...",
      "appToken": "xapp-...",
      "enabled": true,
      "groupsEnabled": true
    }
  ]
}
```

- Uses Socket Mode (no public webhook URL needed).
- Required Slack app scopes: `chat:write`, `app_mentions:read`, `im:history`, `files:read`, `files:write`, `users:read` (each message's author is looked up via `users.info`; without the scope the lookup fails, is logged, and is retried every 5 minutes per author). Add `users:read.email` to attribute each turn's telemetry to the author's email address; without it traces carry the Slack user id (see [Requester](telemetry.md#requester)). Event subscriptions: `app_mention`, `message.im`.
- Peer ID formats: `D<id>` (DM), `C<id>` (channel — auto-mutates to `C<id>|<thread_ts>` after first outbound), `C<id>|<thread_ts>` (existing thread), `U<id>` (user — auto-resolved to DM via `conversations.open` before persistence).

### Mattermost

```json
"mattermost": {
  "enabled": true,
  "instances": [
    {
      "id": "local",
      "serverUrl": "https://mm.example.com",
      "accessToken": "<BOT_ACCESS_TOKEN>",
      "enabled": true,
      "groupsEnabled": true
    }
  ]
}
```

- WebSocket + REST hand-rolled (no third-party Mattermost SDK; avoids ~272 transitive deps).
- Peer ID formats: `<channelId>` (DM/channel — auto-mutates to `<channelId>|<rootPostId>` on first outbound), `<channelId>|<rootPostId>` (existing thread), 26-char user-id (auto-resolved to DM via `channels/direct`).
- Reconnect: 1s → 30s exponential backoff, 20 attempts max. After exhaustion the adapter is marked `error` in `/router/health`.
- Interactive question UI: **single-select only**, and only when the pod knows where the orchestrator lives. Each choice renders as an `attachment.actions` button whose `integration.url` points at the orchestrator's `/router/mattermost/attachment-action`, with an `integration.context` of `{peerId, requestId, choice, token}`. Requires both:

  | Env var | Purpose |
  |---------|---------|
  | `OPENCODE_BRIDGE_REGISTRAR_URL` | Base URL the button's `integration.url` is derived from. |
  | `OPENCODE_BRIDGE_REGISTRAR_PASSWORD` | Shared orchestrator↔runner secret keying the action token (an HMAC-SHA256 over channel, identity, peerId, requestId and choice — Mattermost message actions carry no platform signature of their own). |

  If **either** is unset the send fails and that peer falls back to numbered text. This is deliberate: a button with no URL submits nowhere, and a token keyed with an empty secret is forgeable by anyone who can read the message, so neither is posted.

- Multi-select prompts always use the numbered-text / comma-separated-reply fallback — Mattermost attachment actions have no multi-select-with-apply semantics.

### External

A relay channel with **no chat platform of its own**. Outbound messages and questions are POSTed to the orchestrator's `/router/external/outbound`, which fans them out to a non-chat consumer (e.g. a service subscribing over SSE). `router_send` and the interactive-question flow behave exactly as they do for a chat channel — agent code doesn't know the difference.

```json
"external": {
  "enabled": true,
  "consumers": [
    {
      "id": "c3",
      "enabled": true,
      "relayUrl": "https://orchestrator.example.com",
      "relayCredential": "<SHARED_SECRET>"
    }
  ]
}
```

- `relayUrl` / `relayCredential` are optional in config: when omitted they fall back to `OPENCODE_BRIDGE_REGISTRAR_URL` / `OPENCODE_BRIDGE_REGISTRAR_PASSWORD`. When **neither** config nor env supplies them the adapter still registers but reports `disabled` in `/router/health` and fails every send, so a later reconfigure doesn't need a restart to have an adapter to attach to.
- Peer ID format: `<aid>:<flow_id>:<run_id>`, composed by the consumer. The agent must echo back a peer id it was given — never construct one.
- Inbound is never received directly. It arrives via the orchestrator's forward to `/router/inbound`, the same as any channel in mediated-inbound mode, so no single-listener lock is taken for this channel.
- Relay frames are authenticated with HTTP Basic (the credential as password) and `202 Accepted` is the only success status. Attachments relay as **metadata only** (`fileName`, `mimeType`, `size`) — never content.
- Groups / `@mention` gating don't apply; `POST /router/config/groups` rejects this channel explicitly.

## Outbound prose rendering

Agent replies are authored as GFM (GitHub-flavored Markdown) — headings, bold/italic, links, lists, tables, fenced code. Each adapter's `Send` renders that same `Outbound.Text` into whatever markup dialect its platform actually understands, with automatic degradation to plain text if rendering is rejected. The shared, stdlib-only chunking and conversion helpers live in `internal/bridge/markdown`.

- **Slack**: rendered as Block Kit `markdown` blocks (real GFM parsing, unlike the legacy mrkdwn `text` field, which cannot represent headings, tables, or fenced code with syntax highlighting). Text is chunked to Slack's 12,000-character cumulative budget across all blocks in one payload (`internal/bridge/markdown.BuildBlockChunks`, 3,000-char per-block target). The top-level `text` field is still sent alongside the blocks as the notification/accessibility fallback. If Slack rejects the blocks with an unambiguous block-capability error (`invalid_blocks`, `invalid_block`, `blocks_too_long`, `msg_blocks_too_long`, `invalid_block_id`), the adapter retries as plain text and sets a sticky per-identity latch so subsequent sends skip the blocks attempt entirely. A more ambiguous error (`invalid_arguments`, which Slack also returns for unrelated reasons) still retries as plain text for that one send but does **not** latch — the next send tries blocks again.
- **Telegram**: rendered as Telegram HTML (`internal/bridge/markdown.ToTelegramHTML`), the restricted tag set Telegram's `ParseMode: HTML` supports (`<b>`, `<i>`, `<a href>`, `<code>`/`<pre>`, `<blockquote>`, etc.) — chosen over MarkdownV2 because it requires escaping only three characters instead of ~18 reserved ones. Telegram also forbids `code`/`pre` entities nested inside any other entity (the sole exception being `<pre><code class="language-x">`), so a code span landing inside a heading, emphasis, link label, blockquote or table cell is emitted as plain escaped text rather than a nested `<code>` tag. Source markdown is chunked at 3,500 characters (`MarkdownChunkLimit`) before conversion — headroom under Telegram's 4,096-character *post-parse* cap, which HTML tags and `&amp;`-style escaping do not count against. If a chunk's HTML is rejected with a parse/entity error — or with `message is too long`, which the `---` → 10-em-dash expansion can still provoke — that one chunk is retried unformatted (no `ParseMode`) — there is no sticky latch, since a parse failure is specific to that chunk's content, not a platform capability.
- **Mattermost**: unchanged — `Post.Message` is sent as native GFM and Mattermost's own server-side parser already renders it correctly.

The `RichRenderer` tool-card paths (`internal/bridge/slack/render.go`, `internal/bridge/telegram/render.go`) that hand-author Block Kit / legacy Markdown for tool calls, lists, tables, and status previews are separate code paths, untouched by the above — they compose their own markup directly rather than converting agent-authored GFM.

### Intermediate assistant text

A run usually has several assistant messages: each one that ends in `tool_use` is followed by the tool results and the next model call. For runs the bridge dispatches itself (an inbound chat message), each such message's text is relayed too, not only the final reply. It is posted as `⌛ <tool names>` (the message's tool calls in call order) on the first line, then the text, and is sent before that message's 🔧 call card at `full` verbosity. Messages without text post nothing. The final reply has no header, and no message is posted twice. Before a `question` widget goes out, the router makes sure the text that introduces it has been posted. This works at every `toolUpdateVerbosity` and with tool updates off. Subagent text and self-started turns are not relayed.

Only the bridge's own run is relayed. If another run held the session while the inbound waited (`ErrSessionBusy`), such as a task auto-resume or an API run, that run's text is not posted into the thread. Interactive flow steps bound to chat are not covered yet: the flow engine runs those agents itself, so their intermediate text is not relayed. That is a follow-up.

Timing: each intermediate post is bounded to 10 s (60 s with `FILE:` attachments). The question widget and the final reply each wait at most 5 s for text still being posted, then go out anyway. A post that is still running at that point is not cancelled, so the text arrives once, possibly below the widget.

## HTTP API (`/router/*`)

All endpoints live on the existing opencode API port. Bare paths (`/send`, `/identities/*`, `/config/groups`) return 404 — everything is under `/router/*`.

### `POST /router/send`

Deliver a single message to a peer:

```bash
curl -X POST http://127.0.0.1:3456/router/send \
  -H 'Content-Type: application/json' \
  -d '{
    "channel": "slack",
    "identity": "default",
    "peerId": "D012345",
    "text": "hello",
    "files": []
  }'
```

`autoBind: true` is rejected with 400 + a pointer at `/router/bind` (router-initiated conversations must bind explicitly first). File paths must resolve under `<dataDir>/bridge/media/`.

### `POST /router/bind` + `POST /router/unbind`

Associate one or more peers with an opencode session. The same session can be bound to **multiple peers** across platforms (multi-reviewer fan-out):

```bash
curl -X POST http://127.0.0.1:3456/router/bind \
  -H 'Content-Type: application/json' \
  -d '{
    "sessionId": "<SESSION_ID>",
    "peers": [
      {"channel":"telegram","identity":"default","peerId":"<CHAT_ID>"},
      {"channel":"slack",   "identity":"default","peerId":"U123ABC","mention":"@alice"}
    ]
  }'
```

- Slack `U<id>` / Mattermost user-IDs are auto-resolved to DM channels (`conversations.open` / `channels/direct`) **before** the binding is persisted.
- Channel-only Slack peers (`C<id>`) get the binding **mutated** to channel+thread (`C<id>|<ts>`) after the first outbound creates a thread; subsequent agent output replies in-thread.
- Optional `mention` per peer — platform-native ping handle prepended to the first outbound only, then cleared.
- If the session doesn't exist, `Bind` auto-creates it (so router-initiated callers can bind a fresh sessionID without pre-creating).

`POST /router/unbind` with empty `peers` drops every binding for the session and tears down the dispatcher. With non-empty `peers`, removes only those rows — dispatcher stays alive if any binding remains.

### `POST /router/inbound`

The orchestrator forwards chat replies here in mediated-inbound mode. `202 Accepted` means the inbound was enqueued. `429` with `Retry-After` means the queue is full, so retry.

Under `opencode serve --pool-mode`, the endpoint accepts an inbound only when a live interactive flow step in this process owns the peer's session. Otherwise it returns `409 {"sessionNotOwned": true}` and drops the message. This covers a container that restarted mid-step while its binding survived. Retrying cannot succeed, so orchestrators should not retry a `409`. Pool pods never pass an inbound to the workspace default agent. Without `--pool-mode`, behavior is unchanged.

### Identity CRUD — `/router/identities/{channel}[/{id}]`

```bash
# List (tokens redacted; only `hasToken: bool` is exposed)
curl http://127.0.0.1:3456/router/identities/slack

# Upsert — mutates .opencode.json atomically + launches the adapter
curl -X POST http://127.0.0.1:3456/router/identities/slack \
  -H 'Content-Type: application/json' \
  -d '{"id":"default","botToken":"xoxb-...","appToken":"xapp-...","enabled":true}'

# Delete — deregisters the adapter + cascades all bindings for this identity
curl -X DELETE http://127.0.0.1:3456/router/identities/slack/default
```

### Per-identity `groupsEnabled` — `GET|POST /router/config/groups`

```bash
curl -X POST http://127.0.0.1:3456/router/config/groups \
  -H 'Content-Type: application/json' \
  -d '{"channel":"mattermost","identityId":"local","enabled":true}'
```

Per-identity scope — there is no global `groupsEnabled`.

### `GET /router/health`

Returns `{status, adapters[<channel:identity>]}` with per-adapter `status`/`lastError`/`lastInboundAt`/`lastFailureAt`/`boundSessions`. Overall status is worst of (`error` > `degraded` > `disabled` > `running`). Also embedded as `bridge` in the global `/health` endpoint.

## Chat commands

Once a peer is bound (manually or via the first inbound), the following commands work in any of the chat surfaces:

| Command | Description |
|---|---|
| `/sessions` | List recent sessions (★ marks current binding); shows tokens + cost + relative age. |
| `/session` | Show current session details (title, tokens, cost, message count). |
| `/session <id-prefix>` | Switch this peer's binding to another session by ID prefix. Refuses while a run is in flight on either side; multi-reviewer rows for other peers are unaffected. |
| `/agent` | List primary agents (active marked). |
| `/agent <id-prefix>` | Switch active agent (affects every chat session on this process; prompt cache invalidates). |
| `/model` | List models grouped by provider (active marked). |
| `/model <id>` | Switch model on the active agent. |
| `/reset` | Forget this peer's binding — next message starts a fresh session. |
| `/new` | Alias of `/reset`. |
| `/abort` | Cancel an in-flight run on the current session (releases the busy lock — use when an MCP tool hangs). |
| `/pair <code>` | Pair with a private Telegram bot. |
| `/skip` | Dismiss a pending agent question. |
| `/verbosity` | Show the live tool-update level: `compact` (one progress card per run) or `full` (one card per tool call). |
| `/verbosity compact\|full` | Switch it for this process (not persisted; restart restores `router.toolUpdateVerbosity`). `verbose` and `debug` mean `full`. |
| `/heartbeat …` | Show or configure this chat's heartbeat (daemon mode only). See [Heartbeat](#heartbeat). |
| `/help` | List commands. |
| `/dir` | Unsupported — one opencode process is pinned to one workspace (returns an explanatory message). |

Any non-command message is forwarded as a prompt.

## Heartbeat

In daemon mode (`opencode serve` without `--flow` or `--pool-mode`) a chat can give its agent a heartbeat: on a schedule the bridge wakes the bound session with a heartbeat turn, the agent works through its agenda file, and only what is new reaches the chat.

**It is off until someone in the chat turns it on.** When a daemon starts and a chat has never chosen, the bridge posts a short setup reminder there, at most once every seven days. Turning it on or off ends the reminders.

**Describe it in your own words.** Anything after `/heartbeat` that is not the exact form below goes to the agent, which sets it up with the `heartbeat` tool and writes what to check into the agenda file:

```
/heartbeat every half hour on weekdays, 7 to 23 Oslo time, and keep an eye on my open merge requests
/heartbeat once a day at 9 my time, summarise what changed in my tickets
/heartbeat skip weekends from now on
```

The agent converts times to UTC and replies with the resulting schedule. This needs the `heartbeat` tool, which is opt-in per agent (`"heartbeat": true` in the agent's `tools`). Without it, only the exact form works.

**The exact form** is handled by the bridge itself, without a model call:

| Command | Effect |
|---|---|
| `/heartbeat` or `/heartbeat status` | State, schedule, model, agenda file, last and next beat. |
| `/heartbeat on` / `off` | Start or stop the heartbeat. |
| `/heartbeat now` | Run one beat now, without moving the schedule. |
| `/heartbeat every <duration>` | Interval, 10m to 24h. Default `1h`. |
| `/heartbeat hours <HH-HH>` / `hours all` | Active hours in UTC, start inclusive, end exclusive; `22-06` wraps midnight. Default all day. |
| `/heartbeat days weekdays` / `days all` | Skip Saturday and Sunday (UTC). Default every day. |
| `/heartbeat model <id>` / `model default` | Run beats on another model. A different model cannot reuse the session's prompt cache. |
| `/heartbeat file <path>` / `file default` | Agenda file, relative to the working directory. Default `HEARTBEAT.md`. |

Settings combine in one command: `/heartbeat on every 30m hours 05-21 days weekdays`. They are stored in the database, so they survive restarts, redeploys and `/reset`.

How a beat runs:

- **In the bound session**, through the same dispatcher as a human message, so it never overlaps another run and a reply to its report lands in the same conversation.
- **On a UTC grid that starts at the active hours.** Each day's beats start at the beginning of the active hours (00:00 UTC without them) and repeat every interval inside them: `every 2h hours 07:30-21` beats at 07:30, 09:30, …; `every 24h hours 07-08` is a daily 07:00 beat.
- **Busy sessions defer.** A beat that comes due while the agent is working waits until the session is idle. Beats missed while the process was down collapse into one catch-up beat.
- **An empty agenda skips the beat.** If the agenda file is missing or holds only headings and empty list items, no model call is made.
- **Quiet.** A heartbeat turn posts no queued-ack, progress card or tool-call cards. If the agent replies `HEARTBEAT_OK`, nothing is posted. Otherwise the reply is posted under a `💓 Heartbeat HH:MMZ` header, and a failed beat posts one line with the reason.

The agenda file is the agent's standing instructions for heartbeats: what to check and how to report it. The agent can edit it when its human gives it a new standing instruction.

Each beat is a turn in the main session, so it adds to the session's context. The empty-agenda skip, the silent reply and the active hours are what keep that cost down.

## In-process agent tool: `router_send`

When at least one channel has an enabled identity AND the agent is in `mode: "agent"` (not `subagent`), the agent's tool list automatically includes `router_send` — a tool for sending messages to bound peers mid-turn.

```json
{
  "channel":  "slack",
  "identity": "default",
  "peerId":   "D012345",
  "text":     "Done — see attached.",
  "files":    ["./output/report.pdf"]
}
```

The tool's description is **built dynamically at registration time** from the live `cfg.Router` snapshot — the agent sees enumerated channels, peer-ID formats, identities, and currently-bound peers without needing external documentation. Each bound-peer entry is annotated with the owning session id and a coarse last-active age (`— bound by session <id>, last active 3d ago`), or marked `orphaned (owning session gone)` when the session row was garbage-collected — in shared-DB deployments the snapshot spans other runs' bindings, and the annotation lets the agent recognise a foreign or stale binding before reusing it (GENAI-186). Implementation calls `bridge.Service.Send(...)` directly in-process (no HTTP loopback).

Response shape: `{"delivered": bool, "error"?: string, "resolvedPeerId"?: string}`. Multi-peer fan-out isn't exposed — the agent makes parallel `router_send` calls if it needs to reach multiple peers (allowed via `AllowParallelism: true`).

## Interactive question UI

When `questionMode: "interactive"` is set and the agent calls the `question` tool, the bridge renders choices using platform-native UI:

- **Slack** — `chat.postMessage` with an actions block (one button per option).
- **Telegram** — `sendMessage` with `reply_markup.inline_keyboard` (one row per option).
- **Mattermost** — `attachment.actions` buttons for single-select, when `OPENCODE_BRIDGE_REGISTRAR_URL` and `OPENCODE_BRIDGE_REGISTRAR_PASSWORD` are both set; numbered-text fallback otherwise, and always for multi-select. See the per-channel notes above.
- **External** — a `question` relay frame carrying `requestId`, the choices, and the `multiple` / `custom` flags; the consumer renders it however it likes and answers against `requestId`.

Button click callbacks are normalized into the same `bridge.Inbound` shape as text replies, so the agent's question-reply parsing works identically across platforms. Fallback to numbered text is per-peer — if Slack's `chat.postMessage` errors, only that peer falls back; other peers in a multi-reviewer session still get buttons.

## Subagent visibility (the `task` tool)

When the agent calls the `task` tool to spawn a subagent, the subagent runs in its own session whose `root_session_id` points at the parent. The bridge's dispatcher forwards part events from **both** the parent session and its descendants, so reviewer chat shows tool activity inside the subagent in real time:

```
🔧 task#dkhDCd · Create Jira tickets for c2 unification subagent_type=piano-manager
🔧 atlassian_jira_search#qQjm1W · {"jql":"project = GENAI ..."}
🔧 atlassian_jira_get_all_projects#Sh34dJ · {}
✓ atlassian_jira_search#qQjm1W · (...result preview...)
✓ atlassian_jira_get_all_projects#Sh34dJ · (...result preview...)
✓ task#dkhDCd · (...subagent's final output...)
```

Without this, a long-running subagent would look like 15 minutes of silence — the user might think the bridge is broken when it's actually waiting on an MCP tool.

Permission requests from subagent sessions are also handled by the bridge: `PermissionRouter` matches sessions by either direct `bridge_sessions` row OR `root_session_id` matching a bound row. With `permissionMode: "allow"`, MCP calls inside subagents auto-grant without an interactive prompt (which would hang in headless serve mode).

Subagent **costs** are rolled into the parent via `agent-tool.go`'s `parent.Cost += subagent.Cost` once the task completes (including on cancel/error paths). `/sessions` and `/session` display the aggregated number.

## Multi-reviewer fan-out

A single opencode session can be bound to multiple chat peers across platforms. The bridge:

- **Fans agent output to every bound peer** — text + attachments delivered in parallel via a bounded worker pool (cap 4).
- **Attributes inbound messages** — when multiple peers are bound, each inbound is prepended with `[<peerId> via <channel>]: ` so the agent knows who spoke. The bridge automatically strips this envelope from echoed outbound to avoid feedback loops.
- **Isolates per-peer failures** — if Slack delivery fails for one peer, Mattermost / Telegram delivery still proceeds; failures are reported per-peer in `/router/health`.

## Flow API integration

For external orchestrators (k8s Jobs, automation systems), the bridge integrates with the [Flow API](flows.md):

- Flow steps marked `interactive: true` auto-bind via `bridge.Service.Bind` before `agent.Run` and auto-unbind after `struct_output`.
- `interaction.target` accepts a single PeerRef or array of PeerRefs (resolved from `${args.NAME}`).
- SSE events on `/event`: `flow.step.{started,completed,failed}`, `flow.waiting_for_input` (carries the resolved target peers), `flow.completed`, `flow.failed`.
- `opencode serve --flow <id> --flow-args /tmp/args.json --flow-exit` for the k8s Job entrypoint pattern.

See [docs/flows.md](flows.md) for the YAML schema and orchestrator integration details.

## Single-writer enforcement

Two opencode processes against the same database can't simultaneously own the same chat identity (otherwise both would consume the same inbound stream). The bridge enforces this:

- **SQLite local-dev** — file lock on `<dataDir>/bridge.lock` via `flock` (POSIX) / `LockFileEx` (Windows).
- **MySQL** — `GET_LOCK('opencode_bridge:' + SHA1(project_id + channel + identity_id))` on a dedicated `*sql.Conn` (never returned to the pool until release).

A second process attempting to start an already-locked identity sees:

```
WARN bridge: slack adapter launch failed identity=default err="bridge: lock slack:default: identity is locked by another opencode process"
```

Its other identities continue running normally — the lock is per-identity, not per-process.

## Storage

Bridge tables on both providers (SQLite + MySQL), keyed by `(project_id, channel, identity_id, peer_id)`:

- `bridge_sessions` — many-to-one peer→session mapping with `session_id` FK to `sessions(id) ON DELETE SET NULL`, plus `mention_handle` (per-peer ping handle for first-message attribution) and `mention_consumed_at` (timestamp set after first delivery; reset on re-bind).
- `bridge_allowlist` — per-identity peer allowlist (Telegram private-mode pairing).
- `bridge_heartbeats` — per-binding heartbeat state, schedule settings, next/last beat and the last setup reminder (`20261001130000_add_bridge_heartbeats.sql`).

Migrations live in `internal/db/migrations/{sqlite,mysql}/20260609120000_add_bridge_tables.sql`. MySQL column widths are sized so the compound PK fits within InnoDB's 3072-byte key-length cap under utf8mb4.

## Audit logging

Every inbound message produces one structured log line at the orchestrator's funnel point, before any branching (slash-command interception, question-reply handling, agent.Run):

```
level=INFO msg="bridge: inbound" channel=telegram identity=default peerId=344281281 \
  authorId=344281281 command=pair attachments=0 truncated=false text="/pair MY-SECRET"
```

Grep for `bridge: inbound` to reconstruct who sent what when, across all platforms.

## Cutover from the legacy `opencode-router`

If you were running the legacy Node `opencode-router` process, the migration is mechanical: stop the Node process, copy tokens from `~/.openwork/opencode-router/opencode-router.json` into the `router.channels` section of `.opencode.json`, restart `opencode serve`, re-issue Telegram pairing codes (no allowlist migration ships). Full step-by-step: [interoperability/openwork/DEPLOY.md → Cutover from the TS bridge](../interoperability/openwork/DEPLOY.md#cutover-from-the-ts-bridge).

The legacy CLI subcommands (`opencode-router telegram add`, `slack add`, etc.) are not ported — mutations go through the `/router/identities/*` HTTP CRUD endpoints or direct edits of `.opencode.json`.

## Behavior changes vs. the TS router

| Was (TS router) | Is now (in-process bridge) |
|---|---|
| `opencode-router send …` CLI | `POST /router/send` |
| Bare `/send`, `/identities/*`, `/config/groups` HTTP paths | Under `/router/*`; bare paths return 404 |
| `autoBind: true` on `/send` | Rejected; explicit `POST /router/bind` required |
| `/dir` chat command | Unsupported — one process is pinned to one workspace |
| Per-peer `directory` field | Removed |
| `~/.openwork/opencode-router/opencode-router.db` | `bridge_sessions` / `bridge_allowlist` tables in opencode's existing DB |
| `~/.openwork/opencode-router/opencode-router.json` | `router` section of `.opencode.json` |
| `OPENCODE_ROUTER_HEALTH_PORT` | Bridge runs on the opencode API port |
| `OPENCODE_ENABLE_QUESTION_TOOL=1` env var | `router.questionMode = "interactive"` in `.opencode.json` |

## Context

The openwork-router bridge has lived as a separate TypeScript process for two years. It does its job, but every cross-cutting change (the last three: message-part SSE, agent/model selection endpoints, FILE: reply convention) requires paired commits across two repos and serializes review. The bridge subscribes to opencode's SSE event stream for tool transitions — data that's a single Go channel away on the `pubsub.Broker` inside opencode itself. The bridge owns a second SQLite database (`~/.openwork/opencode-router/opencode-router.db`) with peer→session pointers and bindings; opencode owns sessions/messages/permissions. Two backup stories, two migration stories, two failure-visibility stories.

This change ports the bridge in-process under `internal/bridge/`. The Go opencode codebase already has every primitive the bridge needs: dual-provider sqlc storage (SQLite local-dev, MySQL prod), a part-event broker at `internal/message/message.go`, an agent runner at `internal/llm/agent/agent.go`, an HTTP mux at `internal/api/server.go`, `db.GetProjectID` for multi-tenant scoping, `config.WorkingDirectory()` for workspace pinning. The port is mostly assembly, not invention.

The exhaustive third-pass-reviewed source is `spec/20260606T032323-go-bridge-port.md`. This design document covers only the load-bearing technical decisions; the source spec carries the full rationale, alternatives, and edge-case analysis.

## Goals / Non-Goals

**Goals:**

- Single binary, single language, single database, single config file for the entire chat-bridge surface.
- Zero loopback HTTP/SSE between opencode and the bridge — direct Go calls into `app.ActiveAgent().Run`, `messages.SubscribeParts`, `permission.Service`, `question.Service`, and (new) `bridge.Service.Send` from the `router_send` tool.
- Incremental flow progress visible to the c2 agent orchestrator via `/flow/status` and `flow.step.*` SSE — kill the "30-minute black box" failure mode without re-implementing the full 2026-05-18 spec.
- Interactive flow steps (`interactive: true` in flow YAML) drive router-initiated conversations: opencode → bridge → Slack/Telegram/Mattermost as the **first** message, with the right reviewer(s) pre-bound to the step's session.
- Multi-reviewer fan-out for interactive flows: agent output is broadcast to every bound peer; inbound from any reviewer is attributed (`[<who> via <channel>]: `) so the agent stays grounded.
- An in-process agent tool (`router_send`) the agent can call mid-run to push messages to other channels — only registered when at least one channel is configured, with a tool description that enumerates available channels/identities so the agent knows what to ask for.
- Functional parity with the TS bridge against its ~3.3k LOC of tests (Mattermost 1377, Slack 273, Telegram 204, health-send 446, question 360, bridge-e2e 170, bridge-multiworkspace 182, misc ~340). Port success on each adapter is binary: same scenarios pass, or they don't.
- Single-writer enforcement per identity that works for both SQLite (file lock) and MySQL (`GET_LOCK` on a dedicated connection).
- `.opencode.json` writeback that is actually atomic (temp-file + fsync + rename + parent-dir fsync), preserves operator-applied file modes, and forces `0o600` the moment tokens enter the config.
- No `node_modules`. No second config file. No second process. No second database.

**Non-Goals:**

- Data migration from the TS bridge's `opencode-router.db`. Operators start fresh with empty tables — fresh chat sessions, empty allowlists, re-pasted tokens.
- Sidecar deployment mode. Embedded only.
- Hot-reload of `.opencode.json` for external edits (vim save → live restart). Opencode does not watch its config today (no `viper.WatchConfig`/SIGHUP/fsnotify on the config path); adding a watcher is out of scope. In-process HTTP CRUD does live updates via the in-memory-`cfg` contract.
- True active-active HA. Per-identity single-writer is enforced at the database layer; multi-process operators must split identities between processes. Solving the chat-platform-side single-consumer constraint (Telegram's `getUpdates` offset, Mattermost's WebSocket fan-out) is out of scope.
- Porting the 763-LOC CLI subcommand surface (`telegram add`, `slack add`, etc.). HTTP identity-CRUD is the canonical mutation surface.
- The `POST /flow/input` endpoint from the 2026-05-18 spec. Reviewer replies arrive through chat platforms via the normal inbound bridge path — no second HTTP input mechanism.
- The `GET /flow/output` endpoint. Final step output is read via existing `GET /session/{id}/messages` (the `struct_output` result is already in the session message stream).
- The bridge running outside `serve` mode (no support for TUI-only or `acp`-mode bridging — those don't currently instantiate the HTTP mux that `/router/*` and `/flow/*` need).
- A peer allowlist for the `router_send` agent tool in v1 — trust boundary is at the orchestrator. Allowlist field stays available in the config schema as a forward-compat hook but is not enforced unless explicitly populated.
- Untrusted-agent or multi-tenant peer scoping. c2-agent's k8s pattern is single-tenant per pod; this change preserves that posture.

## Decisions

### Storage: extend opencode's existing dual-provider sqlc machinery, don't introduce a third store

Bridge state is stored in opencode's existing SQLite/MySQL database via two new tables, `bridge_sessions` and `bridge_allowlist`. Every PK includes `project_id` (the same hash `db.GetProjectID(workingDir)` opencode uses for session rows), so a shared MySQL hosts multiple opencode workspaces without collision. The FK on `bridge_sessions.session_id → sessions(id)` uses `ON DELETE SET NULL` — when opencode garbage-collects a session, the bridge pointer becomes NULL and the next inbound message creates a fresh opencode session.

**Why this over a separate bridge SQLite**: one migration story, one backup story, one provider abstraction, one failure-visibility story. The TS bridge's `bindings` and `settings` tables are not ported — directory is fixed via `config.WorkingDirectory()`, settings live in `.opencode.json`'s `router` section.

**Why `project_id` in every PK over schema-per-workspace**: the existing opencode `sessions` table already follows this pattern (`20260131204842_add_project_id.sql`); consistency wins. Same `db.GetProjectID` source.

**Required index**: `idx_bridge_sessions_session_id (session_id)`. Without it, the FK `ON DELETE SET NULL` triggers a full-scan of `bridge_sessions` on every opencode session deletion.

### Run / part-event interaction: subscribe-before-Run, demux into per-session queues, drop-oldest on overflow

`agent.Run(ctx, sessionID, content, maxTurnsOverride, attachments...)` returns `(<-chan AgentEvent, error)` where the channel delivers **one** terminal `AgentEvent` and closes — it is not a stream of parts. Per-tool progress (`pending|running|completed` transitions) arrives via a separate subscription to `messages.SubscribeParts(ctx)`.

The orchestrator MUST subscribe parts before invoking `Run`. `PublishPart` (`internal/message/message.go:76-89`) has a zero-subscribers fast path: if `s.parts.GetSubscriberCount() == 0` the event is dropped at the publisher. Events emitted before any subscriber attaches are **not buffered** for late subscribers. Reordering subscribe-after-Run silently loses the agent's first `ToolCall pending` parts with no error path.

The broker is process-wide — a single subscriber channel carries part events from every session (TUI rendering, every active agent, the bridge). The bridge MUST NOT do outbound IO from the receive loop. Required pattern: the goroutine receiving from the broker channel does a non-blocking `select { case q <- ev: default: dropAndLog(ev) }` into a per-`sessionID` buffered queue of size **64** (covers a worst-case turn of ~12 parallel tool calls × 3 lifecycle transitions plus text-stream headroom).

**Overflow semantics**: drop oldest. `completed` strictly supersedes `running` strictly supersedes `pending`; keeping a stale event would mislead the chat surface. On drop, emit one warn-level log per session per minute: `bridge: part-queue overflow session=<id> dropped=<n>`. The broker-side receive loop NEVER blocks — this is what prevents a wedged Mattermost connection from back-pressuring TUI rendering.

### Per-`sessionId` dispatch goroutine: single call site for `agent.Run`

Multi-reviewer support means N peers can have inbound messages arrive simultaneously for the **same** bound `sessionId`. Calling `agent.Run` from multiple goroutines for one session returns `ErrSessionBusy` for all but one. A naive retry-on-busy path adds chat-surface "queued" UX, cross-goroutine signaling to detect run completion, and a retry loop that itself races.

The cleaner answer is to make `agent.Run` invocation **single-callsite per `sessionId`**: one dispatcher goroutine owns the session, drains an inbound queue, and calls `agent.Run` serially. The same goroutine handles parts demultiplexing (the parts queue described above), so inbound dispatch and outbound part-event delivery are ordered by construction.

```go
// internal/bridge/session_dispatch.go (sketch)

type sessionDispatch struct {
    sessionID string
    inbound   chan inboundMsg   // buffered 16, never drop
    parts     chan partEvent    // buffered 64, drop-oldest
}

// One goroutine per bound session:
for {
    select {
    case <-ctx.Done():
        return
    case msg := <-d.inbound:
        // Serial agent.Run — no ErrSessionBusy possible.
        runCh, _ := agent.Run(ctx, d.sessionID, msg.Content, ...)
        for ev := range runCh {
            // Run channel delivers one terminal event then closes.
            // Per-tool transitions arrive via d.parts (below).
            handleTerminal(ev)
        }
    case ev := <-d.parts:
        fanOutPartToAllBoundPeers(d.sessionID, ev)
    }
}
```

Inbound queue is **buffered 16, never drop**. Reviewer messages must not be lost — if the buffer fills, the per-peer adapter goroutine blocks pushing, which back-pressures only that adapter (acceptable; the platform's own buffering absorbs short stalls). Parts queue is **buffered 64, drop-oldest** (per the prior section).

**Goroutine count**: +1 goroutine per actively-bound session. In c2-agent's k8s pattern (one pod ≈ one interactive flow step ≈ one bound session), this is +1 goroutine total per pod. Negligible.

**Lifecycle**: a dispatcher is created on first `Bind(sessionId, ...)` and torn down on `Unbind(sessionId)` (or when the FK ON DELETE SET NULL fires because the opencode session was garbage-collected).

### Multi-reviewer fan-out and reviewer attribution

`bridge_sessions` is **many-to-one** by design — one `session_id` can appear in N rows, one row per `(channel, identity, peer_id)`. The existing PK `(project_id, channel, identity_id, peer_id)` already supports this; the existing index `idx_bridge_sessions_session_id` doubles as the session→peers lookup index. One new column: `mention_handle TEXT NULL` for the per-peer ping handle on first message.

**Outbound fan-out** (agent → reviewers):

```
agent.Run produces terminal event for session S
       │
       ▼
SELECT channel, identity_id, peer_id, mention_handle
FROM bridge_sessions
WHERE session_id = ? AND project_id = ?
       │
       ▼
parallel goroutine pool (cap 4): for each row → adapter.Send(text, mention?)
```

Failures on one peer (DM closed, user blocked the bot, network blip) MUST NOT block delivery to others. Per-peer errors are logged and surfaced in `/router/health` per-identity status as `lastError`/`lastFailureAt`. The agent is **not** told about per-peer failures — partial fan-out failure is a delivery concern, not a conversation concern.

**Inbound attribution** (any reviewer → agent):

```
Bob replies in his Slack DM
       │
       ▼
SELECT session_id, mention_handle
FROM bridge_sessions
WHERE (project_id, channel, identity_id, peer_id) = (?, 'slack', 'default', 'DBOB')
       │
       ▼
push to dispatcher.inbound, but PREPEND attribution:

  Content = "[" + mention_handle (or peerId) + " via " + channel + "]: " + rawText
         = "[<@U07DEF> via slack]: Looks good to me, ship it"
```

The agent sees attributed text, so it knows which reviewer spoke when output gets fanned to all. The bridge's prompt-builder strips the `[…]:` envelope from anything echoed back in outbound (preventing the attribution from re-appearing as the agent's own quoted text).

**Same-thread reviewers collapse to one row**: if two reviewers map to the same destination (both reviewing in Slack thread `C0DEF456|<ts>`), there's only one binding row — the platform itself delivers the thread message to both. Inbound attribution uses the platform-reported author (Slack `user_id`) to distinguish them.

### Router-initiated conversations: explicit binding + flow-engine auto-bind

The TS bridge's implicit "first inbound message creates the binding" pattern doesn't work for c2-agent's interactive flow: the bridge needs to know `peer → session` *before* the agent's first turn produces output. Two binding paths:

**Auto-bind from flow args (the default for c2-agent's k8s pattern)**:

```yaml
# flow.yaml
steps:
  - id: spec-review
    interactive: true
    interaction:
      target: ${args.reviewer}        # single PeerRef OR array
      mention: ${args.reviewerHandle} # optional, used on first outbound

# --flow-args /workspace/args.json
{
  "reviewer": [
    { "channel": "slack",    "identity": "default", "peerId": "D012345" },
    { "channel": "telegram", "identity": "default", "peerId": "344281281" }
  ],
  "reviewerHandle": "<@U01ABC>"
}
```

On step entry, the flow engine calls `bridge.Service.Bind(sessionId, peers)` synchronously *before* invoking the step's agent. Agent first-turn output flows to all bound peers naturally via the fan-out path. No race, no replay logic.

**Manual `/router/bind` (for orchestrators that bind reactively)**:

```
POST /router/bind
{
  "sessionId": "sess-uuid",
  "peers": [
    { "channel": "slack", "identity": "default", "peerId": "D012345", "mention": "<@U01ABC>" }
  ]
}
```

External orchestrators that listen to `flow.waiting_for_input` SSE and pick the reviewer at runtime use this path. The endpoint is also useful for re-binding mid-flow (e.g., escalation).

**Unbind**: `POST /router/unbind {sessionId}` or `POST /router/unbind {sessionId, peers: [...]}` (partial). The flow engine auto-unbinds on step completion (after `struct_output`) so subsequent messages from the reviewer go to a fresh session, not to the completed step.

### Peer reference shape and platform-specific resolution

```go
// internal/bridge/peer.go

type PeerRef struct {
    Channel    string `json:"channel"`    // "telegram" | "slack" | "mattermost"
    Identity   string `json:"identity"`   // e.g., "default"
    PeerID     string `json:"peerId"`     // platform-specific format (table below)
    Mention    string `json:"mention,omitempty"` // optional ping handle prepended to first outbound
}
```

Platform peer-ID grammar and resolution:

| Platform | Form | Resolution |
|---|---|---|
| Telegram | numeric `chat_id` | used as-is for both DMs and groups |
| Slack | `D<dm-channel>` | DM channel, used as-is |
| Slack | `U<user-id>` | bridge calls `conversations.open` to obtain a DM channel, substitutes before binding |
| Slack | `C<channel>` (no `\|`) | bridge posts first message to channel; captures returned `ts`; **mutates** the binding's `peer_id` to `C<channel>\|<ts>` so subsequent fan-out replies in-thread |
| Slack | `C<channel>\|<ts>` | thread reply, used as-is |
| Mattermost | `<channelId>` (DM type) | used as-is |
| Mattermost | `<channelId>` (channel type, no thread) | first post becomes thread root; binding mutates to `<channelId>\|<rootPostId>` |
| Mattermost | `<channelId>\|<rootPostId>` | thread reply, used as-is |

Binding-mutation-on-first-outbound is a non-trivial requirement: the bridge MUST update the `bridge_sessions.peer_id` column *after* posting the first message, before the next fan-out cycle for that session. This is done in the per-peer adapter delivery goroutine and committed transactionally — if the mutation fails, the binding is left at the pre-mutation peer ID and the next outbound creates yet another thread (acceptable degradation).

### Agent tool: `router_send`, in-process and configured-channels-aware

A new tool in `internal/llm/agent/tools/router_send/` lets the agent send messages to configured router channels mid-run. Two design constraints drive the shape:

1. **Conditional registration.** The tool MUST be registered only if (a) the calling agent's mode is `"agent"` (not `"subagent"` — subagents shouldn't decide who to message), and (b) `cfg.Router != nil` with at least one enabled identity. Otherwise the tool is absent from the agent's tool list — the agent cannot accidentally try to call something that won't work.

2. **Dynamic description.** The tool's description is built at registration time from `cfg.Router` snapshot — enumerates configured channels, their identities, and (optionally) currently-bound peers from `bridge_sessions`. This gives the agent runtime visibility into what's actually available, so it can pick the right channel/identity for "send a notification to Slack when the build finishes" without external context.

3. **In-process call path.** Tool invocation calls `bridge.Service.Send(...)` directly — no HTTP loopback. Symmetric with how the agent already talks to `permission.Service` and `question.Service` in-process. Tests mock the `Adapter` interface, not an HTTP server.

Tool input schema (JSON):

```json
{
  "channel":  "slack",
  "identity": "default",
  "peerId":   "C0DEF456",
  "text":     "Build green ✓",
  "mention":  "<@U01ABC>",      // optional, prefixed to text
  "files":    ["/tmp/report.pdf"]  // optional; subject to per-platform size limits
}
```

Output: `{ "delivered": true, "errors": [] }` or per-peer error breakdown.

**Peer-selection contract**: free-form `peerId` from the agent. c2-agent runs in single-tenant k8s pods; the trust boundary sits at the orchestrator, not at tool dispatch. The tool MUST NOT enforce an allowlist by default. An optional `cfg.Router.AgentPeerAllowlist` field can be added later if untrusted-agent scenarios materialize.

### Flow API: narrow scope, narrow scope only

Per the 2026-05-18 spec's "narrow scope" path: ship the **read-side** flow API plus the SSE events, plus the `interactive:` step type. Skip the spec's `POST /flow/input` and `GET /flow/output`:

- **Skipped `/flow/input`**: reviewer replies come in through chat platforms via the normal inbound bridge path — the bridge is already the input mechanism, no second HTTP path needed.
- **Skipped `/flow/output`**: orchestrator reads the final session's messages via existing `GET /session/{id}/messages` — `struct_output` results are already part of the session message stream.

Endpoints:

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/flow` | List available flows (auto-discovered from `.opencode/flows/*.yaml`) |
| `POST` | `/flow` | Start a flow run (when not using `--flow` flag) |
| `GET` | `/flow/status` | Current state — running step, completed steps, pending input |
| `DELETE` | `/flow` | Abort current run; cancels the current step's agent |

SSE event types added to `internal/api/handler_event.go`:

```
flow.step.started        { runID, stepID, sessionID }
flow.step.completed      { runID, stepID, sessionID, output }
flow.step.failed         { runID, stepID, error }
flow.waiting_for_input   { runID, stepID, sessionID, target }
flow.completed           { runID, output }      ← summary; reads existing session
flow.failed              { runID, error }
```

CLI flags on `opencode serve` (matching the prior spec):

- `--flow <id>` — auto-start this flow when the server boots
- `--flow-args <path>` — JSON file with flow arguments (e.g., reviewers, ticket IDs)
- `--flow-exit` — exit the process after the flow completes (default when `--flow` is set; pod's `--flow-exit` is what makes k8s job lifecycle natural)

Flow YAML schema additions:

```yaml
steps:
  - id: spec-review
    interactive: true            # NEW
    interaction:                 # NEW
      target: ${args.reviewer}   # single PeerRef or array
      mention: ${args.handle}    # optional first-message ping
    prompt: |
      ...
    output:
      schema: { ... }
```

When the flow engine enters a step with `interactive: true`:

1. Resolve `interaction.target` from flow-args.
2. Call `bridge.Service.Bind(sessionId, target)` synchronously.
3. Invoke the step's agent normally (first-turn output fans out via bindings).
4. Emit `flow.waiting_for_input` SSE event.
5. The bridge handles the conversation; the step completes when the agent calls `struct_output`.
6. On step completion, call `bridge.Service.Unbind(sessionId)`.
7. Emit `flow.step.completed`.

If the bridge is not configured (`cfg.Router == nil`) and a flow tries to enter an `interactive: true` step, the step MUST fail fast with a clear error — `interactive` flows are inseparable from the bridge being enabled.

### Question-tool platform-native UI

When `cfg.Router.QuestionMode == "interactive"` and an agent in a bound session calls the `question` tool, the bridge SHALL render the question using platform-native interactive UI:

| Platform | Rendering |
|---|---|
| Slack | `chat.postMessage` with `blocks` containing button elements (one per choice); reply handler matches `block_actions` payload to choice ID |
| Telegram | `sendMessage` with `reply_markup.inline_keyboard` (one button per choice); callback handler matches `callback_query.data` to choice ID |
| Mattermost | `create_post` with `props.attachments[].actions` (one action per choice); `interactiveAction` callback matches action ID to choice |

Each platform's interactive UI has scope requirements (Slack needs `chat:write` + interactivity URL configured, Telegram needs the bot mode that supports callback_data, Mattermost needs the post props feature enabled). If interactive UI fails at send time (missing scope, deprecated feature), the bridge MUST fall back to numbered-options text (`Pick one: 1) foo  2) bar`) and parse the user's reply against numeric or text matches.

The agent itself sees the same question tool API regardless of rendering — this is purely a presentation-layer concern in the bridge.

### Single-writer per identity: file lock on SQLite, GET_LOCK per identity on MySQL

Concurrent `opencode serve` processes pointing at the same database can race on chat-platform credentials (Telegram `getUpdates` offsets, Mattermost WebSocket fan-out) — both processes would receive the same inbound event and dispatch racing runs.

**SQLite (local-dev)**: at startup the bridge takes an OS file lock on `<Data.Directory>/bridge.lock` via a new `internal/fileutil/lock.go`. Two build-tagged implementations: `lock_unix.go` (`//go:build unix`) wrapping `syscall.Flock(LOCK_EX|LOCK_NB)`, `lock_windows.go` (`//go:build windows`) wrapping `LockFileEx` from `golang.org/x/sys/windows` with `LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY`. ~60 LOC total. Released automatically on process exit.

**MySQL (production)**: per-identity scoped lock via `GET_LOCK(?, 0)` on the name `'opencode_bridge:' + SHA1_HEX(project_id + ':' + channel + ':' + identity_id)`. Math: `len('opencode_bridge:') = 16`, `len(sha1_hex) = 40`, total = 56 ≤ MySQL's 64-char lock-name limit. `project_id` is in the hash because `GET_LOCK` names are server-wide and case-insensitive (not schema-scoped) — without it, two opencode deployments against the same MySQL server in different schemas would collide.

Two MySQL implementation constraints:

1. **Dedicated `*sql.Conn` per identity.** `GET_LOCK` is per-connection; releasing the conn to a pool releases the lock. The bridge holds a dedicated `*sql.Conn` (via `db.Conn(ctx)`) for the lifetime of each adapter, never returns it.
2. **Reacquire on disconnect.** If the connection drops (network blip, MySQL restart), the bridge detects via ping failure, reconnects, and re-acquires the lock before resuming. While unreacquired the adapter is marked `degraded` in `/health`.

If initial acquisition fails the adapter is marked disabled with "another opencode instance owns this identity" — other identities on the same process come up normally. An operator running two opencodes against the same MySQL with the same `project_id` can split identities between them. True active-active is out of scope.

### Config writeback: atomic, mode-safe, and the caller-mutates-cfg contract

`.opencode.json` will hold Slack bot/app tokens, Mattermost personal access tokens, and Telegram bot tokens. The current `updateCfgFile` (`internal/config/config.go:1205-1254`) has three deficiencies the bridge must fix before depending on it for identity CRUD:

1. **Not atomic.** `os.WriteFile` with no temp-file; a crash mid-write can leave truncated JSON.
2. **Hard-coded `0o644`.** World-readable mode leaks tokens on multi-user hosts and clobbers operator `chmod 600`.
3. **Doesn't refresh the in-memory `cfg` singleton.** It reads from disk, mutates the parse, writes back — `config.cfg` (what `config.Get()` returns) is untouched. Existing callers (`UpdateTheme`, `UpdateVimMode`, `UpdateAgentModel`) work around this by manually mutating `cfg.X` before invoking it.

Phase 1.2 ships the fix as one consolidated change:

- **(a)** Export as `config.UpdateCfgFile`.
- **(b)** Open `<configFile>.tmp` in the same directory, write, `f.Sync()`, `f.Close()`, `os.Rename(tmp, target)`, then `fsync` the parent directory. Parent-dir fsync is what actually guarantees rename durability on POSIX — without it a power-loss-class crash can lose the rename even after the file fsync. On Windows, Go's `os.Rename` calls `MoveFileEx(MOVEFILE_REPLACE_EXISTING)` which is best-effort atomic on NTFS; acceptable to ship best-effort initially since POSIX is the production target.
- **(c)** Write the temp file with `0o600` whenever `cfg.Router` contains any token-bearing channel; otherwise preserve existing mode via `os.Stat`. New files default `0o600`. The point is to *upgrade* pre-existing `0o644` installs the moment they gain secrets — never silently inherit a lax mode or widen one.
- **(d)** Documented caller contract: `UpdateCfgFile` does not refresh `config.cfg`. Every caller — bridge HTTP handlers, existing `Update*` helpers — must mutate the in-memory `cfg` alongside the file write. Without this contract, a `POST /identities/telegram` would update the file, return 200, and leave the running bridge with stale state until process restart.

### Library choices

| Platform | Library | Notes |
|---|---|---|
| Telegram | `github.com/go-telegram/bot` | Cleaner mid-2020s API, closer in spirit to grammy (TS lib). Open Question #1 keeps `telegram-bot-api/v5` as a fallback if feature gaps surface. |
| Slack | `github.com/slack-go/slack` | Only viable choice with Socket Mode support. The de-facto Go SDK. |
| Mattermost | `github.com/mattermost/mattermost/server/public/model` | Official Go driver. `WebSocketClient` handles framing + auth, but the reconnect loop (1s→30s exponential backoff, 20 attempts) is hand-rolled — the lib's `Listen` returns on disconnect; consumers loop. |

Hand-rolled file lock over a third-party package: ~60 LOC across two build-tagged files isn't worth a dependency.

### Drop CLI subcommands, drop env override, drop per-peer model overrides

The TS bridge's 763-LOC `cli.ts` (`telegram add`, `slack add`, `mattermost add`, `bindings set`, etc.) is not ported. Mutations go through the HTTP identity-CRUD endpoints or direct `.opencode.json` edits. The dashboard is the canonical mutation UI.

`OPENCODE_ROUTER_MODEL` env override is dropped — `/model <id>` chat command does the job, and the env var was a workaround for the missing API. Per-peer model overrides were already dropped in `780446a9`/`6878ece6`.

`/dir` chat command and per-peer directory bindings are not ported. One opencode process = one workspace, pinned to `config.WorkingDirectory()`. The TS bridge's binding abstraction was a workaround for a separate bridge process serving multiple opencodes — irrelevant in-process.

## Risks / Trade-offs

- **Bridge panic could take down `opencode serve`.** → Every adapter goroutine and the orchestrator's run-handler wraps work in `defer recover()` and logs without propagating. A single bridge failure domain cannot kill the API.
- **Two `opencode serve` instances against the same database race on credentials.** → Single-writer per identity, enforced by the database (SQLite file lock or MySQL `GET_LOCK`). Operators wanting HA must split identities between processes.
- **Slow chat-platform outbound stalls part-event delivery for other sessions.** → Non-blocking per-session queue with drop-oldest overflow guarantees the broker receive loop never blocks.
- **MySQL connection drop loses the `GET_LOCK` silently.** → Bridge pings the dedicated `*sql.Conn`; on failure, reconnect and re-acquire before resuming. Adapter marked `degraded` in `/health` until re-acquired.
- **External edit to `.opencode.json` doesn't hot-reload.** → Documented behavior. Restart-required matches every other section of `.opencode.json` today. HTTP mutations remain live via the in-memory-cfg contract.
- **Phase 1.4 requires MySQL test infrastructure the repo doesn't have today.** → Phase 1.4a spells out the five pieces of work (docker-compose service, `make test-mysql`, `//go:build mysql_integration` template, CI wiring, the bridge MySQL test). Fallback: relax the "both providers green" Success Criterion to "SQLite green via tests; MySQL verified via code review against `querier_factory.go`."
- **Token leak on existing `.opencode.json` installs at `0o644`.** → `UpdateCfgFile` rewrites mode to `0o600` whenever the config gains a token-bearing channel, upgrading pre-existing lax-mode installs on first identity CRUD.
- **Windows `os.Rename` over an existing file is best-effort atomic, not formally guaranteed.** → POSIX is the production target. Win10+ callers needing strict atomicity can switch to `SetFileInformationByHandle(FileRenameInfoEx, FILE_RENAME_FLAG_POSIX_SEMANTICS)` later if demand surfaces.
- **No data migration from the TS bridge.** → Trims scope significantly. Operators with existing Telegram pairings lose them on cutover; documented in DEPLOY.md.

## Migration Plan

There is no row-level migration. The cutover for an operator is:

1. Stop the `opencode-router` Node process.
2. Update `.opencode.json` to add the `router` section with their existing tokens (or POST them to the new identity-CRUD endpoints once the bridge is up).
3. Start `opencode serve`.
4. Re-issue any Telegram pairing codes their users had previously redeemed.

The openwork repo's `apps/opencode-router/` package stays in place but is marked deprecated in `interoperability/openwork/README.md`. No formal cutover deadline — cosmetic deprecation. Eventually the TS package can be deleted from the openwork repo.

Rollback: revert to the prior opencode version and restart `opencode-router`. The TS bridge's `opencode-router.db` is untouched by the Go bridge (it lives at a different path), so the old state is intact.

## Resolved Decisions

1. **Telegram library**: `github.com/go-telegram/bot`. Picked over `telegram-bot-api/v5` because the API ergonomics are closer to the grammy idioms the TS bridge uses today; the ~3k vs ~5k star gap isn't load-bearing. Re-open only if a feature gap surfaces during implementation.
2. **Question-tool rendering**: platform-native interactive UI (Slack blocks, Telegram inline keyboards, Mattermost interactive attachments) with numbered-text fallback. Adds ~+600 LOC across the three adapters; pays for itself in reviewer UX clarity.
3. **Legacy bare HTTP paths**: dropped hard — bare `/send`, `/identities/*`, `/config/groups` return 404. The only published external consumer is the `openrouter-communication` skill, updated in lockstep (task 11.3). No deprecated-alias period.
4. **MySQL test infrastructure**: Phase 1.4a is in scope — docker-compose MySQL service, `make test-mysql`, `//go:build mysql_integration` template, CI job, and the bridge-specific MySQL integration test all ship as part of this change. Success Criterion 12.2 ("both providers green") is binding.
5. **File watcher on `.opencode.json` for external edits**: out of scope. Dashboard + HTTP API are the canonical mutation surface; external edits match every other section's "restart required" semantics. Revisit only if operator demand surfaces.
6. **Test framework**: Go std `testing` + `httptest` for HTTP contracts, `gorilla/websocket` for WebSocket mocking (Mattermost adapter). No new test-framework dependency.
7. **Trust model for outbound on Slack/Mattermost**: free-fire — no bridge-level allowlist enforcement. Trust boundary sits at the workspace level (which workspace tokens c2-agent provisions into `cfg.Router`). Telegram's safety comes from the platform's `/start`-required policy + the existing `/pair` flow.
8. **Multi-peer support in `router_send` tool input**: single-peer only in v1. Multi-peer fan-out happens via bindings (`/router/bind`), not via tool args. If an agent wants to send to multiple peers in one call, it makes multiple tool calls.

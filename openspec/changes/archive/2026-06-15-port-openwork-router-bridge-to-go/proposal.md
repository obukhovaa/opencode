## Why

The openwork-router bridge ships today as a separate ~7.6k-LOC TypeScript process (`apps/opencode-router` in the openwork repo) that talks to this Go opencode over a loopback HTTP/SSE socket. Two processes, two languages, two SQLite databases, 71 npm packages, and a paired-PR cost for every cross-cutting change. The last three features (message-part SSE events, agent/model selection endpoints, FILE: reply convention) each needed coordinated commits in both repos. Nothing about the TS bridge is broken — it's a redundant integration boundary, and porting it in-process eliminates the Node runtime, the HTTP loopback, and the cross-repo serialization cost.

This change also folds in two adjacent gaps that any non-trivial use of the bridge by an external c2 agent orchestrator (a separate repository tracked outside this codebase) hits today:

1. **Incremental flow progress visibility** — the prior `spec/20260518T010000-flow-api-and-orchestrator.md` motivated by the same orchestrator-vs-opencode interaction was never implemented. Today a 30-minute flow is a black box to the orchestrator; the only observable signal is process exit. Narrow-scope flow API endpoints (`/flow`, `/flow/status`, `flow.step.*` SSE) solve this once.
2. **Router-initiated conversations for interactive flow steps** — the existing bridge model is user-pings-bot-first. For c2-agent's pattern, an interactive flow step needs opencode to *open* a conversation with a Slack/Telegram/Mattermost peer the user has never messaged. This requires explicit session↔peer binding before the agent's first turn lands, plus an in-process agent tool for sending messages to other channels mid-flow.

Coupling these capabilities into one change keeps the design coherent: the bridge primitives (`/router/bind`, multi-peer session bindings, mention prefixes, per-session dispatch goroutine) are the same whether they're driven by the flow engine, the c2 orchestrator over HTTP, or an in-process agent tool.

## What Changes

### Core bridge port

- Introduce a new `internal/bridge/` Go package holding the orchestrator, three platform adapters (Telegram, Slack, Mattermost), chat command handlers, the question / permission flow, media storage, the binding store, and the HTTP surface under `/router/*`.
- Add a top-level `router` section to `.opencode.json` (channels + tokens, question/permission mode, tool-updates flag). No separate `opencode-router.json`. The bridge starts iff `cfg.Router != nil` and at least one channel is enabled.
- Replace the loopback HTTP/SSE round-trip with direct in-process Go calls: prompts go through `app.ActiveAgent().Run(...)`; tool-transition events arrive via `messages.SubscribeParts(ctx)`; permission/question replies via `permission.Service` and `question.Service` method calls.
- Rework the config writeback helper `updateCfgFile` (`internal/config/config.go:1205`): export as `config.UpdateCfgFile`, replace `os.WriteFile` with temp-file + fsync + `os.Rename` + parent-dir fsync, enforce `0o600` mode when the config carries tokens, and document that callers must mutate the in-memory `cfg` alongside the file write.

### Storage

- Add two bridge tables (`bridge_sessions`, `bridge_allowlist`) on both database providers with `project_id` in every PK. Reuse opencode's existing sqlc + goose dual-provider machinery; no separate database file.
- `bridge_sessions` is **many-to-one** (many peers per `session_id`): a single session can be bound to multiple reviewers, with one row per `(channel, identity, peer_id)`. Add a `mention_handle TEXT NULL` column to carry the per-peer ping handle for first-message attribution.
- Single-writer per identity: SQLite local-dev takes an OS file lock on `<Data.Directory>/bridge.lock` (new `internal/fileutil/lock.go`, hand-rolled `syscall.Flock` / `LockFileEx`); MySQL takes a per-identity `GET_LOCK` on a dedicated `*sql.Conn` keyed by `SHA1(project_id + channel + identity_id)`.

### Per-session dispatch goroutine

- Replace the per-peer-serializer model with a **per-`sessionId` dispatch goroutine** that owns both inbound message dispatch and parts demultiplexing for a bound session. Two channels (inbound, parts) feed it; the goroutine calls `agent.Run` serially, eliminating `ErrSessionBusy` from multi-reviewer fan-in. Inbound channel buffered at 16 (drop never — user messages can't be lost); parts channel buffered at 64 with drop-oldest (parts can collapse).

### Multi-reviewer fan-out

- Outbound from a bound session SHALL fan out to every `(channel, identity, peer_id)` row pointing at that session. Failures on one peer MUST NOT block delivery to others; per-peer errors are logged and surfaced in `/router/health` per-identity status.
- Inbound from any reviewer is **attributed** before being passed to the agent: the bridge prepends a `[<mention_handle or peerId> via <channel>]: ` envelope to the prompt so the agent knows which reviewer spoke. The prompt-builder strips this envelope from anything echoed back outbound.
- The bridge fans **agent output** to all bound peers so every reviewer stays in context, regardless of which reviewer's inbound triggered the turn.

### Router-initiated conversations

- New `POST /router/bind` and `POST /router/unbind` endpoints to associate `sessionId` with one or more `PeerRef` ({channel, identity, peerId, mention?}) entries. This is the load-bearing primitive for the "first message goes from opencode → router → user (not user → bot)" flow that c2-agent's interactive flow steps need.
- The flow engine **auto-binds** when entering a step marked `interactive: true` — `interaction.target` in the flow YAML is read from `--flow-args` JSON; the orchestrator's only job at pod creation is to populate that args file. Manual `/router/bind` remains available for orchestrators that need to re-bind mid-flow.
- Channel-only peers (Slack `C<id>`, Mattermost `<channelId>` without thread) get their binding **mutated** after the first outbound creates a thread — subsequent agent output replies in-thread, inbound replies match the right session.
- User-id peers (Slack `U<id>`) are resolved to DM channels by the bridge (`conversations.open` / `channels/direct`) before binding takes effect. Callers don't need to pre-resolve.

### Flow API (narrow scope from the 2026-05-18 spec)

- Ship `GET /flow`, `POST /flow`, `GET /flow/status`, `DELETE /flow` and the SSE events `flow.step.started|completed|failed`, `flow.waiting_for_input`, `flow.completed`, `flow.failed`. Skip the spec's `GET /flow/output` and `POST /flow/input` — final output is obtainable via `/session/{id}/messages`, and inbound resumption goes through `/router/send` + the existing bridge inbound path.
- Add `--flow <id>`, `--flow-args <path>`, `--flow-exit` flags to `opencode serve` (matches the k8s Job entrypoint from the prior spec).
- Add `interactive: true` and `interaction:` block to the flow step YAML schema. `interaction.target` accepts a single `PeerRef` or an array. `interaction.mention` (optional) for first-message ping handles.
- `flow.waiting_for_input` SSE carries `sessionId`, `stepId`, and the resolved `interaction.target` so external orchestrators that bypass auto-bind can react to it.

### Agent tool: `router_send`

- New in-process tool at `internal/llm/tools/router_send.go` (the existing `internal/llm/tools/` package, alongside other agent tools — not under `internal/llm/agent/tools/` as the proposal originally suggested) that lets the agent send messages to configured router channels mid-run. The interface contract uses `tools.BridgeSender` so the tool package never imports `internal/bridge/service` (which would create an import cycle via `internal/llm/agent → tools → bridge.service → llm/agent`). Registered iff: (a) the calling agent is in `mode: "agent"` (not `subagent`); (b) at least one router channel has an enabled identity in `cfg.Router`.
- The tool's description is built dynamically at registration time from the configured channels, identities, and currently-bound peers — the agent sees what's available without needing extra documentation.
- Implementation calls `bridge.Service.Send(...)` directly in-process; no HTTP loopback. Symmetric with the existing in-process pattern (agent.Run, permission, question).
- Peer-selection contract: free-form `peerId` from the agent. Callers run in single-tenant k8s pods — trust boundary is established at the orchestrator level, not at tool dispatch.

### Question-tool platform-native UI

- When `cfg.Router.QuestionMode == "interactive"` and the agent calls the `question` tool, the bridge SHALL render choices using platform-native interactive UI: Slack interactive blocks with buttons, Telegram inline keyboards. Reply parsing maps clicks back to the question's choice labels by normalizing button callbacks into the standard `bridge.Inbound` shape.
- **Mattermost deviation (v1)**: Mattermost interactive attachments (`actions[].integration.url`) require a Mattermost-callable webhook URL the bridge does not host. Mattermost peers always use the numbered-options text fallback regardless of `QuestionMode`.
- Falls back to numbered-options text if the platform's interactive UI fails (e.g., bot lacks the scope) or if the request has more than one prompt / no options. The agent's question prompt itself is unchanged.

### Configuration & cutover

- **BREAKING (deploy-shape)**: operators stop running `opencode-router` as a separate process; tokens migrate from `~/.openwork/opencode-router/opencode-router.json` into `.opencode.json`'s new `router` section. **No data migration is shipped** — fresh chat sessions and empty allowlists on first run. The openwork repo's TS package is marked deprecated, not deleted, once the Go bridge ships.
- **Removed**: `apps/opencode-router`'s 763-LOC CLI subcommand surface (`telegram add`, `slack add`, etc.) is not ported. Mutations go through the HTTP identity-CRUD endpoints or direct `.opencode.json` edits.
- **Removed**: `OPENCODE_ROUTER_MODEL` env override, per-identity `directory` field, per-peer directory bindings, `/dir` chat command, `autoBind: true` implicit binding on `/send`. One opencode process = one workspace, pinned to `config.WorkingDirectory()`. Explicit `/router/bind` is the only binding path.
- **Renamed**: HTTP paths move under `/router/*` (was bare `/send`, `/identities/*`, `/config/groups`). The bare paths are not aliased — the only published consumer (the `openrouter-communication` skill at `~/.agents/skills/openrouter-communication/SKILL.md`) is updated as part of this change to point at the new paths.

## Capabilities

### New Capabilities

- `chat-bridge`: Inbound chat-message orchestration across Telegram, Slack, and Mattermost — peer-to-session resolution (many-to-one), prompt construction with reviewer attribution, per-`sessionId` dispatch goroutine that serializes inbound and parts onto a single `agent.Run` call site (eliminating `ErrSessionBusy` from multi-reviewer fan-in), parts demux with drop-oldest backpressure (capacity 64), inbound queue (capacity 16, no drop), chat commands (`/agent`, `/model`, `/sessions`, `/session`, `/reset`, `/pair`, `/skip`, `/help`), FILE: outbound parser, typing indicators, per-peer outbound failure isolation, multi-peer fan-out for agent output.
- `chat-bridge-adapters`: Per-platform inbound/outbound implementation — Telegram long-poll via `go-telegram/bot`, Slack Socket Mode via `slack-go/slack`, Mattermost WebSocket via `mattermost/server/public/model` with hand-rolled 1s→30s exponential-backoff reconnect. Each adapter mirrors the contract of its TS test suite (`mattermost.test.js` 1377 LOC, `slack.test.js` 273 LOC, `telegram.test.js` 204 LOC). Adapters also resolve user IDs to DM channels transparently (Slack `U<id>`, Mattermost user-id peers).
- `bridge-storage`: Dual-provider storage layer — `bridge_sessions` (many-to-one peer→session bindings with `mention_handle`, FK `ON DELETE SET NULL`) and `bridge_allowlist` (per-identity peer allowlist), both with `project_id` in every PK for multi-tenant MySQL safety. Single-writer election: SQLite file lock for local dev; per-identity MySQL `GET_LOCK` on a dedicated `*sql.Conn` for production.
- `bridge-http-api`: HTTP surface bolted onto opencode's existing mux under the `/router/*` namespace — `POST /router/send` (outbound proactive delivery, called by external orchestrators and internal agent tool), `POST /router/bind` + `POST /router/unbind` (session↔peer binding mutation), identity CRUD at `/router/identities/{telegram,slack,mattermost}[/:id]`, per-identity `/router/config/groups` toggle, extended `/health` reporting per-identity adapter status and `bridge` summary. All config mutations persist via `config.UpdateCfgFile`.
- `chat-bridge-router-initiated`: Router-initiated conversation flow — the orchestrator-driven binding pattern that lets opencode open a chat with a peer who has never messaged the bot. Auto-bind on entering an `interactive: true` flow step driven by `interaction.target` from `--flow-args`; manual `/router/bind` for external orchestrators that bind reactively. Includes first-message thread-creation semantics: channel-only peers (Slack `C<id>`, Mattermost `<channelId>`) get their binding mutated to channel+thread after the first outbound. Mention prefix on first message via `interaction.mention` or the per-peer `mention_handle`.
- `chat-bridge-agent-tool`: New in-process opencode tool `router_send` registered conditionally — agent mode only, at least one configured router channel. Tool description is built dynamically from `cfg.Router` so the agent sees available channels, identities, and currently-bound peers without external documentation. Implementation calls `bridge.Service.Send` directly in-process.
- `flow-api`: Narrow-scope flow execution API — `GET /flow`, `POST /flow`, `GET /flow/status`, `DELETE /flow`, plus SSE events `flow.step.{started,completed,failed}`, `flow.waiting_for_input`, `flow.completed`, `flow.failed`. New `--flow`, `--flow-args`, `--flow-exit` flags on `opencode serve` for the k8s Job entrypoint pattern. Adds `interactive: true` step type and `interaction:` YAML block with `target`/`targets` (single or array `PeerRef`) and optional `mention`. Final flow output is read via existing `/session/{id}/messages` — no new `/flow/output` endpoint.
- `config-atomic-writeback`: Reworked config writeback — exported `config.UpdateCfgFile` with temp-file + `os.Rename` + parent-dir fsync, mode preservation (always `0o600` when token-bearing fields present, otherwise preserve existing mode via `os.Stat`), and documented in-memory-`cfg` mutation contract for callers. Existing callers (`UpdateTheme`, `UpdateVimMode`, `UpdateAgentModel`) migrate to the new contract.

### Modified Capabilities

None — `openspec/specs/` is empty at the time of this proposal, so all behavior introduced here lands as new specs.

## Impact

- **New Go dependencies**: `github.com/go-telegram/bot`, `github.com/slack-go/slack`, `github.com/mattermost/mattermost/server/public/model`, `golang.org/x/sys/windows` (for the Windows arm of `internal/fileutil/lock.go`). All MIT/Apache. Binary growth budget: ~+15 MB.
- **Affected packages**:
   - `internal/bridge/` (new) — orchestrator, adapters, binding store, agent-tool wiring, per-session dispatch goroutine.
   - `internal/config/` — Router field + `UpdateCfgFile` rework.
   - `internal/fileutil/` — new `lock.go` (SQLite single-writer).
   - `internal/api/` — gains `/router/*`, `/flow/*` routes; SSE event types extended; `/health` reports `bridge`.
   - `internal/db/` — two migrations × two providers; `bridge_sessions.mention_handle` column; sqlc regeneration.
   - `internal/flow/` — `interactive: true` step support; `flow.waiting_for_input` emission; bind auto-call on step entry; SSE event publishing.
   - `internal/llm/tools/` — new `router_send` tool, conditionally registered; declares `BridgeSender` interface that `bridge.Service` satisfies, avoiding the import cycle.
   - `internal/message/` — broker untouched; only consumer-side changes (per-session goroutine demux).
   - `cmd/serve.go` — conditional bridge startup; new `--flow`/`--flow-args`/`--flow-exit` flags.
- **Database**: Two new tables on both SQLite and MySQL providers. `bridge_sessions` permits many rows per `session_id` (mention_handle column). New CI infrastructure for build-tag-gated MySQL integration tests (`//go:build mysql_integration`, `make test-mysql`, docker-compose service); the repo has no equivalent today.
- **Removed**: 763 LOC of CLI subcommands from the deploy surface; `~/.openwork/opencode-router/opencode-router.db` (no longer created); the loopback HTTP/SSE round-trip between opencode and the bridge; `autoBind: true` implicit binding (explicit `/router/bind` only); bare `/send` and `/identities/*` paths (move under `/router/*`).
- **Operational**: `DEPLOY.md` is rewritten — single binary, single config file, single database. Failure-mode visibility improves: bridge panics no longer go unnoticed (every adapter goroutine wraps work in `defer recover()`); `/health` reports per-identity adapter status, last-error, last-inbound-at; `/flow/status` exposes incremental flow progress.
- **External consumers to update in sync**:
   - `~/.agents/skills/openrouter-communication/SKILL.md` — paths change to `/router/*`; remove `autoBind` mention; document `/router/bind` for explicit session-peer binding.
   - The external c2 agent orchestrator (separate repository) — its k8s Job spec needs the dual-container model (opencode + no second router process), reads from `/flow/*` SSE for progress, calls `/router/bind` (or relies on flow-args auto-bind).
- **Out of scope**: data migration from the TS bridge's SQLite, sidecar deployment mode, config-file hot-reload for external edits, true active-active HA (requires solving the chat-platform-side single-consumer constraint), the `POST /flow/input` endpoint from the 2026-05-18 spec (resumption goes through chat-platform inbound + existing bridge path).
- **Source specs**: `spec/20260606T032323-go-bridge-port.md` (bridge port) and `spec/20260518T010000-flow-api-and-orchestrator.md` (flow API, narrow scope only) are the third-pass-reviewed authoritative references for prior decisions. This OpenSpec change is the consolidated, expanded restatement.

# Implementation review and verification

The implementation was reviewed independently across the controller, durable store,
bridge routing and external orchestrator. Findings resolved include initial project
scope, same-transaction lease fencing, SQLite busy retries, terminal-state/outbox
atomicity, answer/result atomicity, delayed widget identity, question/input ordering,
lease expiry at job deadlines, stale detached task notifications, admission takeover
ordering, and relay receipt commits preceding live fanout.

## Supported surfaces and decisions

- Direct Slack, Telegram and Mattermost sessions and mediated external daemon
  conversations use the same top-level flow controller.
- The tool supports goal-driven `plan`, generated `create`/`update`, immutable
  named `run`, and inspection/control operations. Existing standalone jobs cannot
  start nested flows. No new pooled free-form job type was added to c2-agent.
- Named runs snapshot parsed YAML and retain authored agents/model/routing. Each
  invocation has a conversation-scoped run namespace; controls resume that run,
  rather than sharing standalone top-level session prefixes across tenants.
- Ordinary steering arrives at the next step/iteration. A pending question is an
  interaction boundary: non-answer input closes it unanswered and is delivered to
  that same step. Explicit custom answers use the question surface or `/answer`.
- SQLite and MySQL retain definitions, progress, inbox/outbox, questions and
  receipts. Remote recovery requires MySQL and the coordinated c2-agent version.
  A legacy remote standalone `running` row predating leases must be reconciled
  with its original owner before adoption; it is not silently stolen.
- Durable state and transcript writes are fenced. Already-started external
  effects cannot be made exactly-once; receiver event IDs support deduplication.
- Session deletion stops local execution, refuses live foreign ownership, and
  deletes the conversation and durable metadata transactionally.

## Validation evidence

- OpenCode full `make test` (format, vet, race-enabled short suite and coverage).
- Real SQLite and isolated MySQL 8 suites for claims, takeover, deadlines,
  concurrent creation/deletion, message fencing, inbox/outbox and question receipts.
- `scripts/test/session_flows.sh`: real stores, engine, bridge routing, controller
  and HTTP admission with local scripted agents; no cloud services.
- Existing TUI workflow and deterministic regression scripts.
- c2-agent full instrumented race tests, `make check`, OpenAPI asset sync and the
  required bridge endpoint smoke, using Go 1.25.8 for its pinned Orchestrion.
- Cross-repository binary test: a real OpenCode server, actual c2-agent authenticated
  daemon proxy/relay and a local scripted OpenAI-compatible provider. Covers immutable
  named snapshots after YAML edits, actual terminal frames/journal replay, cancel and
  resume, preserved conversation/agent, child question/reply, queued input during a
  question with delivery acknowledgement, and foreign-peer rejection.
- c2 MySQL receiver restart after receipt commit but before fanout, stopped-daemon
  replay, stale fences, payload conflicts, and mapping isolation.

Live chat accounts and paid LLMs were not used. No service was deployed. Release
requires a worker image containing the OpenCode change and the coordinated
orchestrator change; capability negotiation refuses older workers.

## Final session completion (2026-11-12)

Revalidated after resuming this session. OpenCode make test passes with synthetic
provider credentials and an isolated HOME (70.2% aggregate coverage); 25
deterministic E2E scripts and the SQLite/MySQL integration gates pass. Provider
fixtures no longer depend on ambient Langfuse credentials. The coordinated
c2-agent change was recovered onto current main in an isolated worktree; its
make check, bridge smoke, MySQL receipts and real OpenCode binary integration pass.

Final corrections include read-only status polling, idempotent local admission
without delivery-lease contention, full-length legacy identifiers, and native
question identity. Generated shell-markup rejection and literal steering handling
are also backported to the base PR so it can be reviewed independently.

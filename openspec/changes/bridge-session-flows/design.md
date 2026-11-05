## Context

The existing implementation has useful pieces but does not yet compose them into a bridge-owned flow lifetime:

- `internal/app/dynamic_flow.go` launches a background runner after the originating primary turn releases its session. It uses the app lifetime, not the incoming bridge cancellation/deadline/requester context.
- `internal/bridge/service/dispatch.go` subscribes to message parts for one ordinary agent turn plus a short drain window. A later flow's child output is outside that delivery lifetime. Inbound messages start ordinary turns rather than entering the dynamic steering queue.
- `internal/bridge/service/question.go` routes questions by exact session binding. Dynamic steps have none. The permission router already follows root ancestry and offers a reusable ownership pattern.
- `/flow` is currently TUI-only. API flow SSE originates in its flow runner; the app's dynamic runner bypasses that adapter.
- Definitions currently live under `~/.agents[-profile]/flows/dynamic`; SQLite/MySQL stores step histories. A new remote worker can read MySQL histories but may lack the definition. File locks only coordinate processes sharing their lock filesystem.
- The existing flow parser validates YAML and resolves local includes/schema references. The runtime supports conditional routing, loops, standalone agent assignments and interactive bridge hooks. These should be reused.

This follow-up also fixes the independent startup regression: `ResolvedTools` must never wait for an MCP-discovery writer lock while the welcome screen renders. That correction does not depend on this proposal.

## Goals / Non-Goals

Goals: make a flow one durable activity of an ordinary conversation; run an existing definition faithfully; preserve replies, cancellation and identities across run boundaries and worker recovery; expose the same lifecycle to TUI and bridge surfaces.

Non-goals: nested flows, subprocess orchestration, model-selected cross-tenant routing, changing standalone YAML behavior, or promising exactly-once external side effects after a crash. A flow step cannot invoke `plan`, `create` or `run` to start another flow, including a standalone first step whose session is also its root.

## Decisions

### 1. One controller, separate conversation and run identities

Introduce a surface-independent controller whose run record has a unique run ID, root conversation session ID, step-session associations, definition kind/version, run state and owner. A conversation can have multiple sequential completed runs, but at most one active/paused run at a time. Reject a second start until the existing run is resumed, terminated or completed; a retry of the same invocation returns the same run.

The parent ordinary turn submits the run and finishes. The conversation remains flow-busy until the controller reaches a terminal or explicitly suspended state. The run does not occupy the root's agent slot while its steps execute. Incoming messages enter the run inbox, not an unrelated root turn. Completion posts one result to the conversation and makes it available for an optional subsequent ordinary turn. This avoids the current race where the parent turn finishes and the bridge assumes all work is done.

Capture requester, job arguments and authorized origin peers explicitly from the initiating request. Do not capture temporary request cancellation by accident: use an owned run context that inherits the originating job deadline and explicit stop signal, survives the submitting HTTP request, and is canceled on worker shutdown. Rehydrate identity through trusted admission on recovery; do not persist tokens or callback secrets in the definition.

### 2. Same `flow` tool, two definition kinds

Retain dynamic `create`/`update` and add generated `plan`, named `run`, plus bounded read-only `list`/`status` and `control` operations. `plan` accepts the user goal and optional complexity; configured complexity wins, and automatic/requested selection cannot bypass runtime ceilings. It records a generated-source planning request, waits for the submitting ordinary turn to release its session, and invokes the shared builder. `create` remains compatible with direct validated-definition submission. Both paths produce generated runs; `run` produces a named run and bypasses planning. Persist source kind and invocation identity before work starts, and persist the accepted definition before execution. `run` takes a discovered `flowId`, validated flow args and an idempotency key derived by the host from session/tool-call identity. The tool cannot accept arbitrary filesystem paths. `list` exposes only authorized discovered flows and their argument schemas. The existing `dynamicFlow.enabled` gate continues to expose the tool for primary agents; renamed configuration is unnecessary in this release and docs will explain the expanded tool scope.

Generated runs require OpenSpec for planning. Bridge-generated plans use the same bundled flow-creator guidance, available-agent inventory and complexity policy as TUI planning through shared controller admission; adapters must not author their own planning prompt or leave bridge agents to infer the YAML contract from the tool name. Running already-authored YAML uses the existing parser and does not require the OpenSpec CLI. Disabled definitions and forbidden agents are refused. Validate delegation permissions at admission and again when a templated step agent resolves; authored YAML cannot grant access beyond the caller's authorized agents/tools.

For named YAML, snapshot the fully parsed definition after includes and local schema references resolve. Keep its authored model/agent/routing settings rather than silently repinning or rewriting it. The ordinary chat agent stays selected; internal step agents do not switch the conversation UI. Dynamic definitions retain their existing primary-agent pinning policy.

Freeze named-flow content for the invocation. The agent cannot `update` that run or rewrite its source through the flow tool. User steering is supplemental input and cannot alter the graph, agent assignments or schemas. Normal file-edit permissions remain a separate tool policy: this is not a claim that all filesystem edits everywhere are forbidden. Resume uses the saved snapshot even if the original YAML changes or disappears. Store source path/hash for provenance. For Langfuse-backed step prompts, preserve the normal first-entry resolution semantics, persist the selected version and resolved text when first used, and reuse it for retries/recovery; remote prompt updates cannot silently change an already-entered step.

### 3. Route the whole run through its root conversation

Maintain a root-scoped delivery subscription for the run lifetime. It translates root/descendant progress, errors and terminal results to the origin's authorized peers. Do not temporarily move the user's binding to successive step sessions. Questions retain the actual requesting step session/request ID, but resolve the authorized conversation through the run association. Multiple questions queue or carry distinct request-bound widgets; answering one cannot settle another.

Reuse platform adapters and external message rendering. Root-aware question routing must enforce exact run ancestry and peer ownership, never simply choose any binding with a similar root. Existing explicit `interactive` step targets remain supported when authorized; reject a named flow whose target would broaden the initiating external conversation's authority. For external conversations, the existing exact-conversation isolation rule remains in force.

Handle explicit question replies before steering. Ordinary messages become ordered durable steering, delivered at step/iteration boundaries. Id-bearing external batches retain IDs until actual delivery is committed; accepting input is not a delivery acknowledgement. Requests to stop or terminate cancel controller work and descendants, not just the completed parent turn. Suspended runs retain their inbox and block conflicting new starts.

### 4. Session backend is authoritative; profile files remain an export

Add run-definition and run-association storage to the configured session backend, covering both SQLite and MySQL. Store immutable named snapshots, versioned dynamic snapshots, original args, status, origin references, inbox/outbox delivery state and tool-invocation identity. Persist pending question descriptors, requesting step/session identity and answer receipts as part of recoverable interaction state; the current question service's in-memory pending map is not a recovery source. Include project and conversation ownership in every lookup; a shared database is not permission to address another tenant's run.

Profile-scoped JSON files become legacy import sources/local exports, not the only recovery source. Import only after project/session ownership is verified, record a content hash, and make imports idempotent. A replacement worker needs the same session backend and workspace/tool dependencies, but not the original profile filesystem. Profiles still select runtime configuration and tools; changing profile does not automatically grant access to runs created elsewhere.

### 5. Fenced ownership for mediated workers

Use a backend lease with owner, expiry and monotonically increasing fencing token. Claim/renew/transition operations compare ownership atomically. SQLite retains single-host locking with transactional version checks. MySQL workers use database time and compare-and-set claims. The runner refuses work after lease loss; state transitions, inbox acknowledgements and outbound delivery commits require the current token. A stale worker must not report completion or acknowledge input after a replacement takes ownership.

External calls already started before lease loss cannot always be undone. Forward idempotency keys where supported, persist side-effect evidence, and expose uncertain outcomes rather than claiming exactly-once execution. The orchestrator still decides which worker should acquire a run; the runner enforces exclusive ownership. Run creation, worker admission, heartbeat, stop, resume and terminal acknowledgement need a versioned capability handshake with the external orchestrator.

### 6. Share lifecycle events with API/SSE and worker ownership

Publish controller events through the API's existing event adapter with run ID, root session ID, definition kind, step/session IDs and state. Root creation acceptance is not outer-job completion. Pool ownership, busy checks, shutdown waits and external inbound admission consult the controller's run lifetime. The worker remains assigned while any step or human question is active; suspended state is explicit so an orchestrator can release/reassign safely.

Use durable event IDs/outbox receipts for retryable external delivery. A reconnect requests state plus an event cursor; duplicate final frames are recognized rather than re-posted. A worker replacing one that was waiting for a question restores or reissues the pending interaction with a stable logical question identity and maps a late answer to that exact step. Already committed answers must not be asked again or consumed twice, and unrelated new questions cannot reuse the old widget's identity. Direct adapters can reuse their current best-effort editing behavior, while terminal result delivery has a durable retry record. No adapter-specific flow engine is needed.

### 7. Two approval and delivery waves

Wave A: shared controller/store contract, direct bridge routing, named immutable YAML execution, surface tests and backward-compatible local import. This is the recommended first implementation scope.

Wave B: MySQL takeover/fencing, external ownership protocol, durable event delivery and pool recovery, coordinated with the external orchestrator. External bridge clients on a single persistent worker can use the same routing from A, but cross-worker recovery must not be advertised until B and the orchestrator integration pass together.

Nested flows are discarded, not postponed implicitly. The guard remains even after both waves. An agent needing more work should amend its own unentered dynamic steps, use task fanout, or finish one top-level run and start another from the ordinary conversation.

## Risks / Trade-offs

- Named YAML can declare privileged or templated agents → delegation is checked at admission and dispatch, with no policy expansion from flow arguments.
- Concurrent question/reply/steering races → preserve request IDs and ordered inbox delivery; test delayed/duplicate widgets and batches.
- Local manifest migration can associate the wrong run → verify project/session ownership and snapshot hash before importing.
- Lease loss during a side-effecting tool → stop new calls and refuse stale writes; preserve evidence for explicit retry decisions.
- Waiting on a human can outlive a worker → persist suspended/waiting state and transfer ownership through the orchestrator handshake.
- Outbound retries can duplicate messages → durable event identity and recipient-scoped receipt keys; no exactly-once claim without receiver support.
- Database migration adds operational cost → additive tables first, legacy files retained until verified import, feature gates default unchanged.

## Migration Plan

1. Ship additive storage/migrations and capability reporting, with no new bridge behavior enabled.
2. Import legacy manifests lazily on verified access; preserve files for rollback/export.
3. Enable Wave A for direct bridge sessions and validate TUI/standalone regressions.
4. Ship orchestrator capability support and backend fencing; enable Wave B only for compatible peers/workers.
5. Roll back by disabling bridge-flow admission and draining/suspending active runs. Keep new tables and imported source files; an older binary must not resume a newer active run. Export definitions if manual recovery is needed.

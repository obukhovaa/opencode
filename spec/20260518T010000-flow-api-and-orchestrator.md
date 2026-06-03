# Flow API for Server Mode and Orchestrator Integration

**Date**: 2026-05-18
**Status**: Draft — partially superseded (see "Status as of 2026-06-03" below)

## Status as of 2026-06-03

None of the work proposed in this spec has shipped. In the two weeks since it was filed, the orchestrator-integration problem space was attacked through a different, smaller primitive instead.

**What did NOT land from this spec:**

- ❌ `GET /flow`, `POST /flow`, `GET /flow/status`, `GET /flow/output`, `POST /flow/input`, `DELETE /flow` — no flow endpoints exist in `internal/api/server.go`.
- ❌ `--flow`, `--flow-args`, `--flow-exit` flags on `opencode serve` — `cmd/serve.go` has no flow flags.
- ❌ SSE `flow.step.*` / `flow.waiting_for_input` / `flow.completed` / `flow.failed` events — `internal/api/handler_event.go` only fans out `message`, `session`, `permission`, and `question` events.
- ❌ `interactive: true` step type and `interaction:` YAML block — flow steps still have no notion of conversational pause; `internal/flow/service.go` keeps only the pre-existing `postponed` status from the in-process-loop work.
- ❌ Orchestrator dual-container pod migration and router programmatic binding (Phases 3 and 4 — these live outside this repo and are unaffected by the no-op here).

**What landed instead (related work):**

- ✅ **Question tool** (`spec/20260528T120000-question-tool.md`, Implemented in `68291ff`) — adds a `question` tool plus `GET /question`, `POST /question/{requestID}/reply`, `POST /question/{requestID}/reject` HTTP endpoints and `question.*` SSE events. This is a *smaller, agent-level* primitive than the spec's `interactive: true` step, but it solves the same human-in-the-loop problem at finer granularity — any step (not just designated ones) can pause and surface structured options to the user.
- ✅ **OpenWork integration** (`interoperability/openwork/{README,DEPLOY}.md`) — OpenWork is the new name for the chat-bridge sidecar (formerly "opencode-router"). It wires the question tool into Slack/Telegram via the `OPENCODE_ENABLE_QUESTION_TOOL=1` env var and `"questionMode": "interactive"` config — delivering the chat-bridging UX this spec sketched, but built on `/question` + `/session` rather than a dedicated `/flow` API.
- ✅ **General serve/ACP hardening** (`f3c4f42 feat(server):acp and serve feature for remote access`, `52a0677 feat(servere):extend api`, `e7c2092 fix(server):support auto approve for server mode`, `6cfdd31 feat(serve):add --agent flag to pin active agent at startup`) — server-side surface area required for any remote orchestration is now in place, just without flow-specific endpoints.
- ✅ **In-process step loops** (`spec/20260602T120000-in-process-step-loops.md`, Implemented in `21b19e2`) — iteration support inside a single step. Orthogonal to this spec but worth noting because it reshaped `internal/flow/service.go` substantially since this spec was drafted; any future flow API implementation must take iteration state into account.

**Open design question for whoever picks this up:**

Given that OpenWork now drives agents via `POST /session/{id}/message` + `/question` rather than a flow-level API, the original motivation for `/flow` endpoints is weaker — flows-as-runs may simply not be the right abstraction for orchestrated chat conversations. The remaining real gap is **incremental progress visibility** (problem #1 in the original problem statement): a long flow run is still a black box to anything that isn't tailing logs. Two possible paths forward:

1. **Narrow scope** — ship only `GET /flow`, `POST /flow`, `GET /flow/status`, and `flow.step.*` SSE events to expose progress. Drop the interactive-step design entirely (question tool covers that case). Drop `POST /flow/input` and `flow.waiting_for_input`.
2. **Drop the spec** — accept that orchestrators should construct flow-equivalent behavior by chaining `/session` calls and observing `question.*` / `message.*` SSE events, and remove `/flow` from the roadmap.

The original Implementation Plan checkboxes below are preserved unchanged but should be read in light of this status block.

## Problem

The C2 Agent Orchestrator spawns opencode as a K8s Job, passes flow arguments via env vars and configmaps, then scrapes container logs for JSON output. This approach has three problems:

1. **No incremental progress** — the orchestrator learns nothing until the container exits. A 30-minute flow is a black box.
2. **Fragile output parsing** — `extractJSON()` scans backwards through stdout for the last valid JSON object. Git warnings, echo lines, or multiline tool output can break it.
3. **No interactivity** — flows are fully autonomous. There's no way for a step to pause, ask a human a question (via Slack/Telegram), and resume with the answer.

## Goals

1. Expose flow execution via the HTTP server API so the orchestrator can start a flow, poll progress, and receive step completions incrementally.
2. Support an "interactive" step type where the agent converses with a human (via Slack/Telegram through opencode-router) before producing structured output.
3. Keep opencode agnostic about the orchestrator and messaging platforms — the API is generic; opencode-router handles Slack/Telegram I/O.

## Non-Goals

- Implementing Slack/Telegram bridges inside opencode — opencode-router already does this.
- Replacing the CLI flow mode (`opencode -p ... -F flow-id`) — it remains as-is for local use.
- Multi-flow concurrency within a single serve process — one flow at a time per server instance is sufficient for the K8s Job model where each job is a separate container.

## Current Architecture

### How the orchestrator runs flows today

```
Orchestrator                        K8s Job (opencode container)
    │                                        │
    ├─ Create K8s Job ──────────────────────>│
    │   env: AGENT_FLOW=review-code          │
    │   env: USER_PROMPT=...                 │
    │   volume: flow-args.json               │
    │                                        │
    │   (black box for N minutes)            │
    │                                        │
    ├─ Poll heartbeat ─────────────────────> │
    │                                        │
    │   (job completes)                      │
    │                                        │
    ├─ Read pod logs ──────────────────────> │
    │   extractJSON(stdout) ─────────> parse │
    └────────────────────────────────────────┘
```

### How flows execute internally

`flow.Service.Run()` returns two channels:
- `<-chan AgentEvent` — per-message events from each step's agent
- `<-chan *FlowState` — step-level state transitions (running → completed/failed/postponed)

`FlowState` contains: `SessionID`, `RootSessionID`, `FlowID`, `StepID`, `Status`, `Args`, `Output`, `IsStructOutput`.

Steps can produce structured output via `struct_output` tool. Steps can be `postponed` (paused, waiting for user input on resume).

### How opencode-router works today

The opencode-router is a sidecar that bridges Slack/Telegram with the opencode HTTP API:

1. Creates opencode sessions via `@opencode-ai/sdk/v2`
2. Sends user messages as prompts to sessions
3. Subscribes to SSE events and delivers agent responses back to Slack/Telegram
4. Supports Slack threads via `channelId|threadTs` peerId format
5. Manages session-to-peer (chat) mapping in a SQLite store

The router communicates with opencode exclusively via the HTTP REST API — it has no awareness of flows, steps, or structured output.

## Design

### Protocol choice: HTTP serve, not ACP

**HTTP serve** is the right choice for the orchestrator because:

- **Network-addressable** — the orchestrator connects over the pod network, not stdio
- **Pollable** — the orchestrator's heartbeat loop can poll flow progress alongside health checks
- **SSE for push** — real-time step progress without polling, using the existing SSE infrastructure
- **Stateless caller** — the orchestrator doesn't need to maintain a session state machine; it just calls REST endpoints
- **Multi-client** — the orchestrator, opencode-router, and monitoring tools can all read the same SSE stream concurrently

**ACP is wrong** for this because:
- ACP requires spawning opencode as a child process with stdio pipes — not feasible across K8s pods
- ACP is designed for 1:1 editor integration, not for service-to-service communication

### New pod architecture

```
┌─ K8s Pod ─────────────────────────────────────────────────┐
│                                                           │
│  ┌─ opencode container ──────────┐                        │
│  │  opencode serve               │                        │
│  │    --port 8080                │                        │
│  │    --flow review-code         │                        │
│  │    --flow-args /ws/args.json  │                        │
│  │                               │◄── Orchestrator (HTTP) │
│  │  /workspace (shared volume)   │                        │
│  └───────────────────────────────┘                        │
│             ▲ HTTP localhost:8080                          │
│             │                                             │
│  ┌─ opencode-router sidecar ─────┐                        │
│  │  opencode-router              │                        │
│  │    --opencode-url :8080       │◄── Slack/Telegram      │
│  │                               │                        │
│  │  /workspace (shared volume)   │                        │
│  └───────────────────────────────┘                        │
└───────────────────────────────────────────────────────────┘
```

Both containers mount the same `/workspace` volume. The router connects to opencode via `localhost:8080`. The orchestrator connects via the pod's cluster IP.

### New container entrypoint

Instead of the current entrypoint that runs `opencode -p "..." -F flow-id -f json`, the container runs:

```bash
opencode serve --port 8080 --flow <flow-id> --flow-args /workspace/flow-args.json
```

New flags on `serve`:
- `--flow <flow-id>` — start this flow automatically when the server starts
- `--flow-args <path>` — JSON file with flow arguments (currently mounted as configmap)
- `--flow-exit` — exit the process after the flow completes (default when `--flow` is set; only applies to `--flow`-triggered flows, not `POST /flow`-triggered ones)

The server starts, becomes healthy, begins the flow, serves progress via API, and exits on completion.

### HTTP API: Flow Endpoints

#### List available flows

```
GET /flow

Response 200:
[
  {
    "id": "review-code",
    "name": "Code Review",
    "description": "...",
    "args": { "type": "object", "properties": {...} }
  }
]
```

#### Start flow

```
POST /flow
{
  "flowID": "review-code",
  "args": {"hash": "abc123"},
  "sessionPrefix": "review"
}

Response 202:
{
  "runID": "run-uuid",
  "flowID": "review-code",
  "status": "running",
  "currentStep": "find-files"
}
```

Auto-started via `--flow` flag, or manually via this endpoint.

#### Get flow status

```
GET /flow/status

Response 200:
{
  "runID": "run-uuid",
  "flowID": "review-code",
  "status": "running",          // running, completed, failed, waiting_for_input
  "currentStep": {
    "id": "review-code",
    "status": "running",
    "sessionID": "sess-uuid",
    "startedAt": 1716000000
  },
  "completedSteps": [
    {
      "id": "find-files",
      "status": "completed",
      "sessionID": "sess-uuid-1",
      "output": {"files": ["a.go", "b.go"]},
      "startedAt": 1716000000,
      "completedAt": 1716000060
    }
  ],
  "pendingInput": null           // non-null when status is "waiting_for_input"
}
```

This endpoint serves as the catch-up mechanism for SSE race conditions on startup — the orchestrator connects to SSE, then immediately calls `GET /flow/status` to get current state including any steps completed before the SSE connection.

#### Get flow output (final result after completion)

```
GET /flow/output

Response 200:
{
  "runID": "run-uuid",
  "flowID": "review-code",
  "status": "completed",
  "output": { ... },             // structured output from the final step
  "steps": [ ... ]               // all step results
}
```

#### Submit input for interactive steps

```
POST /flow/input
{
  "runID": "run-uuid",
  "stepID": "spec-review",
  "input": "Looks good, proceed with implementation"
}

Response 200:
{
  "accepted": true
}
```

This injects the user's text as a new prompt into the step's session and triggers a new agent turn. The step's agent sees the full conversation history (its own previous messages plus the user's input). This is the same as what happens when resuming a postponed flow via CLI (`opencode -s <session-id> -F flow-id`) — the session already has context, and the new prompt continues the conversation.

#### Abort a running flow

```
DELETE /flow
{
  "runID": "run-uuid"
}

Response 200:
{
  "aborted": true
}
```

Cancels the current step's agent, sets the flow status to `failed`. The orchestrator can try graceful abort first via this endpoint, then kill the pod as a fallback.

### SSE Events for Flow Progress

The existing SSE event stream (`GET /event`) carries flow events alongside message/session events:

```json
{"type": "flow.step.started", "properties": {"runID": "...", "stepID": "find-files", "sessionID": "..."}}
{"type": "flow.step.completed", "properties": {"runID": "...", "stepID": "find-files", "output": {...}}}
{"type": "flow.step.failed", "properties": {"runID": "...", "stepID": "find-files", "error": "..."}}
{"type": "flow.waiting_for_input", "properties": {"runID": "...", "stepID": "spec-review", "sessionID": "...", "channel": {...}}}
{"type": "flow.completed", "properties": {"runID": "...", "output": {...}}}
{"type": "flow.failed", "properties": {"runID": "...", "error": "..."}}
```

### Interactive Steps via opencode-router

#### Architecture

An interactive step doesn't need new protocol between opencode and the user. It uses the **existing opencode-router bridge**:

1. Flow engine starts an interactive step in a session (like any step)
2. The step's agent runs its prompt, produces output (e.g., a question to the user)
3. The flow engine tells the router to connect this session to a Slack thread
4. The router bridges: user messages → opencode session prompts, agent responses → Slack messages
5. The agent converses with the user in the session until it calls `struct_output`
6. `struct_output` signals step completion → flow advances to the next step
7. The router disconnects from the session (further messages to the thread are ignored)

#### Flow YAML

```yaml
steps:
  - id: spec-review
    interactive: true
    interaction:
      channel: slack              # or telegram
      thread_context: |           # initial message posted to Slack to start the thread
        🔍 **Spec Review Required**
        Flow: ${args.flowID}
        Please review the specification below and provide feedback.
    prompt: |
      You are reviewing a specification with a human reviewer.
      Ask clarifying questions about the spec until the reviewer approves.
      When approved, output {"approved": true, "feedback": "summary of changes"}.
    output:
      schema:
        type: object
        properties:
          approved:
            type: boolean
          feedback:
            type: string
```

#### Mechanism

When the flow engine reaches an `interactive: true` step:

1. **Step agent runs first turn** — produces the initial review/question
2. **Flow engine emits `flow.waiting_for_input`** SSE event with:
   - `sessionID` — the step's session
   - `channel` config from the YAML (`slack`/`telegram`)
   - `thread_context` — the initial message to post
3. **Orchestrator receives the SSE event** and instructs opencode-router to:
   - Post `thread_context` as a new message in the configured Slack channel (creating a thread)
   - Bind the step's `sessionID` to that Slack thread's peerId (`channelId|threadTs`)
4. **Router bridges the conversation**:
   - User replies in Slack thread → router sends as prompt to the step's session
   - Agent responds → router delivers to Slack thread
   - This repeats for as many turns as needed
5. **Agent calls `struct_output`** → step completes with structured output
6. **Flow engine advances** → emits `flow.step.completed` SSE event
7. **Router unbinds** the session from the Slack thread
8. **Agent posts a final summary** to the thread (e.g., "Step completed. Approved with feedback: ...")

The key insight: **opencode doesn't need to know about Slack/Telegram**. The opencode-router already knows how to bridge a session to a Slack thread. The only new thing is the orchestrator telling the router *which* session to bind to *which* thread, triggered by the `flow.waiting_for_input` event.

#### Concurrency: threads solve the multi-flow problem

Each interactive step gets its own Slack thread:
- Flow A's spec-review step → new thread in `#agent-reviews` channel
- Flow B's spec-review step → different thread in the same channel

Threads are isolated — messages in one thread don't affect another. The router's `peerId` format (`channelId|threadTs`) already supports this. No per-user channels needed.

For user-specific routing (e.g., the reviewer is a specific person):

```yaml
interaction:
  channel: slack
  target_user: ${args.reviewer_slack_id}  # DM to specific user
```

Or for team channels:

```yaml
interaction:
  channel: slack
  target_channel: C12345678               # specific Slack channel
```

### Authentication

The serve mode supports `OPENCODE_SERVER_PASSWORD` for HTTP Basic Auth. The orchestrator passes this as a K8s env var (same as today's setup). The opencode-router sidecar also receives it to authenticate its SDK calls. No code changes needed — both already support the env var.

### Orchestrator Changes

The orchestrator's `RunJob` changes from:

```
1. Create K8s Job with CLI entrypoint
2. Wait for job completion via k8s watch
3. Scrape pod logs for JSON
```

To:

```
1. Create K8s Pod with opencode + opencode-router sidecars
2. Wait for health check on pod IP:8080
3. Subscribe to SSE + call GET /flow/status for initial state
4. On flow.step.completed → log progress, update heartbeat
5. On flow.waiting_for_input → instruct router to bind session to Slack thread
6. On flow.completed → GET /flow/output for result
7. Pod exits automatically (--flow-exit)
```

The K8s Job spec adds a readiness probe on `/global/health`, exposes port 8080, and adds the opencode-router sidecar container.

## Implementation Plan

### Phase 1: Flow API on HTTP Server

1. [ ] Add `GET /flow` — list available flows
2. [ ] Add `POST /flow` — start a flow run
3. [ ] Add `GET /flow/status` — current flow state and step progress
4. [ ] Add `GET /flow/output` — final structured output
5. [ ] Add `DELETE /flow` — abort a running flow
6. [ ] Add `--flow`, `--flow-args`, `--flow-exit` flags to `cmd/serve.go`
7. [ ] Wire flow state transitions to SSE events (`flow.step.*`, `flow.completed`, `flow.failed`)

### Phase 2: Interactive Steps

8. [ ] Add `interactive: true` and `interaction` section to flow step YAML schema
9. [ ] Implement `waiting_for_input` flow status when interactive step's first agent turn completes
10. [ ] Add `POST /flow/input` endpoint — injects input as new prompt in step session, triggers agent turn
11. [ ] Wire SSE `flow.waiting_for_input` event with session ID and channel config
12. [ ] When step produces `struct_output`, mark step completed and advance flow normally

### Phase 3: Orchestrator Migration

13. [ ] Update K8s Job spec: dual-container pod (opencode + opencode-router sidecars)
14. [ ] Add readiness probe and port exposure
15. [ ] Replace log scraping with HTTP API polling + SSE subscription
16. [ ] Add orchestrator logic: on `flow.waiting_for_input`, instruct router to bind session to Slack thread
17. [ ] Add orchestrator logic: on `flow.step.completed`, instruct router to unbind session

### Phase 4: Router Enhancements (if needed)

18. [ ] Add API endpoint to opencode-router for programmatic session-to-thread binding (currently only manual pairing)
19. [ ] Add API endpoint to opencode-router for unbinding a session from a thread
20. [ ] Handle "step completed" notification to post a final summary and stop bridging

## Compatibility

- **Existing CLI flows**: Unchanged. `opencode -p "..." -F flow-id` continues to work.
- **Existing serve mode**: Flow endpoints return empty lists / 404 when no flow is running. The `--flow` flag is optional.
- **Existing orchestrator**: Can continue using the log-scraping approach until migration is complete. The two approaches can coexist since the new entrypoint is a different command.
- **Existing opencode-router**: Works as-is for manual chat. Phase 4 adds programmatic binding for interactive steps.

## Edge Cases

- **Flow timeout**: The `--flow-exit` flag combined with K8s Job `activeDeadlineSeconds` handles timeouts. The orchestrator can also call `DELETE /flow` to abort gracefully first.
- **Pod restart**: Flow state is persisted in the database (existing `flow_states` table). On restart with the same session, the flow resumes from the last completed step.
- **Interactive step timeout**: The orchestrator sets a deadline when the `flow.waiting_for_input` event arrives. If no `struct_output` from the agent within N minutes, the orchestrator posts a timeout message to the Slack thread and calls `DELETE /flow` or `POST /flow/input` with a cancellation message.
- **Multiple SSE clients**: Safe — the existing pubsub broker supports multiple subscribers.
- **SSE connection race**: The orchestrator calls `GET /flow/status` immediately after connecting to SSE to catch up on any steps completed before the connection was established. No events are missed.
- **User messages after step completion**: Once the step produces `struct_output` and completes, the router unbinds the session. Further Slack messages in the thread are ignored. The router could post a "conversation closed" message to the thread.
- **Concurrent interactive steps**: Each gets its own Slack thread. The router's `channelId|threadTs` peerId format already isolates them.

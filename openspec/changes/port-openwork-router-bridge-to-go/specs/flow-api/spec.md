## ADDED Requirements

### Requirement: Flow execution API endpoints

The HTTP server SHALL expose four flow-execution endpoints under `/flow/*`:

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/flow` | List available flows (auto-discovered from `.opencode/flows/*.yaml`) |
| `POST` | `/flow` | Start a flow run with given flow ID and arguments |
| `GET` | `/flow/status` | Current state of the running (or last-completed) flow |
| `DELETE` | `/flow` | Abort the current run |

The endpoints MUST be mounted on opencode's existing API mux. Auth and localhost-only posture are inherited from the API server.

The endpoints from the prior `spec/20260518T010000-flow-api-and-orchestrator.md` that are explicitly **not** included in this change:

- `POST /flow/input` — reviewer replies come in via chat platforms through the normal bridge inbound path; no second HTTP input mechanism.
- `GET /flow/output` — final step output is read via the existing `GET /session/{id}/messages` endpoint; `struct_output` results are already in the session message stream.

#### Scenario: GET /flow lists available flows

- **WHEN** `GET /flow` is called and `.opencode/flows/review.yaml` and `.opencode/flows/release.yaml` exist
- **THEN** the response is a JSON array containing two entries with `id`, `name`, `description`, and `args.schema` fields

#### Scenario: POST /flow starts a run

- **WHEN** `POST /flow` is called with `{flowID: "review", args: {hash: "abc123"}}` and no flow is currently running
- **THEN** the response is 202 with `{runID, flowID, status: "running", currentStep}`; the flow begins executing

#### Scenario: Only one flow at a time

- **WHEN** `POST /flow` is called while a flow is already running
- **THEN** the response is 409 with a message indicating one-flow-per-process is the model

#### Scenario: GET /flow/status reports current state

- **WHEN** `GET /flow/status` is called during a multi-step flow
- **THEN** the response includes the current step (id, sessionID, status, startedAt) and the list of completed steps with their outputs

#### Scenario: DELETE /flow aborts gracefully

- **WHEN** `DELETE /flow` is called for a running flow
- **THEN** the current step's agent is cancelled via the agent's context; the flow status transitions to `failed` with an `aborted` reason; the per-session dispatch goroutine (if any) is torn down

#### Scenario: Status after completion

- **WHEN** the flow finishes and `GET /flow/status` is called afterwards
- **THEN** the response reflects the final state with `status == "completed"` (or `"failed"`); subsequent `POST /flow` is allowed (one-at-a-time, but sequential runs OK)

### Requirement: SSE events for flow progress

The existing `GET /event` SSE stream SHALL carry six new event types: `flow.step.started`, `flow.step.completed`, `flow.step.failed`, `flow.waiting_for_input`, `flow.completed`, `flow.failed`. Event payload schemas:

| Type | Payload fields |
|---|---|
| `flow.step.started`      | `runID, stepID, sessionID, startedAt` |
| `flow.step.completed`    | `runID, stepID, sessionID, output, startedAt, completedAt` |
| `flow.step.failed`       | `runID, stepID, error, startedAt, failedAt` |
| `flow.waiting_for_input` | `runID, stepID, sessionID, target` (resolved `PeerRef` or array thereof) |
| `flow.completed`         | `runID, completedAt` (no output — orchestrator reads from session messages) |
| `flow.failed`            | `runID, error, failedAt` |

External orchestrators subscribing to `/event` MUST be able to combine these with `GET /flow/status` to catch up on any events emitted before the SSE connection was established (the existing message-broker semantics — events are not buffered for late subscribers).

#### Scenario: Step lifecycle emits started→completed pair

- **WHEN** an autonomous step runs to completion
- **THEN** the SSE stream emits `flow.step.started` then `flow.step.completed` for that step

#### Scenario: Interactive step emits waiting_for_input

- **WHEN** a step with `interactive: true` enters the conversation phase (after the agent's first turn but before `struct_output`)
- **THEN** the SSE stream emits `flow.waiting_for_input` with the step's `sessionID` and resolved `target` (the same `PeerRef`(s) the flow engine auto-bound)

#### Scenario: Flow completion emits flow.completed without output

- **WHEN** the final step of a flow completes
- **THEN** the SSE stream emits `flow.completed { runID, completedAt }` — orchestrators read final output via `GET /session/{currentSession}/messages`

#### Scenario: SSE catch-up via /flow/status

- **WHEN** an orchestrator connects to `/event` after the flow has been running for 30 seconds (missing the initial `flow.step.started` for the current step)
- **THEN** calling `GET /flow/status` immediately returns the current step's state, allowing the orchestrator to reconcile without missing data

### Requirement: --flow, --flow-args, --flow-exit CLI flags on serve

The `opencode serve` command SHALL accept three new flags supporting the k8s Job entrypoint pattern:

| Flag | Purpose |
|---|---|
| `--flow <id>` | Auto-start this flow when the server boots; the server becomes healthy first, then begins flow execution |
| `--flow-args <path>` | Path to JSON file with flow arguments (e.g., `/workspace/flow-args.json`) |
| `--flow-exit` | Exit the process after the flow completes (success OR failure); default behavior when `--flow` is set; only applies to `--flow`-triggered flows (not `POST /flow`-triggered ones) |

#### Scenario: Server boots, becomes healthy, then auto-starts flow

- **WHEN** `opencode serve --port 8080 --flow review --flow-args /ws/args.json --flow-exit` is invoked
- **THEN** the server starts and `/health` reports ready BEFORE the flow begins; the orchestrator can subscribe to SSE before any `flow.step.*` events are emitted

#### Scenario: --flow-exit exits after completion

- **WHEN** the auto-started flow completes (success or failure)
- **THEN** the process exits cleanly with code 0 (completion) or non-zero (failure); the k8s Job transitions to its terminal phase naturally

#### Scenario: Manual POST /flow during --flow-running flow

- **WHEN** `POST /flow` is called against a server running an auto-started `--flow` execution
- **THEN** the response is 409 (one-flow-per-process) just like the auto-flow being concurrent with itself

### Requirement: interactive flow step type with interaction block

The flow YAML schema SHALL support an `interactive: true` flag on step definitions, accompanied by an `interaction:` block. When the flow engine enters a step with `interactive: true`:

1. Resolve `interaction.target` from flow-args expression (e.g., `${args.reviewer}`).
2. Call `bridge.Service.Bind(sessionId, target)` synchronously. If the bridge is not enabled (`cfg.Router == nil`), the step MUST fail fast with a clear error indicating the bridge is required for interactive steps.
3. Optionally call `bridge.Service.SetMention(sessionId, peer, mention)` if `interaction.mention` is provided.
4. Emit `flow.waiting_for_input` SSE event.
5. Invoke the step's agent normally — first-turn output fans out to all bound peers via the bridge.
6. The bridge handles the conversation until the agent calls `struct_output`.
7. On step completion, call `bridge.Service.Unbind(sessionId)` to release all bindings for the step's session.
8. Emit `flow.step.completed`.

`interaction.target` MUST accept either a single `PeerRef` object or an array of `PeerRef` objects, supporting multi-reviewer scenarios.

#### Scenario: Single-reviewer interactive step

```yaml
- id: spec-review
  interactive: true
  interaction:
    target: ${args.reviewer}
    mention: ${args.reviewerHandle}
  prompt: ...
  output:
    schema: { ... }
```

- **WHEN** the flow engine enters `spec-review` with `args.reviewer = {channel:"slack",identity:"default",peerId:"D012345"}`
- **THEN** the bridge is bound to that one peer; the agent's first turn fans to Slack DM `D012345` with the mention prefix; replies from `D012345` route to the step's session

#### Scenario: Multi-reviewer interactive step

```yaml
- id: spec-review
  interactive: true
  interaction:
    target: ${args.reviewers}    # array
```

- **WHEN** `args.reviewers` is `[{channel:"slack",..., peerId:"D1"}, {channel:"telegram",..., peerId:"12345"}]`
- **THEN** the bridge binds the step's session to both peers; agent output fans out to both; inbound from either resolves to the step's session with reviewer attribution

#### Scenario: Interactive step without bridge configured fails fast

- **WHEN** a flow with an `interactive: true` step starts on a process where `cfg.Router == nil`
- **THEN** the step transitions to `failed` immediately with an error like `interactive step requires router configuration; cfg.Router is nil`; subsequent steps do not execute

#### Scenario: Unbind on step completion

- **WHEN** an interactive step's agent calls `struct_output`
- **THEN** the step transitions to `completed`, `bridge.Service.Unbind(sessionId)` is called, and the dispatcher goroutine for that session exits

### Requirement: Flow run aborts cleanly via DELETE /flow

When `DELETE /flow` is called for a running flow, the flow engine MUST:

1. Cancel the current step's agent via its context.
2. If the current step has bound peers, call `bridge.Service.Unbind(sessionId)`.
3. Transition the flow status to `failed` with reason `aborted`.
4. Emit `flow.step.failed` then `flow.failed` SSE events.

#### Scenario: Abort during autonomous step

- **WHEN** `DELETE /flow` is called while an autonomous step is running
- **THEN** the agent's context is cancelled, the step transitions to failed (reason: aborted), and `flow.failed` is emitted

#### Scenario: Abort during interactive step

- **WHEN** `DELETE /flow` is called while a step is in `waiting_for_input`
- **THEN** the bridge unbinds the session; the dispatcher goroutine exits; the step transitions to failed (reason: aborted); `flow.failed` is emitted; subsequent inbound from previously-bound peers is treated as user-initiated (creates a fresh session, not the aborted one)

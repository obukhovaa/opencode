## MODIFIED Requirements

### Requirement: Flow execution API endpoints

The HTTP server SHALL expose four flow-execution endpoints under `/flow/*`:

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/flow` | List available flows (auto-discovered from `.opencode/flows/*.yaml`) |
| `POST` | `/flow` | Start a flow run with given flow ID and arguments |
| `GET` | `/flow/status` | Current state of the running (or last-completed) flow |
| `DELETE` | `/flow` | Abort the current run |

The endpoints MUST be mounted on opencode's existing API mux. Auth and localhost-only posture are inherited from the API server.

The `POST /flow` body SHALL accept, besides `flowID`, `args` and `fresh`, an optional boolean `recoverRunning`. When true it is forwarded as `flow.RunOptions.RecoverRunning`: the caller asserts that the process which left this flow's `running` `flow_states` rows is dead, so the runtime resumes those steps instead of replaying them as another process's live run (see `flow-runtime-resume`). It defaults to false; a caller that cannot vouch for the owner's death MUST NOT send it.

The endpoints from the prior `spec/20260518T010000-flow-api-and-orchestrator.md` that are explicitly **not** included in this change:

- `POST /flow/input` — reviewer replies come in via chat platforms through the normal bridge inbound path; no second HTTP input mechanism.
- `GET /flow/output` — final step output is read via the existing `GET /session/{id}/messages` endpoint; `struct_output` results are already in the session message stream.

#### Scenario: GET /flow lists available flows

- **WHEN** `GET /flow` is called and `.opencode/flows/review.yaml` and `.opencode/flows/release.yaml` exist
- **THEN** the response is a JSON array containing two entries with `id`, `name`, `description`, and `args.schema` fields

#### Scenario: POST /flow starts a run

- **WHEN** `POST /flow` is called with `{flowID: "review", args: {hash: "abc123"}}` and no flow is currently running
- **THEN** the response is 202 with `{runID, flowID, status: "running", currentStep}`; the flow begins executing

#### Scenario: POST /flow with recoverRunning resumes a dead owner's step

- **WHEN** `POST /flow` is called with `{flowID: "review", args: {…}, recoverRunning: true}` and the flow's `flow_states` hold a `running` row left by a pod that was killed
- **THEN** the run is accepted and the running step is re-entered in its own session rather than replayed

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

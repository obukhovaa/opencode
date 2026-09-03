# Flow API

## Purpose

Defines the flow-execution HTTP surface (`GET|POST|DELETE /flow`, `GET /flow/status`), the six SSE `flow.*` event types carried on the existing `/event` stream, the `--flow`, `--flow-args`, `--flow-exit` CLI flags supporting the k8s Job entrypoint pattern, and the `interactive: true` flow-step type. Interactive steps auto-bind the step's session via `bridge.Service.Bind`, fail fast when the bridge is not configured, and unbind on completion or abort. Models a one-flow-per-process semantics; `POST /flow/input` and `GET /flow/output` are explicitly not in scope.
## Requirements
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

The existing `GET /event` SSE stream SHALL carry these new event types: `flow.step.started`, `flow.step.completed`, `flow.step.failed`, `flow.step.retrying`, `flow.waiting_for_input`, `flow.completed`, `flow.failed`. Event payload schemas:

| Type | Payload fields |
|---|---|
| `flow.step.started`      | `runID, stepID, sessionID, startedAt` |
| `flow.step.completed`    | `runID, stepID, sessionID, output, startedAt, completedAt` |
| `flow.step.failed`       | `runID, stepID, error, startedAt, failedAt` |
| `flow.step.retrying`     | `runID, stepID, sessionID, output` (the reason) — the step is still `running` |
| `flow.waiting_for_input` | `runID, stepID, sessionID, target` (resolved `PeerRef` or array thereof) |
| `flow.completed`         | `runID, completedAt` (no output — orchestrator reads from session messages) |
| `flow.failed`            | `runID, error, failedAt` |

External orchestrators subscribing to `/event` MUST be able to combine these with `GET /flow/status` to catch up on any events emitted before the SSE connection was established (the existing message-broker semantics — events are not buffered for late subscribers).

#### Scenario: Step lifecycle emits started→completed pair

- **WHEN** an autonomous step runs to completion
- **THEN** the SSE stream emits `flow.step.started` then `flow.step.completed` for that step

#### Scenario: Schema-bearing step emits flow.step.retrying when it is re-prompted

- **WHEN** a step declaring `output.schema` ends a turn with no `struct_output` call and the flow runner spends its one bounded re-prompt (see the flow-runtime-resume capability)
- **THEN** the SSE stream emits `flow.step.retrying` with the step's `sessionID` and a reason in `output`
- **AND** the run's status is unchanged — the step is still in progress, so no `flow.step.completed` / `flow.step.failed` is emitted for it yet, and an interactive step keeps its `waiting_for_input` target in `GET /flow/status`

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

### Requirement: Flow files compose shared step definitions via include and extends

A flow file SHALL be able to declare `include:` at the top level, listing local
files that contribute reusable step templates, and a step SHALL be able to declare
`extends:` naming one or more of those templates. A template's keys seed the step;
the step's own keys override them. Composition MUST happen at load time before
validation, so that a merged step is validated exactly as an inline step is and a
template cannot introduce a step shape inline YAML could not.

A template is a top-level key whose name begins with `.` in an included file. Only
`local:` include entries are supported; a `remote:` or `project:` entry MUST be
rejected rather than ignored, so an unsupported reference fails loudly instead of
silently yielding an unresolved `extends`.

Merging MUST be shallow per top-level step key: a key present on the step replaces
the template's value wholly, including `output`, `rules` and `fallback`. With
several `extends` entries, templates MUST apply in declaration order with later
entries overriding earlier ones, and the step's own keys overriding all of them.

A template carries a step's **behaviour**, not its identity or its scheduling.
Concretely, a template MUST NOT declare `id`, `interactive`, `interaction` or
`resume_after`; each MUST be a load error naming the offending key. Every other step
key is inheritable — except `extends` itself, since templates are leaves.

A template key that is **not a known step field** MUST also be a load error, naming the
key and listing the real fields. Without that half of the rule a misspelled key such as
`promt:` is dropped silently by the typed decode, and in a template that loss is
multiplied across every flow extending it. A template is therefore deliberately
**stricter** than an inline step, which still tolerates an unknown key.

The reason is that the flow file is also parsed by the orchestrator — a separate
program that never resolves templates and reads a different subset of the schema.
`id` identifies the step in its task card and its postpone matching; `interactive`
and `interaction` decide reviewer-argument enrichment, so a template setting them
yields a job with nobody bound to answer; `resume_after` is read by the orchestrator to
compute a postpone-resume deadline, so a template setting it leaves the orchestrator
seeing no opt-in. Any key added to the flow schema later is
inheritable by default, and this list grows only when a specific key is shown to be
read by a second consumer.

A template MUST NOT itself declare `include` or `extends`, and neither MAY appear at an
included file's own top level — that rejects self-inclusion and rejects including a flow
file as if it were a template library. `local:` include paths MUST resolve against the
workspace root, as GitLab CI's do, so that one include line works from a flow at any
depth — including a team-hosted flow in its own directory; an absolute path MUST be
honoured as given. A `$ref`
inside a template MUST resolve relative to the **template** file, which is deliberately
a different rule and must be documented as such. The per-file size limit MUST apply to
each included file.

When two included files define a template of the same name, the later `include:` entry
MUST win and the collision MUST be logged — an author splitting one template across two
files would otherwise get silent shadowing.

Addendum — the `context` step field:

The `Step` struct gains a `context` field (`context: { paths, mode, nested }`), typed
as `Step.Context *contextfile.StepContext` — parallel to the `contextfile.AgentContext`
referenced by `config.Agent`. This field is **inheritable** via `extends`/`include`.
Concretely:

- `context` MUST NOT be listed in `nonInheritableStepKeys`
  (`internal/flow/include.go:182-187`). The orchestrator does not read the `context`
  field — it reads only `id`, `interactive`, `interaction`, and `resume_after`. A
  template that supplies `context` is safe: every flow that extends it inherits a
  consistent per-step context override, which is the correct and desired behavior.
- The reflection-driven merge in `mergeTemplates` (`internal/flow/include.go:259`)
  requires no code change for the new field: the merge iterates over declared YAML
  keys, and `context` is a standard top-level step key that overrides the template's
  `context` when the extending step declares its own.
- A template MAY declare `context` (it is not in `nonInheritableStepKeys`); an
  extending step that declares its own `context` key overrides the template's entirely
  (shallow merge, consistent with all other step fields).

Resolution of `step.Context` happens in `runStep` (`internal/flow/service.go:461`),
which passes the resolved `contextfile.StepContext` into agent construction (`NewAgent`
call at `service.go:513`) alongside the existing `agentID`, `outputSchema`, `step.ID`,
`step.Interactive`, and `boundPeers` arguments. The `${flow.id}` and `${flow.step}`
template tokens are populated explicitly from `f.ID` and `step.ID` at that call site —
`NewAgent` is context-free (`factory.go:207-208` discards its ctx), and the
`FlowIDContextKey` / `FlowStepIDContextKey` ctx values are set only later
(`service.go:654-656`) on the Run context for telemetry, so they cannot be the source
of the template tokens.

#### Scenario: A step inherits a template
- **WHEN** a flow includes a file defining `.resolve-team` with `agent` and `prompt`, and a step declares `extends: [".resolve-team"]` and no `agent` or `prompt`
- **THEN** the step runs with the template's agent and prompt, indistinguishable at execution from having declared them inline

#### Scenario: The step overrides an inherited key
- **WHEN** the template sets `maxTurns: 15` and the extending step sets `maxTurns: 20`
- **THEN** the step runs with `maxTurns: 20`, and every key it does not set keeps the template's value

#### Scenario: Overriding a block replaces it wholly
- **WHEN** the template defines an `output` block and the extending step defines its own `output`
- **THEN** the step's `output` is used unmerged, and no field of the template's block survives

#### Scenario: Several templates apply in order
- **WHEN** a step declares `extends: [".base", ".override"]` and both set `agent`
- **THEN** `.override`'s agent wins over `.base`'s, and a key set only by `.base` is still inherited

#### Scenario: A template declaring an orchestrator-read key is rejected
- **WHEN** a template declares `interactive: true`, `interaction`, `resume_after` or `id`
- **THEN** loading the flow fails with an error naming that key, because the orchestrator parses the same file without resolving templates and would see the key as absent

#### Scenario: A merged step is validated like an inline one
- **WHEN** a template supplies a `rules` entry naming a step the extending flow does not define
- **THEN** the flow fails validation with the existing unknown-rule-target error, because merging precedes validation

#### Scenario: An unknown template name is an error
- **WHEN** a step declares `extends: [".missing"]` and no included file defines it
- **THEN** loading fails naming the template, rather than running a step with an empty prompt

#### Scenario: A template may not nest composition
- **WHEN** an included file's template declares `include` or `extends`
- **THEN** loading fails, keeping the merge order one level deep and cycle-free

#### Scenario: Flows without include are unaffected
- **WHEN** a flow declares neither `include` nor `extends`
- **THEN** it loads and executes exactly as before, with no keys required by the include mechanism itself

#### Scenario: A declared prompt source is not merged with an inherited one
- **WHEN** a step declares `prompt` and its `extends` template declares `langfusePromptPath` (or the reverse)
- **THEN** the step inherits neither the competing key nor `langfusePromptLabel`, so the step's declared source replaces the template's rather than colliding on the exactly-one-source rule
- **AND WHEN** a step declares only `langfusePromptLabel`
- **THEN** it still inherits `langfusePromptPath` from its template, so a flow can run a shared prompt against a different label

#### Scenario: An include path is the same from any flow depth
- **WHEN** the same `include: - local: .agents/steps/x.yaml` line appears in a flow under `.agents/flows/` and in a team-hosted flow under `<team>/flows/`
- **THEN** both resolve to the same file, and a `$ref` inside that template resolves next to the TEMPLATE, not next to either flow

#### Scenario: An included file is subject to the size limit
- **WHEN** an included file exceeds the per-file size limit
- **THEN** loading fails naming that file, so `include` is not a way around the cap

#### Scenario: A misspelled template key is rejected
- **WHEN** a template declares a key that is not a step field, such as `promt:`
- **THEN** loading fails naming the key and listing the real fields, rather than dropping it silently as an inline step would

#### Scenario: A template supplies `context` and the extending step inherits it

- **WHEN** a step template declares `context: { paths: ["AGENTS.runtime.md"], mode: replace }`
  and a flow step `extends` that template without declaring its own `context`
- **THEN** the step runs with `AGENTS.runtime.md` as its only context file, identical
  to declaring `context` inline

#### Scenario: The extending step overrides the template's `context`

- **WHEN** the template declares `context: { paths: ["AGENTS.runtime.md"], mode: replace }`
  and the extending step declares `context: { paths: ["AGENTS.custom.md"], mode: append }`
- **THEN** the step runs with `AGENTS.custom.md` in `append` mode and the template's
  `context` is wholly replaced (shallow merge, consistent with `output`, `rules`, etc.)

#### Scenario: Flows without step `context` are unaffected

- **WHEN** a flow declares neither `context` on any step nor any `include` templates
  with `context`
- **THEN** it loads, validates, and executes exactly as before this change — no new
  required keys, no behavior change


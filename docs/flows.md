# Flows

Flows provide deterministic, multi-step agent workflows. A Flow is a YAML-defined directed graph of steps, each with its own agent, prompt template, optional structured output, routing rules, and fallback strategy.

When authoring or editing a flow, prefer the bundled **[flow-creator skill](../.agents/skills/flow-creator/SKILL.md)** — it encodes the YAML spec, design guidelines, common patterns (sequential pipeline, conditional branching, parallel fan-out, postponed and in-process self-loops, interactive human-in-the-loop steps), and the available built-in agents, so the LLM produces a valid file on the first try.

Flows run in two modes — see [Execution Modes](#execution-modes):

- **Direct CLI Mode** — `opencode -F <flow-id>` runs the flow once, prints the JSON envelope, exits.
- **Server Mode** — `opencode serve` exposes HTTP and SSE so an external orchestrator (k8s Job, c2-agent, OpenWork, etc.) can start, observe, and abort flows over the wire. Also supports `--flow` auto-start as a Job entrypoint pattern.

## Quick Start

Create a flow file in your project:

```yaml
# .opencode/flows/review-and-fix.yaml
name: Review and Fix
description: Reviews code and optionally fixes issues
flow:
  steps:
    - id: review
      agent: explorer
      prompt: |
        Review the following for issues:
        ${args.prompt}
      output:
        schema:
          type: object
          properties:
            has_issues:
              type: boolean
            summary:
              type: string
          required: [has_issues, summary]
      rules:
        - if: "${args.has_issues} == true"
          then: fix-issues

    - id: fix-issues
      agent: coder
      prompt: |
        Fix the issues found in the review:
        ${args.summary}
```

Run it:

```bash
opencode -F review-and-fix -p "Check src/main.go for bugs"
```

## Flow Definition Format

Flow files are YAML and are discovered from these locations (project paths take priority):

| Location | Scope |
|----------|-------|
| `.opencode/flows/*.yaml` | Project |
| `.agents/flows/*.yaml` | Project |
| `~/.config/opencode/flows/*.yaml` | Global |
| `~/.agents/flows/*.yaml` | Global |

The flow ID is derived from the filename without its extension. For example, `review-and-fix.yaml` becomes the flow ID `review-and-fix`. Both `.yaml` and `.yml` extensions are supported.

### Top-level fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | No | Display name |
| `disabled` | bool | No | If true, flow cannot be executed |
| `description` | string | No | Description of the flow |
| `flow` | object | Yes | Flow specification |

### Flow specification

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `flow.args` | object | No | JSON Schema for expected arguments |
| `flow.session` | object | No | Session configuration (see [Session Management](#session-management)) |
| `flow.steps` | array | Yes | Ordered list of step definitions |

### Step fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | Yes | Unique step identifier (kebab-case, max 64 chars) |
| `agent` | string | No | Agent ID to use (defaults to `coder`) |
| `session.fork` | bool | No | Fork previous step's session (same agent only) |
| `prompt` | string | Yes | Prompt template with `${args.*}` and `${step.*}` placeholders |
| `output.schema` | object | No | JSON Schema for structured output |
| `rules` | array | No | Conditional routing rules |
| `fallback` | object | No | Retry and error routing |
| `maxTurns` | int | No | Per-step override for the agent's `maxTurns`. `0` (unset) inherits from the agent. |
| `maxIterations` | int | No | Cap on in-process self-loop iterations. `0` (unset) is unbounded — only the flow timeout applies. When the (N+1)th self-route would exceed the cap, the step fails (and runs its `fallback`). See [Self-Loops](#self-loops). |

### Rules

```yaml
rules:
  - if: "${args.status} == READY"
    then: implement
  - if: "${args.status} != READY"
    then: skip
```

A rule with no `if` field is an **unconditional transition** — it always matches and advances to the named step:

```yaml
rules:
  - then: next-step    # unconditional — always advances
```

**Steps without rules are terminal.** When a step has no `rules` array, the branch stops there. If you want linear flow progression, declare it explicitly with an unconditional rule.

Supported operators:

| Operator | Description |
|----------|-------------|
| `==` | Equality |
| `!=` | Inequality |
| `=~` | Regex match (pattern delimited by `/`) |

Predicates can reference two scopes:

- `${args.X}` — the flow's accumulated args (structured outputs from prior steps merge in here). A missing key evaluates the predicate to false (no error).
- `${step.X}` — step-scoped variables. Currently `${step.iteration}` (1-based, incremented on each self-route). Unknown `step.` keys are flow-author bugs and produce an error rather than silently matching false. Step variables are **not** stored on `args` and never persisted.

The `sizeof` prefix works on both scopes (`sizeof ${args.items} != 0`, `sizeof ${step.iteration} == 1`).

Multiple atoms can be composed with `&&` (logical AND) and `||` (logical OR), grouped with parentheses. `&&` binds tighter than `||`; evaluation is short-circuited.

```yaml
rules:
  - if: sizeof ${args.blockers} != 0
    then: failed
  - if: sizeof ${args.blockers} == 0 && ${args.build_service_snapshots} == true
    then: build-service-snapshots
  - if: sizeof ${args.blockers} == 0 && ${args.build_service_snapshots} != true && ${args.trigger_review} == true
    then: trigger-review
  - if: (${args.env} == prod || ${args.env} == staging) && ${args.deploy} == true
    then: deploy
```

Composite operators inside `${...}` placeholders and `/.../` regex literals are not interpreted (so `${args.x} =~ /IMPL|REVIEW/` works). Literal `&&` / `||` / `(` / `)` in the right-hand-side of `==` / `!=` are not currently supported.

When multiple rules match, the corresponding steps execute in parallel.

#### Rule fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `if` | string | No | Predicate expression to evaluate. Omit for an unconditional transition. |
| `then` | string | Yes | Step ID to route to when predicate matches |
| `postpone` | bool | No | If true, store the target step as postponed instead of running it immediately |

### Fallback

```yaml
fallback:
  retry: 3
  delay: 10
  to: error-handler
```

| Field | Type | Description |
|-------|------|-------------|
| `retry` | int | Number of retry attempts |
| `delay` | int | Delay between retries (seconds) |
| `to` | string | Step ID to route to after all retries fail |

## Execution Modes

Flows can be executed in two ways. Both modes use the same flow YAML and engine — only the entrypoint, output channel, and lifecycle differ.

| | Direct CLI | Server |
|---|---|---|
| Entry | `opencode -F <flow-id>` | `opencode serve` + HTTP, or `opencode serve --flow <id>` |
| Output | Single JSON envelope on stdout, then process exits | SSE events on `/event`, snapshot on `/flow/status` |
| Concurrency | One flow per invocation | One flow at a time per process (POST `/flow` returns 409 otherwise) |
| Best for | Local one-shots, CI pipelines that want stdout output | Long-running interactive flows, k8s Jobs with external reconcilers, OpenWork/c2-agent integration |
| Auto-exit | Always — the process exits when the flow terminates | Opt-in via `--flow-exit` (plus optional `--flow-exit-grace`) |
| Interactive steps | Not supported (chat bridge isn't started) | Supported when `router` is configured — see [Bridge docs](bridge.md) |

### Direct CLI Mode

```bash
# Basic flow execution
opencode -F <flow-id> -p "<prompt>"

# With a session prefix (enables resumption)
opencode -F my-flow -s my-prefix -p "do the thing"

# With extra arguments
opencode -F my-flow -p "PROJ-1234" -A "priority=high" -A "team=backend"

# With arguments from a JSON file
opencode -F my-flow -p "PROJ-1234" --args-file flow-args.json

# Fresh start (delete previous state)
opencode -F my-flow -s my-prefix -D -p "restart"
```

#### CLI flags

| Flag | Short | Description |
|------|-------|-------------|
| `--flow` | `-F` | Flow ID to execute |
| `--arg` | `-A` | Flow argument as `key=value` (repeatable) |
| `--args-file` | | JSON file with flow arguments |
| `--prompt` | `-p` | Initial prompt (optional with `--flow`, added to args) |
| `--session` | `-s` | Session prefix for deterministic naming |
| `--delete` | `-D` | Delete previous state and start fresh |

#### CLI output

Direct CLI mode prints a single JSON envelope to stdout when the flow terminates and exits — see [JSON envelope](#json-envelope).

### Server Mode

Run `opencode serve` to expose the flow engine over HTTP. External callers (orchestrators, dashboards, k8s controllers) start and observe flows via REST + SSE. See [`docs/server.md`](server.md) for the general server flags and authentication.

#### Server-only `--flow*` flags

These are honored by `opencode serve` only:

| Flag | Default | Description |
|------|---------|-------------|
| `--flow` | _(unset)_ | Auto-start the named flow once the server is healthy (k8s Job entrypoint pattern). |
| `--flow-args` | _(unset)_ | Path to a JSON file with flow arguments (e.g. reviewers, ticket IDs). Read once at start. |
| `--flow-fresh` | `false` | Discard any existing per-step session state when auto-starting (equivalent to `-D` in direct mode). |
| `--flow-exit` | `false` | Cancel the parent context (shutting the server down) once the auto-started flow terminates. |
| `--flow-exit-grace` | `5s` | Hold the HTTP server up this long after the flow terminates so an external reconciler (`GET /flow/status`) can land before shutdown. Capped at 60 s. Only honored with `--flow-exit`. Set to `0s` to exit immediately. |

`--flow-exit-grace` exists for the k8s pattern where an external controller polls `GET /flow/status` after seeing the pod transition to `Succeeded` — without the grace the server may shut down before the reconciliation read lands. A SIGTERM during the grace short-circuits the wait.

> **Behaviour change.** Prior to the grace flag, `--flow-exit` exited immediately. Existing deployments that relied on immediate exit must now pass `--flow-exit-grace=0s`.

#### HTTP endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/flow` | List every discovered flow YAML (`{id, name, description, disabled, args}`). |
| POST | `/flow` | Start a new run. Body: `{flowID, args, fresh}`. Returns `202 Accepted` with `{runID, flowID, status, currentStep}`, or `409` if another run is in flight, or `404` for an unknown flow ID. |
| GET | `/flow/status` | Snapshot of the latest run: `{runID, flowID, status, startedAt, completedAt, currentStep, completedSteps, waitingTarget, error}`, or `{"status":"idle"}` if no run has been started in this process. |
| DELETE | `/flow` | Abort the in-flight run. `409` if no run is active. |

`status` values mirror the flow-api spec: `running`, `waiting_for_input`, `completed`, `failed`.

#### SSE events on `/event`

Server mode multiplexes flow lifecycle events onto the existing `/event` SSE stream (the same stream that carries `message.*` / `session.*` events — see [`docs/server.md`](server.md#events-sse)). Each event is a `FlowEvent` JSON payload:

```json
{
  "type": "flow.step.completed",
  "runID": "8f3a…",
  "stepID": "review",
  "sessionID": "1700000000-review-and-fix-review",
  "output": "{\"has_issues\":false}",
  "completedAt": 1780000000123,
  "isStructOutput": true,
  "iteration": 1,
  "cost": 0.0042,
  "contextSize": 12345
}
```

| `type` | When emitted | Notable fields |
|---|---|---|
| `flow.step.started` | A step enters `running`. | `stepID`, `sessionID`, `startedAt`, plus the per-step fields below. |
| `flow.step.completed` | A step finishes successfully. | `stepID`, `output`, `completedAt`, per-step fields. |
| `flow.step.failed` | A step exhausted retries / hit `maxIterations` / errored. | `stepID`, `error`, `failedAt`, per-step fields. |
| `flow.waiting_for_input` | An `interactive: true` step bound to its peer(s) and is awaiting reviewer reply. | `stepID`, `sessionID`, `target` (resolved PeerRef or array). |
| `flow.completed` | The run terminated successfully. | `runID`, `completedAt`. |
| `flow.failed` | The run failed. | `runID`, `error`, `failedAt`. |

Per-step fields (present on `flow.step.*` and `flow.waiting_for_input`):

- `isStructOutput` — `true` when the step produced a JSON `struct_output`, mirrored from `flow.FlowState.IsStructOutput`. Orchestrators can render struct vs free-text blocks differently.
- `iteration` — 1-based self-loop iteration number, mirrored from `flow.FlowState.Iteration`. `1` for non-looping steps; bumps on every in-process self-route.
- `cost` — cumulative session cost (USD) at emit time, looked up via `session.Service.Get(state.SessionID).Cost`. Zero on session-lookup failure.
- `contextSize` — cumulative prompt tokens for the session at emit time. Zero on lookup failure.

All four use `omitempty` — older consumers that don't read them ignore the keys.

#### Example: k8s Job entrypoint

```bash
# Job spec command — auto-start a flow with reviewer PeerRefs from a mounted ConfigMap.
opencode serve \
  --hostname 0.0.0.0 \
  --port 4096 \
  --flow review-and-fix \
  --flow-args /etc/flow-args/args.json \
  --flow-exit \
  --flow-exit-grace 10s
```

The Job's controller polls `GET /flow/status` and reads the final snapshot before the pod shuts down.

#### Example: start a flow over HTTP

```bash
# Start
curl -X POST http://127.0.0.1:4096/flow \
  -H 'content-type: application/json' \
  -d '{"flowID":"review-and-fix","args":{"prompt":"Check src/main.go for bugs"}}'
#  -> 202 {"runID":"8f3a…","flowID":"review-and-fix","status":"running","currentStep":null}

# Tail events
curl -N http://127.0.0.1:4096/event

# Snapshot
curl http://127.0.0.1:4096/flow/status

# Abort
curl -X DELETE http://127.0.0.1:4096/flow
```

#### Interactive steps

When the chat bridge is configured (a `router` section in `.opencode.json` — see [`docs/bridge.md`](bridge.md)), flow steps can be marked `interactive: true` with an `interaction.target` PeerRef. The flow engine auto-binds the step session to the resolved peer(s), `agent.Run` fans out to the bound peer(s), reviewer replies route back over the bridge, and the step completes when the agent emits `struct_output`. Server mode emits `flow.waiting_for_input` on `/event` so orchestrators can render a "waiting on reviewer" indicator without polling. See the flow-creator skill's [Interactive Step section](../.agents/skills/flow-creator/SKILL.md#interactive-step-human-in-the-loop-via-chat-bridge) for YAML syntax and supported channels.

If `router` is unconfigured, interactive steps fail-fast on bind with `flow.ErrInteractiveBridgeDisabled` — they cannot be used in Direct CLI Mode.

## JSON envelope

Direct CLI Mode prints this envelope to stdout when the flow terminates. (Server Mode does not emit this envelope — consumers reconstruct equivalent information from `flow.step.*` and `flow.completed`/`flow.failed` SSE events, or call `GET /flow/status` for a final snapshot.)

## Template Substitution

Prompts support `${args.*}` and `${step.*}` placeholders:

- `${args.prompt}` — Value of the `prompt` argument
- `${args.key}` — Value of any argument by key
- `${args}` — Full JSON dump of all arguments
- `${step.iteration}` — Current iteration of the step (1-based, increments per self-route). Always available; equals `1` for non-looping steps.

Step-scoped variables are substituted first so they cannot be shadowed by args of the same name.

Arguments accumulate as the flow progresses. When a step produces structured output, its fields are merged into the args map for subsequent steps. `${step.*}` values are **not** merged into args — they exist only for rendering/predicates and do not leak into downstream steps.

## Session Management

Each step creates a session with a deterministic ID:

```
<prefix>-<flow-id>-<step-id>
```

This enables:

- **Resumption**: Re-running with the same prefix reuses existing sessions
- **Fresh start**: Adding `-D` deletes all previous sessions and state
- **Inspection**: Session IDs are predictable and can be queried

### Session prefix resolution

The session prefix is chosen using the following priority (highest first):

1. **CLI flag** `--session` / `-s` — always wins when provided
2. **Flow spec** `flow.session.prefix` — used when no CLI flag is given
3. **Fallback** — a Unix timestamp, making each invocation independent

The `flow.session.prefix` field accepts either a literal string or an `${args.*}` reference:

```yaml
# Literal constant
flow:
  session:
    prefix: my_static_id
  steps: [...]

# Value from flow args
flow:
  session:
    prefix: ${args.jira_issue_id}
  steps: [...]
```

When `prefix` references an arg variable (e.g. `${args.jira_issue_id}`), the variable must exist in the provided args or the flow will return an error.

A `--session` flag on the CLI always overrides the spec value:

```bash
# Uses "override" as prefix, ignoring whatever flow.session.prefix says
opencode -F my-flow -s override -p "do the thing"
```

### Re-trigger semantics

When `Run` is invoked for a `(prefix, flow_id)` pair that already has `flow_states` rows, the runtime decides between **resume** (continue prior mid-state) and **restart** (re-execute from step 0). The decision is governed by a "resumable work" predicate over the prior rows that folds two checks:

- **Status-driven** — any row in `running` / `postponed` / `waiting_for_input` short-circuits to resume. `failed` is opt-in via `resume_on_failure` (see below); otherwise it counts as terminal.
- **Rule-walk-driven** — for completed rows, the runtime re-evaluates the step's routing rules using the row's persisted args and iteration. If any rule still points at the same step (self-route — the next iteration was never scheduled, e.g. a crash between iter-N-completed and iter-N+1-running) or at a step that hasn't reached terminal, the runtime resumes.

If neither check fires, the prior run terminated cleanly and the runtime **restarts** from step 0. Per-step sessions are preserved on restart, so the agent retains cumulative LLM history across re-triggers. This is the "react on external event" case — a flow keyed by `${args.jira_issue_id}` re-fires when the Jira issue changes and the new comment must be re-evaluated, with the prior conversation still visible.

`--flow-fresh` / `-D` / `{fresh: true}` is the hard reset: deletes both `flow_states` rows AND the per-step session tree, then runs from step 0 with empty LLM history. This is the only path that touches per-step sessions.

> Full contract in [`openspec/specs/flow-runtime-resume/spec.md`](../openspec/specs/flow-runtime-resume/spec.md).

#### `resume_on_failure`

By default a `failed` flow_states row is terminal — the next re-trigger restarts from step 0. Flows that want retry-from-failure semantics (long pipelines where re-doing prior expensive steps after a transient failure is wasteful) can opt in:

```yaml
flow:
  session:
    prefix: ${args.build_id}
    resume_on_failure: true
  steps:
    - id: download-artifacts
      prompt: "..."
    - id: build
      prompt: "..."
    - id: publish
      prompt: "..."
```

With `resume_on_failure: true`, a re-trigger of a run whose latest row is `failed` continues from that failed step instead of restarting. Step args are restored from the failed row, so the retry sees the same inputs the previous attempt had.

Default `false` matches the React-on-event use case: a re-trigger after failure means "the world may have changed, re-evaluate from step 0," not "retry the same step."

Unknown keys inside `session:` (e.g. a typo `resume_on_fail` missing the trailing `ure`) are rejected at flow load time with `ErrInvalidYAML`, naming the offending key so the author can fix the config.

```json
{
  "flow_id": "my-flow",
  "steps": [
    {
      "step_id": "review",
      "session_id": "prefix-my-flow-review",
      "status": "completed",
      "iteration": 3,
      "output": "...",
      "is_struct_output": true,
      "finished_at": 1780000000,
      "context_size": 12345,
      "cost": 0.0021
    }
  ],
  "metrics": {
    "cost": 0.0021,
    "gauge": 12345
  }
}
```

The `steps` array is in completion order; each entry's `status` is its terminal state (`completed`, `failed`, or `postponed`). `metrics.cost` is the flow-wide total; `metrics.gauge` is wall-clock duration in milliseconds.

The envelope contains exactly **one entry per step ID**, even when the step iterated. The latest published state wins:

- `iteration` is the count of how many times the step ran before reaching its terminal state (1-based; `1` for non-looping steps).
- `status` is the terminal status — `completed` if the loop exited cleanly, `failed` if a `maxIterations` cap (or any other error) tripped, `postponed` if the loop suspended itself.
- `output` is the structured output (or text) from the **last** iteration.
- `cost` and `context_size` are session-level totals — because all iterations share one session, these aggregate automatically across the whole loop.

The intermediate iterations are not surfaced in the JSON envelope. To inspect them, query the session (its messages span all iterations) or Langfuse (one trace per iteration, distinguishable by the `#N` suffix in the trace name).

## Naming Rules

Flow IDs and step IDs must be kebab-case:

- `review-and-fix` ✓
- `analyse-issue` ✓
- `step1` ✓
- `ReviewAndFix` ✗ (uppercase)
- `review_and_fix` ✗ (underscore)
- `-review` ✗ (starts with hyphen)

Maximum length is 64 characters for both flow IDs and step IDs.

## Examples

### Multi-step analysis flow

```yaml
name: Deep Analysis
description: Analyse, plan, and implement
flow:
  steps:
    - id: analyse
      agent: explorer
      prompt: |
        Analyse the codebase and determine what needs to change for:
        ${args.prompt}
      output:
        schema:
          type: object
          properties:
            plan:
              type: string
            complexity:
              type: string
              enum: [low, medium, high]
          required: [plan, complexity]
      rules:
        - if: "${args.complexity} == high"
          then: detailed-plan
        - if: "${args.complexity} != high"
          then: implement

    - id: detailed-plan
      agent: explorer
      prompt: |
        Create a detailed implementation plan:
        ${args.plan}
      rules:
        - if: "${args.plan} =~ /.+/"
          then: implement

    - id: implement
      agent: workhorse
      prompt: |
        Implement the following plan:
        ${args.plan}
      fallback:
        retry: 2
        delay: 5
```

### Flow with parallel branches

```yaml
name: Parallel Review
description: Run multiple reviews in parallel
flow:
  steps:
    - id: triage
      prompt: |
        Classify this issue: ${args.prompt}
      output:
        schema:
          type: object
          properties:
            needs_security_review:
              type: boolean
            needs_perf_review:
              type: boolean
          required: [needs_security_review, needs_perf_review]
      rules:
        - if: "${args.needs_security_review} == true"
          then: security-review
        - if: "${args.needs_perf_review} == true"
          then: perf-review

    - id: security-review
      agent: explorer
      prompt: "Review for security issues: ${args.prompt}"

    - id: perf-review
      agent: explorer
      prompt: "Review for performance issues: ${args.prompt}"
```

### Flow with error handling

```yaml
name: Safe Deploy
description: Deploy with automatic rollback on failure
flow:
  steps:
    - id: deploy
      agent: workhorse
      prompt: |
        Deploy the changes described here:
        ${args.prompt}
      fallback:
        retry: 2
        delay: 30
        to: rollback

    - id: rollback
      agent: workhorse
      prompt: |
        The deployment failed. Roll back to the previous stable state.
        Original task: ${args.prompt}
```

## Behaviour Notes

### Terminal steps

A step with no `rules` is terminal — the branch stops when it completes. To advance to the next step unconditionally, add a rule with only a `then` field:

```yaml
rules:
  - then: next-step
```

### Parallel execution

When multiple rules on a single step evaluate to true, all matching successor steps run concurrently. Each fork receives its own copy of the accumulated args, so parallel branches cannot interfere with each other.

### Diamond convergence

If two parallel branches both route to the same step (A→B, A→C, B→D, C→D), the first branch to arrive runs step D. The second branch detects that step D is already running and skips it. Step D executes exactly once with the args from whichever branch arrived first.

**Self-loops are exempt** from this guard. A step that routes back to itself (via a rule whose `then` names the step itself) re-enters intentionally — see [Self-Loops](#self-loops) below. The guard only applies when the route comes from a different step.

### Session forking

When `session.fork: true` is set on a step, the step's session is created by copying the message history from the previous step's session. This only works when both steps use the same agent — if the agents differ, a fresh session is created instead and the previous step's output is still prepended to the prompt.

### Running state guard

If a flow invocation finds steps in `running` status from a previous interrupted run, it returns the existing states without invoking any agents. Use `-D` to force a fresh start.

### Postponed steps

A rule can set `postpone: true` to defer a step's execution until the next flow invocation. When a postponed rule matches, the target step is stored with status `postponed` instead of being run immediately. On the next invocation (with the same session prefix), the postponed step is picked up and executed normally.

This is useful when a step discovers a blocker that requires external action (e.g., user input, approval, external service). The step can output the blocker information, and a rule with `postpone: true` routes back to a check step. On re-invocation the check step re-evaluates and either proceeds or postpones again.

```yaml
steps:
  - id: check
    agent: explorer
    prompt: |
      Check if blockers are resolved: ${args.blockers}
      Do work which could create blockers...
    output:
      schema:
        type: object
        properties:
          blockers:
            type: array
            items:
              type: string
        required: [blockers]
    rules:
      - if: sizeof ${args.blockers} != 0
        then: check
        postpone: true
      - if: sizeof ${args.blockers} == 0
        then: implement

  - id: implement
    prompt: |
      Implement the changes now that all blockers are resolved.
      ${args.prompt}
```

### Self-loops

A step can route back to itself to iterate over a workload that doesn't fit in a single agent turn — for example, building libraries level-by-level, polling an external job, or scanning a paginated source.

```yaml
- id: build-level
  agent: coder
  prompt: |
    Build libraries at level ${args.current_level}. Iteration ${step.iteration}.
    Current state: ${args.snapshot_versions}
  output:
    schema:
      type: object
      properties:
        current_level:
          type: integer
        has_more_levels:
          type: boolean
        snapshot_versions:
          type: object
      required: [current_level, has_more_levels, snapshot_versions]
  maxIterations: 20  # safety cap
  rules:
    - if: ${args.has_more_levels} == true
      then: build-level   # self-route
    - if: ${args.has_more_levels} != true
      then: publish
```

Two modes for self-routing:

- **In-process** (`postpone` omitted or `false`) — the next iteration runs in the same flow invocation, immediately after the current one completes. Counts against the flow timeout.
- **Postponed** (`postpone: true`) — the row is marked `postponed`; the step does not re-enter the agent loop. The next iteration runs only when the flow is re-invoked with the same session prefix. Use this when iteration progress depends on external state changing between runs.

Both modes reuse the same step session, so the agent has memory of prior iterations (and the session's cost/tokens aggregate naturally). `${step.iteration}` is 1-based and survives postpone → resume via the `flow_states.iteration` column.

Caveats to design around:

- **Args persist between iterations.** Fields the agent omits from one iteration's structured output stay at the value the prior iteration set. List those fields under `required:` on the output schema — the model's structured-output API enforces required keys, so each iteration is forced to emit the freshly-computed value (e.g. `has_more_levels`). The flow runner itself does not re-validate the output beyond what the schema enforces on the model.
- **Always cap with `maxIterations`** when the termination condition comes from the agent. If the predicate has a bug, an uncapped loop burns through the flow timeout. The cap fires the step's `fallback` so you can route to a clean failure handler. Two notes on cap semantics:
  - The cap counts **in-process** iterations only. A `postpone: true` self-route does not bump the iteration counter, so a postpone loop is not bounded by `maxIterations` — each invocation runs the postponed step once before pausing. Bound those externally (e.g. orchestrator-level retry limits).
  - The cap is a **post-step check**: with `maxIterations: N`, exactly N agent calls happen — iter N's pre-check sees that iter N+1 would exceed the cap and fails the step. If you want a strict token budget below the agent's cost-per-iteration, set the cap one below the desired hard limit.
- **Diamond and self-loop are independent.** A self-loop bypasses the diamond-convergence guard, but a normal (non-self) route into a step still runs at most once per invocation.

## See Also

- [flow-creator skill](../.agents/skills/flow-creator/SKILL.md) — bundled skill for authoring flow YAML
- [Server & ACP Mode](server.md) — general HTTP server flags, auth, and endpoint catalog
- [Chat Bridge](bridge.md) — required for `interactive: true` steps
- [Custom Commands](custom-commands.md)
- [Skills](skills.md)
- [Session Providers](session-providers.md)
- [Structured Output](structured-output.md)

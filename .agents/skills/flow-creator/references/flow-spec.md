# Flow Specification Reference

## File Locations (project paths take priority)

- `.opencode/flows/*.yaml` (project)
- `.agents/flows/*.yaml` (project)
- `~/.config/opencode/flows/*.yaml` (global)
- `~/.agents/flows/*.yaml` (global)

Flow ID is derived from the filename without extension. Both `.yaml` and `.yml` are supported.

## Top-level Fields

```yaml
name: string        # display name (optional)
disabled: bool      # if true, flow cannot be executed (optional)
description: string # description of the flow (optional)
flow:               # flow specification (required)
  args: object      # JSON Schema for expected arguments (optional)
  session: object   # session configuration (optional)
  steps: array      # ordered list of step definitions (required)
```

## Step Fields

```yaml
- id: string             # unique, kebab-case, max 64 chars (required)
  agent: string          # agent ID, defaults to "coder" (optional)
  session:
    fork: bool           # copy message history from previous step, same agent only (optional)
  prompt: string         # prompt template with ${args.*} and ${step.*} placeholders (required)
  output:
    schema: object       # JSON Schema for structured output (optional)
  rules: array           # conditional routing rules (optional)
  fallback: object       # retry and error routing (optional)
  maxTurns: int          # per-step override for agent's maxTurns. 0 (unset) inherits from agent. (optional)
  maxIterations: int     # cap on in-process self-loop iterations. 0 (unset) is unbounded — only flow timeout applies. (optional)
```

## Rules

```yaml
rules:
  - if: "${args.status} == READY"   # predicate expression (required)
    then: step-id                    # target step ID (required)
    postpone: bool                   # defer until next invocation (optional)
```

Operators: `==` (equality), `!=` (inequality), `=~` (regex, pattern delimited by `/`).
Prefix operator: `sizeof` for arrays/objects, e.g. `sizeof ${args.items} != 0`.
Boolean composition: `&&` (AND), `||` (OR), parenthesised groups. `&&` binds tighter than `||`; evaluation short-circuits. Example: `sizeof ${args.blockers} == 0 && ${args.deploy} == true`.

Predicates can reference two scopes:
- `${args.X}` — flow args. Missing key → predicate is false (silent, no error).
- `${step.X}` — step-scoped variables. Currently only `${step.iteration}` (1-based, increments per self-route). Unknown `step.` keys are errors (closed namespace).

A rule with no `if` field is an **unconditional transition** — it always matches and advances to the named step:

```yaml
rules:
  - then: next-step    # unconditional — always advances
```

When multiple rules match, all matching steps execute in parallel.

## Fallback

```yaml
fallback:
  retry: 3      # number of retry attempts (int)
  delay: 10     # seconds between retries (int)
  to: step-id   # step to route to after all retries fail (string)
```

## Template Substitution

- `${args.prompt}` expands to the value of the `prompt` argument.
- `${args.key}` expands to any argument by key.
- `${args}` dumps all arguments as formatted JSON.
- `${step.iteration}` expands to the step's current iteration (1-based). Always available; equals `1` for non-looping steps. Step-scoped — never merged into args, never persisted.
- Arguments accumulate: structured output fields merge into args for subsequent steps. `${step.*}` does NOT accumulate or leak into downstream steps.
- Step-scoped variables are substituted before args, so they cannot be shadowed by an args key of the same name.

## Session Management

Each step creates a session with deterministic ID: `<prefix>-<flow-id>-<step-id>`.

Session prefix resolution (highest priority first):
1. CLI flag `--session` / `-s`
2. `flow.session.prefix` (literal or `${args.*}` reference)
3. Unix timestamp fallback

## CLI Usage

```bash
opencode -F <flow-id> -p "<prompt>"
opencode -F my-flow -s my-prefix -p "do the thing"
opencode -F my-flow -p "PROJ-1234" -A "priority=high" -A "team=backend"
opencode -F my-flow -p "PROJ-1234" --args-file flow-args.json
opencode -F my-flow -s my-prefix -D -p "restart"   # fresh start
```

## Naming Rules

Flow IDs and step IDs must be kebab-case, max 64 characters.

Valid: `review-and-fix`, `analyse-issue`, `step1`
Invalid: `ReviewAndFix`, `review_and_fix`, `-review`

## Behaviour Notes

### Steps without rules are terminal
When a step has no `rules` array, the branch stops there. If you want linear flow progression, declare it explicitly with an unconditional rule.

### Parallel Execution
When multiple rules on a single step evaluate to true, all matching successor steps run concurrently. Each fork receives its own copy of accumulated args.

### Diamond Convergence
If two parallel branches route to the same step, the first arrival runs it. The second branch skips it. Step executes exactly once with the first branch's args.

**Self-loops are exempt** — a step routing back to itself re-enters intentionally. The guard only applies to routes coming from a different step. See [Self-Loops](#self-loops).

### Session Forking
`session.fork: true` copies message history from the previous step's session. Only works when both steps use the same agent. If agents differ, a fresh session is created and the previous step's output is prepended to the prompt instead.

### Running State Guard
If a flow invocation finds steps in `running` status from a previous interrupted run, it returns existing states without invoking agents. Use `-D` to force a fresh start.

### Postponed Steps
`postpone: true` on a rule defers the target step until the next flow invocation with the same session prefix. Useful for blockers requiring external action. On re-invocation, the postponed step is picked up normally.

### Self-Loops
A step may route back to itself in two modes:

- **In-process** (`postpone` omitted/false): the next iteration runs immediately within the same flow invocation. Counts against the flow timeout. Use when the loop's progress is determined inside the flow (e.g. iterating over levels of a build dependency graph).
- **Postponed** (`postpone: true`): the row is marked `postponed`. The next iteration runs only when the flow is re-invoked. Use when iteration progress depends on external state changing between runs (blockers cleared, external job finished).

Both modes reuse the same step session, so the agent has memory of prior iterations and session cost/tokens aggregate naturally. `${step.iteration}` is 1-based and survives postpone → resume via `flow_states.iteration`.

Cap unconditional self-loops with `maxIterations` — if the agent's termination predicate has a bug, an uncapped loop burns through the flow timeout. When the cap trips, the step fails (and its `fallback`, if any, runs).

Cap semantics: counts **in-process** iterations only — a `postpone: true` self-route does not bump the counter, so a postpone loop is not bounded by `maxIterations` (bound those externally). The cap is a **post-step check** — with `maxIterations: N`, exactly N agent calls happen before the step fails.

```yaml
- id: build-level
  agent: coder
  prompt: |
    Building libraries at level ${args.current_level} (iteration ${step.iteration}).
  output:
    schema:
      type: object
      properties:
        current_level:
          type: integer
        has_more_levels:
          type: boolean
      required: [current_level, has_more_levels]
  maxIterations: 20
  rules:
    - if: ${args.has_more_levels} == true
      then: build-level
    - if: ${args.has_more_levels} != true
      then: publish
```

**Stale-arg footgun**: args accumulate, so fields the agent omits from one iteration's output keep their value from the prior iteration. List those fields under `required:` on the output schema — the model's structured-output API enforces required keys, so each iteration is forced to emit the recomputed value.

### Free-form Output Prepending
When a step has no structured output schema, its text output is automatically prepended to the next step's prompt as "Previous step (step-id) output: ...". When structured output is used, fields merge into args instead and the agent accesses them via `${args.*}`.

### Default Agent
When `agent` is omitted, the step runs with the `coder` agent.

### Built-in Agents
- `coder` — full tool access, default for all steps.
- `hivemind` — supervisory agent, can read and delegate via `task` but cannot edit files or run bash.
- `explorer` — read-only codebase exploration, no bash, no editing, no subagent spawning.
- `workhorse` — autonomous coder with full tools except `task` and `websearch`.
- `summarizer` — no tools at all, text-only output. Use for terminal summary/failure steps.
- `descriptor` — no tools, generates short titles. Internal/hidden.

Custom agents can be defined in `.agents/types/<name>.md` or `.opencode/agents/<name>.md`.

### Terminal Steps and Output
Steps that are terminal (no rules, no downstream consumers) should omit the `output` schema so their free-form text is displayed directly to the user. Prefer `summarizer` agent for such steps when only a textual summary is needed.

### Args Validation
If `flow.args` is defined, provided arguments are validated against the JSON Schema at invocation time. The `prompt` key is always allowed regardless of schema. Required fields are enforced, types are checked.

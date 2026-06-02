---
name: flow-creator
description: >
  Create and edit OpenCode flow YAML files that define deterministic, multi-step agent workflows.
  Use when the user asks to create a new flow, build an automation pipeline, design a multi-step
  agent workflow, convert a process description into a flow, or edit an existing flow definition.
  Also use when the user asks about flow syntax, step routing, structured output schemas,
  session management, parallel branching, fallback strategies, or postponed steps in the context
  of OpenCode flows.
---

# Flow Creator

## Workflow

1. Clarify the user's desired workflow: what steps, what agents, what decisions, what inputs.
2. Read `references/flow-spec.md` for the complete flow YAML specification.
3. Design the flow structure: identify steps, routing logic, output schemas, and fallback needs.
4. Write the flow YAML file to `.agents/flows/<flow-id>.yaml` (or `.opencode/flows/` if the user prefers).
5. Validate the generated YAML mentally against the spec constraints before delivering.

## Design Guidelines

Prefer structured output when a step's result drives routing rules or feeds data to downstream steps. Use free-form output only for terminal steps or when the output is purely informational.

Keep step prompts focused on a single responsibility. If a prompt grows beyond 30 lines, consider splitting into multiple steps connected by sequential flow or rules.

Use `sizeof` prefix operator for array/object emptiness checks in rules rather than string comparison.

Use `session.fork: true` when consecutive steps with the same agent benefit from shared conversation history. Do not set fork when agents differ because the engine ignores it and creates a fresh session anyway.

Always define a fallback step for flows where reliability matters. A common pattern is a terminal `failed` step that summarizes the error.

When a step may encounter blockers requiring human intervention, use `postpone: true` to pause the flow. On re-invocation with the same session prefix, the postponed step resumes. In this case, recommended to instruct step with explicit Blockers section, so agent can check if blockers resolved before doing any further work. The same strategy works well for both struct `output` and without it.

When a step needs to iterate inside a single invocation (build levels of a graph, process pagination, retry a polling operation), route the step back to itself **without `postpone`** — this is an in-process self-loop. Always cap such a loop with `maxIterations` as a safety net. `${step.iteration}` (1-based) is available in both prompts and rules for iteration-aware behaviour and termination predicates. Mark fields that must be recomputed each iteration as `required` in the output schema (args accumulate across iterations, so omitted fields persist from the prior pass).

`maxTurns` (per-step) overrides the agent's `maxTurns` for a single step. Useful when one step in a flow needs more (or fewer) tool-use turns than the rest of the flow — e.g. a long-running build coordinator vs. a short summary step. `maxIterations` is a different axis (counts whole agent runs of the step, not tool-use turns within one run).

Use `flow.session.prefix` with an `${args.*}` reference when the flow should be resumable by a user-provided identifier (e.g., ticket ID). Otherwise omit it to get independent invocations.

Use `flow.args` with JSON Schema to validate required inputs upfront. The `prompt` key is always allowed without declaration.

When multiple rules on a step can match simultaneously, the engine forks and runs all matching steps in parallel. Design rules to be mutually exclusive when parallel execution is not desired.

Step IDs and flow filenames must be kebab-case, max 64 characters.

Default agent is `coder` when `agent` is omitted from a step.

## Available Agents

The following built-in agents are available for use in flow steps:

| Agent ID | Tools | Best for |
|---|---|---|
| `coder` | All tools | Default. General-purpose development work — coding, searching, running commands. |
| `hivemind` | `view`, `glob`, `grep`, `fetch`, `sourcegraph`, `websearch`, `task`, `skill` (no `bash`, `edit`, `multiedit`, `write`, `delete`, `patch`, `lsp`) | Supervisory coordination across subagents. Use for orchestration steps that delegate work rather than do it directly. |
| `explorer` | Read-only tools only (no `bash`, `edit`, `multiedit`, `write`, `delete`, `patch`, `task`) | Fast codebase exploration — finding files, searching code, answering questions about the repo. |
| `workhorse` | All tools except `task` and `websearch` | Autonomous coding tasks that run to completion. Use when a step must write/modify code but doesn't need to spawn subagents. |
| `summarizer` | No tools | Summarizes conversation history. Has zero tool access — can only produce text output. |
| `descriptor` | No tools | Generates short session titles. Internal/hidden, rarely needed in flows. |

You can also reference **custom agents** defined in:
- `.agents/types/<agent-name>.md` (project)
- `.opencode/agents/<agent-name>.md` (alternative project path)

Custom agent files define the agent's system prompt and tool restrictions. Use custom agents when a step requires specialized instructions or a curated tool set not covered by the built-in agents.

## Step Output Guidelines

For **terminal steps** (e.g., `failed`, `summary`, `done`) that don't drive routing or feed data to downstream steps, **omit the `output` schema**. Without structured output, the agent's free-form text response is shown directly to the user, making it readable as a human-friendly summary.

For such terminal steps, prefer the `summarizer` agent when the step only needs to produce a textual summary from accumulated context. Since `summarizer` has no tool access, it is lightweight and focused purely on text generation.

```yaml
# Good: terminal summary step — no output schema, summarizer agent
- id: summary
  agent: summarizer
  prompt: |
    Summarize what was accomplished in this flow.
    Previous context: ${args}

# Good: terminal failure step — no output schema
- id: failed
  agent: summarizer
  prompt: |
    The flow failed. Summarize what went wrong and suggest next steps.
    Error context: ${args}
```

Reserve structured `output` for steps whose results are consumed by rules or subsequent step prompts via `${args.*}`.

## Patterns

### Sequential Pipeline
Omit rules from all steps. Steps execute in array order, each receiving the previous step's output.

### Conditional Branching
Use a classifier step with structured output and rules routing to different branches:
```yaml
- id: classify
  output:
    schema:
      type: object
      properties:
        category:
          type: string
          enum: [bug, feature, docs]
      required: [category]
  rules:
    - if: ${args.category} == bug
      then: fix-bug
    - if: ${args.category} == feature
      then: implement-feature
    - if: ${args.category} == docs
      then: write-docs
```

### Parallel Fan-out
Multiple rules matching simultaneously triggers parallel execution:
```yaml
rules:
  - if: ${args.needs_tests} == true
    then: write-tests
  - if: ${args.needs_docs} == true
    then: write-docs
```

### Self-loop with Postpone (Blocker Pattern)
A step routes back to itself with `postpone: true` when blockers exist. The flow pauses until the next invocation:
```yaml
- id: work
  output:
    schema:
      type: object
      properties:
        blockers:
          type: array
          default: []
          description: list of blockers preventing from further progress and requiring user's feedback, empty if none
          items:
            type: string
      required: [blockers]
  rules:
    - if: sizeof ${args.blockers} != 0
      then: work
      postpone: true
    - if: sizeof ${args.blockers} == 0
      then: next-step
```

### Self-loop In-Process (Iteration Pattern)
A step routes back to itself **without** `postpone` to iterate within the same flow invocation. Use when iteration progress is determined inside the flow (no external blocker). Always cap with `maxIterations`:
```yaml
- id: build-level
  agent: coder
  prompt: |
    Build libraries at level ${args.current_level} (iteration ${step.iteration}).
    Accumulated state: ${args.snapshot_versions}
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
  maxIterations: 20
  rules:
    - if: ${args.has_more_levels} == true
      then: build-level
    - if: ${args.has_more_levels} != true
      then: publish
```

Each iteration shares the same step session (the agent remembers prior iterations). The iteration counter persists across postpone → resume cycles via `flow_states.iteration`. The cap fires the step's `fallback` (if any) on exhaustion.

### Error Recovery
Use fallback to retry and then route to an error-handler step:
```yaml
fallback:
  retry: 2
  delay: 10
  to: error-handler
```

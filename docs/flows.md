# Flows

Flows provide deterministic, multi-step agent workflows. A Flow is a YAML-defined directed graph of steps, each with its own agent, prompt template, optional structured output, routing rules, and fallback strategy.

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
| `prompt` | string | Yes | Prompt template with `${args.*}` placeholders |
| `output.schema` | object | No | JSON Schema for structured output |
| `rules` | array | No | Conditional routing rules |
| `fallback` | object | No | Retry and error routing |

### Rules

```yaml
rules:
  - if: "${args.status} == READY"
    then: implement
  - if: "${args.status} != READY"
    then: skip
```

Supported operators:

| Operator | Description |
|----------|-------------|
| `==` | Equality |
| `!=` | Inequality |
| `=~` | Regex match (pattern delimited by `/`) |

When multiple rules match, the corresponding steps execute in parallel.

#### Rule fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `if` | string | Yes | Predicate expression to evaluate |
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

## CLI Usage

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

### Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--flow` | `-F` | Flow ID to execute |
| `--arg` | `-A` | Flow argument as `key=value` (repeatable) |
| `--args-file` | | JSON file with flow arguments |
| `--prompt` | `-p` | Initial prompt (optional with `--flow`, added to args) |
| `--session` | `-s` | Session prefix for deterministic naming |
| `--delete` | `-D` | Delete previous state and start fresh |

## Template Substitution

Prompts support `${args.*}` placeholders:

- `${args.prompt}` — Value of the `prompt` argument
- `${args.key}` — Value of any argument by key
- `${args}` — Full JSON dump of all arguments

Arguments accumulate as the flow progresses. When a step produces structured output, its fields are merged into the args map for subsequent steps.

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

## Output

Flow execution produces a JSON envelope:

```json
{
  "flow_id": "my-flow",
  "completed_steps": [
    {
      "step_id": "review",
      "session_id": "prefix-my-flow-review",
      "status": "completed",
      "output": "...",
      "is_struct_output": true
    }
  ],
  "failed_steps": [],
  "running_steps": [],
  "postponed_steps": []
}
```

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

### Parallel execution

When multiple rules on a single step evaluate to true, all matching successor steps run concurrently. Each fork receives its own copy of the accumulated args, so parallel branches cannot interfere with each other.

### Diamond convergence

If two parallel branches both route to the same step (A→B, A→C, B→D, C→D), the first branch to arrive runs step D. The second branch detects that step D is already running and skips it. Step D executes exactly once with the args from whichever branch arrived first.

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

## See Also

- [Custom Commands](custom-commands.md)
- [Skills](skills.md)
- [Session Providers](session-providers.md)
- [Structured Output](structured-output.md)

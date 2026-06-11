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

Compose multiple atomic predicates with `&&` (AND) and `||` (OR), grouped with parentheses, instead of emitting overlapping rules. `&&` binds tighter than `||`; evaluation short-circuits. Example: `sizeof ${args.blockers} == 0 && ${args.deploy} == true`.

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

### Interactive Step (Human-in-the-Loop via Chat Bridge)

When a step needs the user (a reviewer, operator, or stakeholder) to provide
information mid-flow, mark it `interactive: true` and supply an `interaction`
block. The flow engine auto-binds the step's session to the resolved peer(s)
via the in-process chat bridge BEFORE `agent.Run` starts, and auto-unbinds
on `struct_output`. The agent's first turn fans out to the bound peer(s);
reviewer replies route back to the agent through the bridge's inbound
dispatcher. The conversation lives entirely inside one `agent.Run`
invocation — no postpone/resume needed for the in-conversation back-and-forth.

```yaml
- id: ask-reviewer
  agent: coder
  interactive: true
  interaction:
    target: ${args.reviewer}           # single PeerRef OR array — see "Target shapes" below
    mention: ${args.reviewer.mention}  # optional: ping handle for first message only
  prompt: |
    Ask the reviewer for clarification on ${args.topic}. Use the `question`
    tool to present numbered options — on Slack/Telegram the bridge renders
    them as interactive buttons; on Mattermost it falls back to numbered text.
    Capture the reviewer's choice via struct_output.
  output:
    schema:
      type: object
      properties:
        decision: { type: string }
      required: [decision]
  maxTurns: 50   # long conversations need headroom
  rules:
    - then: next-step
```

**Target shapes.** `interaction.target` accepts:

- **Single PeerRef** (object) — the step binds to exactly one chat peer.
  ```yaml
  interaction:
    target: ${args.reviewer}    # PeerRef object {channel, identity, peerId, mention?}
  ```
- **Array of PeerRefs** — the step binds to multiple reviewers; agent output
  fans out to every reviewer, inbound from any reviewer routes back to the
  same agent run with `[<who> via <channel>]: ` attribution prefix.
  ```yaml
  interaction:
    target: ${args.reviewers}   # array of PeerRef objects
  ```

The target value MUST come from a `${args.NAME}` expression — the resolver
does not support literal PeerRefs in YAML or nested-path expressions. The
caller (orchestrator, `/flow` POST body, or `--flow-args` JSON) supplies the
PeerRef in the args object. This forces dynamic peer selection to live in
the orchestrator layer where access control is enforced, not in YAML.

**PeerRef shape** (each entry, whether single or array):

```json
{
  "channel":  "slack" | "telegram" | "mattermost",
  "identity": "<configured identity id, e.g. 'default'>",
  "peerId":   "<platform-specific peer id, see below>",
  "mention":  "<optional first-message ping handle>"
}
```

**Supported channels and peerId formats:**

| Channel | peerId form | Notes |
|---|---|---|
| `slack` | `D<id>` (DM), `C<id>` (channel), `C<id>\|<thread_ts>` (thread), `U<id>` (user) | `U<id>` is auto-resolved to a DM channel via `conversations.open` before persistence. Channel-only `C<id>` is auto-mutated to `C<id>\|<ts>` on first outbound — subsequent replies thread correctly. |
| `telegram` | numeric `chat_id` (e.g. `344281281`) | Private bots require the chat to have paired with `/pair <code>` first (allowlist gate is inbound-only — outbound delivery works regardless). |
| `mattermost` | `<channelId>` (DM/channel), `<channelId>\|<rootPostId>` (thread), 26-char user-id | User-id is auto-resolved to a DM via `channels/direct`. Channel peers mutate to channel\|rootPostId on first outbound. |

**`interaction.mention`** (optional) is a platform-native ping handle (e.g.
`@username` on Slack, `@FirstName` on Telegram) prepended to the FIRST
outbound message for the binding only — then cleared. Useful for paging the
reviewer on a busy channel.

**Bridge-disabled fail-fast.** If `cfg.Router == nil` at server boot
(no `router` section in `.opencode.json`), the no-op `InteractiveHook`
returns `flow.ErrInteractiveBridgeDisabled` on `OnInteractiveStepStart`,
so the interactive step transitions to `failed` immediately with a clear
error rather than silently hanging. Don't author interactive flows for
deployments where the bridge isn't configured.

**Tuning maxTurns for interactive steps.** Each user reply consumes one
tool-use turn (the inbound→agent.Run cycle counts as one). For a planning
conversation spanning 15–30 reviewer exchanges plus tool calls per turn,
`maxTurns: 100–150` is the right ballpark. The agent's default is much
lower and will cut off the conversation mid-way.

**Question UI rendering.** When `cfg.Router.QuestionMode == "interactive"`
(set in `.opencode.json`), the agent's `question` tool renders choices
using platform-native UI:
- Slack: actions block with one button per option.
- Telegram: inline keyboard with one row per option.
- Mattermost: numbered-text fallback (interactive attachments need a
  webhook URL the bridge doesn't host).

Reviewer button clicks are normalized into the same `bridge.Inbound`
shape as text replies, so the agent's question-reply parsing works
identically across all three channels.

**SSE for orchestrators.** When an interactive step enters its
conversation phase, the flow runner emits `flow.waiting_for_input` on
the `/event` SSE stream with `{runID, stepID, sessionID, target}`. External
orchestrators (c2-agent, k8s Jobs) MAY use this to display a "waiting on
reviewer" indicator without polling `/flow/status`.

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

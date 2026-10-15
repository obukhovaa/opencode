## Context

A flow run enters its first step and follows rules from there. `flow_states` holds one row per step session; a re-trigger walks those rows from the first step (`collectResumableSteps`): a `completed` row is skipped, its output merged into args, and its rules followed with that output. The first step that is not completed is where the run resumes.

An interactive step binds its session to peers through the `InteractiveHook`, emits `waiting_for_input`, and runs its agent. The agent calls the `question` tool, which publishes a `question.Request` the chat bridge relays to the bound peers and blocks on the answer.

## Goals / Non-Goals

**Goals**
- Start a run at a named step, with the outputs of the steps before it supplied by the caller and treated as done.
- Let an interactive step show a supplied output and wait for the person's answer with no model call; the answer moves the run on as usual.
- A run started this way resumes like any other run when it later parks.

**Non-Goals**
- Deciding which steps to supply or present. The caller decides per step.
- Flow YAML changes. Nothing in a flow declares that it can be started part-way.
- Supplying transcripts. Done steps get empty sessions.
- The `opencode serve --flow` auto-start flags.

## Decisions

### D1 — Done steps are completed rows, written before the first step runs

The run records each supplied step as a `completed` row with the args it would have run with and the supplied output, and publishes it as completed, the same state the resume planner publishes for a step it skips. Its output is merged into args the way `runStep` merges a struct output.

*Why rows, not only args.* A run that parks after the start step resumes in a new invocation. That invocation walks rows from the first step. Without rows for the done steps it would run them. With them it walks past, exactly as for a run that ran them.

*Rejected.* Merging the outputs into the start step's args and writing nothing. It works until the first park, then the resume runs the first step again.

### D2 — The supplied outputs must route to the start step, and nothing else is recorded

Before writing anything, the engine walks from the first step over the supplied outputs, with each step's own rules and iteration 1, the way the resume planner will. Every step the walk reaches must be either supplied or the start step. The walk must reach the start step. A supplied output that routes its step back to itself, or to a postpone, is refused. A supplied step the walk never reaches is refused too: a stray completed row would be skipped by a later resume as work done.

The first arrival at the start step decides its args and predecessor, as the diamond-convergence guard decides for a real run.

### D3 — A start-at run never lands on resumable work

`startAt` is refused when the run has work a re-trigger would resume, and with `recoverRunning`. The caller passes `fresh` to discard that work first. Otherwise the done rows and the resumed rows would disagree about which steps ran.

### D4 — A presented step asks its question in the engine, on the step's own session

On the presented step's first entry, after the bind and the `waiting_for_input` transition, the engine calls the question service's `Ask` on the step's session with the supplied prompt. The bridge relays it as it relays the agent's own question, so the peer sees no difference.

- The accept option alone completes the step with the supplied output, through the same completion path as an agent's result: rules, `maxIterations`, the completed row and event. No attempt runs.
- Any other answer, a multi-select that adds another option, or a dismissal runs the step's agent as usual. Its first prompt starts with the output that was shown, the question and the answer, then the step's own prompt. This keeps "the person's answer moves the run on as usual" true for answers the engine cannot interpret.
- A presentation is taken once per run. A self-loop or cycle back into the step runs the agent.
- A cancelled context while waiting fails the step, as a cancelled agent run would.

*Why the caller supplies the question.* The engine has no wording of its own for an arbitrary step. The caller already knows what its steps ask.

*Rejected.* Writing a synthetic assistant turn into the session so the agent's own `question` call is replayed. It ties the engine to one prompt's tool-call shape and still needs a model call to continue.

### D5 — Supplied outputs are checked against the step

An output is a JSON object for a step with `output.schema`, and must carry the schema's `required` fields, the same check `struct_output` applies. It is stored compacted, as a struct output is. Text is accepted only for a step without a schema, because a schema step's rules read struct fields.

### D6 — Validation happens before the first write

All refusals wrap `ErrInvalidStartOptions` and come back from `RunWithOptions` before a row is written or a session created. `POST /flow` forwards the fields unchanged, and the run fails with the refusal as any invalid-args run does.

## Risks / Trade-offs

- **Forked steps lose context.** A step with `session.fork` after a done step copies an empty transcript. A flow that relies on a forked history must pass that context through args when started part-way.
- **Done steps never ran here.** Their sessions have no cost or messages. Consumers that read per-step cost see zero for them.
- **Presented question bypasses the agent's own wording.** If the caller's question differs from what the agent would ask, the person sees the caller's.

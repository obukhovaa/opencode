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

A done step on a branch parallel to the start step is recorded as done, unless the start step can route to it through any chain of rules or fallbacks, whatever their conditions. That is a join: recorded as completed, its route from the start step would be dropped as a diamond convergence and it would never run after the start step. Such a run is refused. Scheduling the parallel branch's unsupplied steps alongside the start step, as a resume schedules pending routes, would allow it, and is left for later.

### D3 — A start-at run never lands on resumable work

`startAt` is refused when the run has work a re-trigger would resume, and with `recoverRunning`. The caller passes `fresh` to discard that work first. Otherwise the done rows and the resumed rows would disagree about which steps ran.

### D4 — A presented step asks its question in the engine, on the step's own session

On the presented step's first entry, after the bind and the `waiting_for_input` transition, the engine calls the question service's `Ask` on the step's session with the supplied prompt. The bridge relays it as it relays the agent's own question, so the peer sees no difference.

- The accept option alone completes the step with the supplied output, through the same completion path as an agent's result: rules, `maxIterations`, the completed row and event. No attempt runs.
- Any other answer, a multi-select that adds another option, or a dismissal runs the step's agent as usual. Its first prompt starts with the output that was shown, the question and the answer, then the step's own prompt. This keeps "the person's answer moves the run on as usual" true for answers the engine cannot interpret.
- A presentation is taken once per run. A self-loop or cycle back into the step runs the agent.
- The engine posts only the question. The caller puts what the person should review into it; the revision prompt therefore says the person was asked about the output, not that they saw it.
- A cancelled context while waiting fails the step, as a cancelled agent run would, and the failure is persisted on a fresh deadline: written with the cancelled context it would fail, leave the row `running`, and the next trigger would replay it instead of running.

*Why the caller supplies the question.* The engine has no wording of its own for an arbitrary step. The caller already knows what its steps ask.

*Rejected.* Writing a synthetic assistant turn into the session so the agent's own `question` call is replayed. It ties the engine to one prompt's tool-call shape and still needs a model call to continue.

### D5 — Supplied outputs are checked against the step

An output is a JSON object for a step with `output.schema`, completed as `struct_output` completes a document (`tools.CompleteStructOutput`): declared defaults fill absent properties, then the schema's `required` fields must be present. It is stored completed and compacted, as a struct output is, so rules that read a defaulted field route as they would for the step's own output. A step without a schema takes text only, because its agent cannot produce an object and its rules cannot read one.

### D6 — Validation happens before the first write

All refusals wrap `ErrInvalidStartOptions` and come back from `RunWithOptions` before a row is written or a session created. `POST /flow` forwards the fields unchanged, and the run fails with the refusal as any invalid-args run does.

## Risks / Trade-offs

- **Forked steps lose context.** A step with `session.fork` after a done step copies the done step's transcript, which this run left empty; without `fresh`, it may hold an earlier run's transcript under the same prefix. → Accepted: the engine does not invent a transcript for a step that never ran here. A caller that starts part-way passes what a forked step needs through args, and `docs/flows.md` ("Starting at a step") says so.
- **Done steps never ran here.** Their sessions have no cost or messages. Consumers that read per-step cost see zero for them. → Accepted: that is the true cost of this run; the outputs were paid for in the run that produced them.
- **Presented question bypasses the agent's own wording.** If the caller's question differs from what the agent would ask, the person sees the caller's. → Accepted: the caller owns the presentation and is expected to send the question the step's agent asks; any answer other than `accept` hands the step back to its agent.
- **The step timeout does not bound a presented question.** The question waits like any question to a person, until it is answered, dismissed or the run is aborted; `timeout:` and the non-interactive wait timeout apply once the step's agent runs. → Accepted: a presentation is a person's decision, and a timeout read as a dismissal would run the agent unasked. `docs/flows.md` states it.

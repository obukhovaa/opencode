## Why

A caller that already holds the outputs of a flow's early steps has no way to skip them. Re-running a reviewed setup with one change in the middle means running every step from the first: each earlier step costs an agent turn that reproduces an output the caller already has. Steps after the change that it does not reach cost the same, only to show the person the output they approved last time.

The engine already knows how to treat a step as done. A resumed run walks completed `flow_states` rows, merges their outputs into args and carries on from the first step that is not done. What is missing is a way for the caller to supply those outputs at start, and a way for an interactive step to show a supplied output and wait for the person's answer without a model call.

## What Changes

- **Start at a step.** `flow.RunOptions` and `POST /flow` gain `startAt` (a step id) and `doneSteps` (step id to output). The steps the flow reaches before `startAt` are recorded as `completed` with the supplied outputs, published as completed, and merged into args. None of them runs. The run then enters `startAt`.
- **Present a supplied output.** `presentSteps` maps an interactive step id to an output, a question and the option label that accepts it. On the step's first entry in the run, the engine binds it as usual, asks the question on the step's session with no model call, and waits. The engine posts only the question, so the caller puts what the person reviews into it. Accepting completes the step with the output and routes on. Any other answer runs the step's agent as usual, told the output, the question and what the person answered.
- **Validated before anything is written.** The supplied outputs must route, through the flow's own rules, from the first step to `startAt`, exactly as a later resume would walk them. A run with work to resume is refused unless `fresh` is set.

## Capabilities

### New Capabilities

- `flow-start-at-step`: starting a run at a named step with earlier outputs supplied, and showing a supplied output on an interactive step with no model call.

### Modified Capabilities

- `flow-api`: the `POST /flow` body accepts `startAt`, `doneSteps` and `presentSteps`.

## Impact

- `internal/flow`: `RunOptions.StartAt`, `DoneSteps`, `PresentSteps`; `PresentedStep`; `ErrInvalidStartOptions`; the start plan and its validation (`start_at.go`); `runStep` asks a presented step's question before its first attempt.
- `internal/api`: `POST /flow` forwards `startAt`, `doneSteps` and `presentSteps`.
- `docs/flows.md`: "Starting at a step".
- No change to flow YAML, config or the `.opencode.json` schema. A run that sets none of the new fields behaves exactly as before.
- Not covered: the `--flow` auto-start flags of `opencode serve` (the per-pod entrypoint) do not take the new options. A caller that needs them there is a follow-up.

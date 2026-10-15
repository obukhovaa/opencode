## 1. Start at a step

- [x] 1.1 `RunOptions.StartAt` and `RunOptions.DoneSteps`; `ErrInvalidStartOptions`
- [x] 1.2 Supplied outputs normalised per step: object with the schema's required fields, or text for a schemaless step
- [x] 1.3 Start plan: walk from the first step over the supplied outputs to `startAt`; refuse unknown steps, gaps, self-routes and postpones, unreached start, stray done steps, `startAt` in `doneSteps`, `recoverRunning`
- [x] 1.4 Refuse `startAt` over resumable work unless `fresh`
- [x] 1.5 Record each done step as a completed row in its own session, publish it as completed, enter `startAt` with the merged args

## 2. Present a supplied output

- [x] 2.1 `RunOptions.PresentSteps` and `PresentedStep`; refuse non-interactive steps, steps also in `doneSteps`, missing question text, an accept label that is not an option, and a missing question service
- [x] 2.2 On the step's first entry, after the bind, ask the question on the step's session with no model call
- [x] 2.3 Accept completes the step with the supplied output; any other answer or a dismissal runs the agent with what was shown and answered ahead of its prompt
- [x] 2.4 Presentation taken once per run

## 3. API and docs

- [x] 3.1 `POST /flow` forwards `startAt`, `doneSteps`, `presentSteps`
- [x] 3.2 `docs/flows.md` "Starting at a step"

## 4. Tests

- [x] 4.1 Plan refusals and argument merging
- [x] 4.2 A run started at step 3 with outputs for steps 1-2 runs only step 3's agent and presents step 4 with no model call
- [x] 4.3 Not accepted and dismissed presentations run the agent with the shown output and the answer
- [x] 4.4 A presentation is taken once
- [x] 4.5 Refused over resumable work; accepted with `fresh`
- [x] 4.6 A start-at run that parks resumes past the done steps
- [x] 4.7 `POST /flow` forwards the three fields

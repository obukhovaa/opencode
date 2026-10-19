## 1. Engine

- [x] 1.1 `stepWork.resumed`: set by `collectResumableSteps` on a resumed non-completed row, a postponed row and a completed self-loop's pending iteration
- [x] 1.2 `runStep` takes `resumed`; a resumed presented step takes the presentation, asks nothing and runs its agent

## 2. Docs

- [x] 2.1 `docs/flows.md` "Starting at a step": which continuations run the agent, including an interrupted presentation
- [x] 2.2 `RunOptions.PresentSteps` and `presentationSet` doc comments

## 3. Tests

- [x] 3.1 A continuation over a `running` row (with `recoverRunning`), a `postponed` row, a `failed` row under `resume_on_failure` and a self-loop's pending iteration runs the agent and asks nothing
- [x] 3.2 The resumed entry uses up the presentation: a self-loop in the same run asks nothing either
- [x] 3.3 A re-trigger over a finished run's `completed` rows presents again, for the first step and for a routed-to step
- [x] 3.4 The existing presentation tests pass unchanged

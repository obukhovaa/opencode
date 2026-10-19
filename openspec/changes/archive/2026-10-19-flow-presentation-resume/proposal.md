## Why

A presented step asks its question on its first entry in a run. The orchestrator carries `presentSteps` onto every continuation job (killed-step recovery, resume after a postpone), and each continuation is a new run with its own presentations. So when a person declines a presented output, the step's agent starts revising it, and the pod is killed or the step parks before it completes, the continuation asks the original question again. If the person now accepts, the step completes with the output they asked to change, and the revision is lost (CD-5359).

## What Changes

- A first entry that continues the step's own unfinished work from an earlier run no longer asks. It runs the step's agent as usual, on the step's session, which already holds the revision prompt and the person's answer. That covers the entry the resume planner makes for a step whose row was `running`, `postponed`, `waiting_for_input` or `failed` before the run wrote anything, and the pending next iteration of a completed self-loop.
- That entry still uses up the presentation, so a self-loop or cycle back into the step later in the same run runs the agent too, as it would have had the earlier run not been interrupted.
- A step with no row, or whose `completed` row a re-trigger re-runs from the first step, presents as before.
- A run interrupted while the presented question was still open is not asked again either: the continuation runs the agent, which asks its own question. So does a step another step's `postpone` rule parked before it ever ran.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `flow-start-at-step`: "A presentation is used once per run" adds the continuation rule.

## Impact

- `internal/flow`: `stepWork.resumed`, set by `collectResumableSteps` and passed to `runStep`, which skips the presentation on a resumed entry. The decision comes from the rows the planner walks, as they stood before the run wrote anything. `runStep`'s own read at its entry-time write cannot make it: by then `RunWithOptions` has already set every initial step's row to `running`.
- `docs/flows.md`: "Starting at a step".
- No change to the API, flow YAML, config or the `.opencode.json` schema.

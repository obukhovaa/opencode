# Design

## Context

The per-run tracker currently counts arguments before dispatch. Tools execute in parallel or sequential groups, and inbox input is persisted at two boundaries between turns. See proposal.md for motivation.

## Goals / Non-Goals

Goals: distinguish completed no-progress calls from useful calls and from a conversation with new input. Keep the existing repeat threshold and forced-tool exemption.
Non-goals: change tool permissions, turn budgets, transport delivery or configuration schema.

## Decisions

- Observe completed results, including type, content and error status; exclude call IDs and execution metadata such as timestamps and durations. Store a digest so large tool responses are not retained twice. Compare the requested arguments, as the existing detector does; compare the result after hooks, as delivered to the model.
- On the threshold result, report the loop warning; later identical requests are blocked before dispatch until input changes or delivered messages reset history. This permits the threshold call to return different output rather than predicting its result from earlier calls.
- Serialize recording through the existing result publication closure, so parallel completions cannot race tracker updates. Synthetic results for missing tools, pre-tool blocks, canceled unstarted calls and loop warnings are not evidence of completed repetitions.
- Reset only repetition state when either inbox boundary successfully stores messages or a question returns a successful answer. Repeated identical answers are still user input; dismissed questions remain errors and accumulate repetitions. Empty/failed drains do not reset; permission-denial history stays independent.

## Risks / Trade-offs

- Detecting output changes requires one additional real execution compared with the old pre-dispatch threshold. Subsequent identical attempts are blocked, and max-turns still bounds the run.
- A result digest has a negligible collision risk; SHA-256 avoids retaining potentially large response bodies.
- Parallel completions are observed in completion order; the recording lock keeps each complete observation atomic.

## Migration Plan

No data or config migration. Ship in an engine release after review. Revert the commit to restore the previous detector.

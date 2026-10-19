# Run-Level Trace Metadata and the Pooled Flow Tag

## Why

GENAI-499 (stage 1). A Langfuse trace of an agent run says what the run was but not how it went. To learn whether a run stopped on its own, ran out of turns or failed, and how much of its turn budget it spent, one has to fetch every generation observation of the trace and count. That is too slow and too costly to do across thousands of runs, so nobody measures it.

Pooled runs have a second gap. A pool pod boots with the static tag `flow:none` because it does not yet know which flow it will serve. The run's `identity:` and `team:` tags already replace the pod's boot-time ones (`runidentity.MergeTags`), but nothing replaces the `flow:` tag. Every pooled run therefore reports `flow:none`, and a dashboard filtered by flow does not see pooled traffic at all.

## What Changes

- **Turn budget on the trace.** When a run's trace is created, it records `max_turns`: the effective budget of that `RunWith` call.
- **Outcome on the trace.** When the run ends, it records `turns_used` (model calls made, the forced wrap-up included, summed over non-interactive re-entries) and `finish_reason`, one of `end_turn`, `max_tokens`, `turns_exhausted`, `permission_denied`, `canceled`, `error`.
- The keys go through the existing `telemetry.metadataNamespace` (`piano.max_turns` and so on). `telemetry.generations` does not gate them.
- A subagent's values land on its own observation through a new `langfuse.SetTraceMetadata`, which follows the child guard that `SetTraceOutput` uses. A child never overwrites its parent's trace metadata.
- **`flow:<flowID>` on POST /flow runs.** The run identity a POST /flow run publishes now carries a `flow:<flowID>` tag. A flow tag alone is enough to publish an identity. The tag shadows a configured `flow:` tag such as `flow:none` instead of sitting beside it. `opencode serve --flow` (the per-Job auto-start) is unchanged and keeps its configured tag.

## Capabilities

### New Capabilities

- `telemetry-run-metadata`: run-level trace metadata (`max_turns`, `turns_used`, `finish_reason`) and the flow tag on pooled runs.

## Impact

- **New:** `internal/llm/agent/trace_run_metadata.go`.
- **Modified:**
  - `internal/langfuse/context.go` (`SetTraceMetadata`);
  - `internal/llm/agent/agent.go`: the trace start, a turn counter at the two model-call sites, and the deferred stamp;
  - `internal/api/handler_flow.go` (`flowStartOptions.telemetryFlow`, `flowTags`, the publish gate);
  - `docs/telemetry.md`.
- **`.opencode.json` public contract:** unchanged.
- **Behaviour change:**
  - every POST /flow run now publishes a run identity, even one that sends no identity fields;
  - the `flow:` tag also reaches provider request metadata (LiteLLM tags) through `provider.ResolveTags`;
  - spend dashboards that bucket on `flow:none` will see that bucket shrink.
- **Follow-up:** an overflow-retry resend (GENAI-497) that must not count as a turn has to skip the `turnsUsed++` that sits beside each `streamAndHandleEvents` call.

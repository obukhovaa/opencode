## Context

See [proposal.md](proposal.md) for motivation. In the Go fork, MCP leaves enforce permissions, the registry owns pooled transport and request identity, and the agent loop owns hooks, watchdogs and result publication. Calling either the registry or a leaf directly from a composition tool would skip part of that execution contract. MCP rendering currently flattens content and loses `IsError`; it does not expose a safe, post-filter structured result channel.

Parallel calls already reduce waiting for independent reads. Deferral reduces schema exposure, and RTK compacts shell output. None keeps dependent MCP intermediates outside the model transcript. The TypeScript upstream's CodeMode has a separate interpreter and host runtime; porting that machinery is outside this pilot.

## Goals / Non-Goals

**Goals:** Reuse authorized reads inside one invocation, return selected facts with provenance, and establish deterministic correctness and context-volume evidence before enabling a pilot agent.

**Non-Goals:** A scripting VM, new dependencies, dynamic fanout, automatic pagination, write tools, cross-call storage, background jobs, cache warming, background compaction, or changes to Claude provider prefix/cache behavior.

## Decisions

### 1. Fixed declarative plan

Register `mcp_compose` only when the agent explicitly names it in its grant and the top-level `mcpComposition.readTools` exact-name list is nonempty. Neither an implicit legacy grant nor a wildcard grants this tool. The operator-maintained list attests reviewed read-only endpoints; MCP annotations alone never grant eligibility. Known mutating endpoints and general SQL, HTTP or code execution endpoints are unsuitable for the list. Reject an explicitly false `readOnlyHint`; a missing hint requires the same operator review. No default configuration adds the tool or changes prompt text.

The plan schema is fixed, independent of available endpoints:

```json
{
  "steps": [
    {"id":"lookup", "tool":"catalog_find", "arguments":{"key":"demo"},
     "select":{"fields":{"id":"/id"}}},
    {"id":"detail", "tool":"catalog_get", "arguments":{"id":null},
     "bindings":[{"target":"/id", "step":"lookup", "pointer":"/id"}],
     "select":{"fields":{"id":"/id", "state":"/state"}}}
  ],
  "return":["detail"]
}
```

Tools and arguments are static JSON; bindings replace existing argument values with values from earlier projected results. Targets must be distinct, nonoverlapping RFC6901 pointers below the argument root. Reject unknown keys, duplicate IDs, future references, invalid pointers and invalid return IDs before any call. Every step has exactly one selection form: `{"fields":{...}}` for an object projection, or `{"rows":"/items","fields":{...},"limit":20}` for a projected array. Row field pointers are relative to each row. Maps contain 1–32 fields; row limits are 1–100. Missing pointers fail; explicit JSON null is valid. No expressions, interpolation or dynamic tool names.

Use the Go standard JSON decoder with preserved numeric lexemes, depth validation and duplicate-key rejection so large numeric IDs and ambiguous bindings cannot change meaning. Numeric preservation must extend through the shared hook and MCP argument decoders, not only plan parsing; replace float-coercing argument decoding at that boundary and assert `9007199254740993` on the actual child request after both no-op and rewriting hooks. A JavaScript runtime adds execution and dependency surface without being needed for these operations. A batch-only tool would save result bytes but still require model turns between dependent reads.

### 2. One authorized dispatch path

Extract the common single-call execution boundary from the parallel and sequential paths in `internal/llm/agent/agent.go`. It must return execution status, authoritative post-hook text, hook context and output eligibility separately from conversation publication. Ordinary paths retain their ordering, loop/denial tracking and publication behavior. Composition calls the same boundary; it does not call `MCPRegistry.CallTool` or `BaseTool.Run` directly.

Bind the tool to the resolved, filtered tool snapshot in `internal/llm/agent/tools.go`, like toolsearch. Intersect that snapshot with `readTools`, reject duplicate effective names and non-MCP tools, preserve deferred wrappers, and require deferred children to be activated in the current session. Recheck eligibility and current permission for each child, including hook-mutated arguments. Do not activate tools, refresh the catalog, or reorder provider tools as a side effect.

Each child gets the original session, agent, requester, peer, auth and flow context, its own call identity and the existing watchdog/pool path. Clear inherited `HookAllowKey` before evaluating the child's hooks; an outer composition approval cannot approve its children. Child hooks, ownership checks, human denial, cancellation and loop/denial accounting retain ordinary semantics. A bounded plan cannot be used to reset repeated-call tracking across invocations. Nested permission-wait observation must be chained or shared: replacing the outer observer would leave the composition watchdog charging human wait time. Test a permission wait longer than the active execution budget without timing out either level.

### 3. Project the filtered text, never a raw bypass

First preserve MCP `IsError` and add runtime-only eligibility metadata recording whether the original content is text-only and complete. Preserve ordinary text rendering and existing spill behavior. Composition accepts complete text-only output whose authoritative post-hook text is one valid JSON value. It does not parse appended hook context. A hook block, structured-only result, media/resource content, capped result, malformed JSON or projection error stops the plan. Do not inspect raw `structuredContent`, reopen a spill file or fall back to raw text after a rewrite. Supporting structured-only endpoints later requires a separate explicit hook contract.

Only selected values survive into the next step; release full decoded results after projection. Publish one outer tool result. Child tracing carries tool name, status, duration and byte counts, but neither raw output nor bound argument values; child SSE events must not carry raw results. Hook context remains visible through the existing outer hook-context mechanism, including on failure. Existing permission prompts can display actual child arguments as they do for ordinary calls.

### 4. Fixed bounds and explicit incompleteness

Limits are not model-overridable: 1–8 steps/calls, 32 KiB plan UTF-8, JSON nesting depth 64, 256 KiB authoritative text per child, 1 MiB cumulative accepted text, 16 KiB serialized projected values across the plan, and 120 seconds active execution excluding permission waits. The parent deadline and each child's existing smaller applicable bound still apply. These are retained-data limits, not transport allocation limits: the MCP library decodes responses before the pilot can inspect their size.

The result contains `complete`, `results` keyed by requested step IDs, and ordered `steps` entries with `id`, `tool`, `status` (`ok`, `failed`, `skipped`), plus row `totalRows`, `returnedRows` and `limited` when applicable. Errors have stable `code`, `step` and a bounded message. A row limit deliberately selects the first N rows in server order and sets `limited:true` and `complete:false`; it is not a tool failure. Stop at the first other failure, mark later steps skipped, set the outer error flag and return only completed requested projections. Human rejection and cancellation propagate their existing control outcome. Never append full unprojected output as an error fallback.

Hook context is separate from the 16 KiB projected-value budget. Existing limits allow up to 1 MiB stdout per hook and concatenate contexts; there is no small aggregate hook-context cap. Preserve that behavior rather than claiming the projection budget bounds the whole response; it must not disappear because projection omitted fields. Bound counts/status diagnostics independently and measure their actual serialized overhead. No composition retries are added; retain the registry's existing retry only when the request was provably not sent.

### 5. Deterministic quality gate

Use at least three checked-in fake or sanitized recorded workflows: list-to-detail, multiple status reads, and nested JSON with large irrelevant fields. Keep every successful response below existing ordinary MCP caps. Pin inputs, required answer fields, provider configuration and deferred activation state. Replay the same child request sequence through ordinary dispatch and composition; compare complete JSON arguments and required projected facts exactly, including numeric IDs, nulls and provenance. Negative fixtures exercise permissions, hooks, limits, errors and cancellation. Include a large-hook-context fixture in the volume report so shared hook output is not hidden from the cost comparison.

For each workflow, serialize the Claude request immediately before a scripted final answer, after the identical starting transcript and completed reads. Define `B` as the UTF-8 bytes of ordinary tool-result contents and `C` as the outer composed result content including envelope and hook context. Each positive workflow must satisfy `C <= 0.20 * B`. Also report the full serialized request delta, including tool schema and plan-input overhead; the aggregate full-request bytes must decrease. Report the deterministic number of intermediate model decisions avoided, not claimed wall-clock or token/cost savings. Default-off golden requests must be byte-identical to the baseline, excluding only existing nondeterministic request IDs. These are acceptance targets, not measurements already achieved.

## Risks / Trade-offs

- Text-only eligibility excludes some MCP endpoints → document unsupported outputs and retain ordinary tools for recovery; do not weaken filtering to increase coverage.
- Dispatch extraction affects ordinary calls → establish parity tests before adding composition and retain existing regression suites.
- Read-only annotations can be inaccurate → exact operator-reviewed endpoint list plus per-call grants/permissions; never infer safety from a tool's name.
- Hidden intermediate information can omit facts a task needs → explicit selection, provenance, row-limit indicators and curated answer-field equivalence gate.
- Large upstream responses allocate before rejection → document this limit; do not claim streaming or hard network-memory containment.
- Synthetic fixtures cannot establish production task quality → permit only one explicitly configured pilot agent after the gate, without global defaults.

## Migration Plan

Implement correctness prerequisites, dispatch parity, then the default-off tool/config/schema and fixtures. Update configuration, tools, MCP and permissions documentation with unsupported cases and the exact opt-in. Run schema checks, targeted suites and repository final checks. Record the fixture report before requesting pilot rollout. No runtime or workspace opt-in is part of this planning change. Disable a pilot by removing its exact tool grant or emptying `readTools`; no stored composition state needs migration.

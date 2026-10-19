## 1. Run metadata

- [x] 1.1 `langfuse.SetTraceMetadata`: trace metadata on a root, observation metadata on a child.
- [x] 1.2 `startRunTrace` stamps `max_turns` after `createLangfuseTrace`; `stampTraceMetadata` namespaces and gates on the Langfuse client only.
- [x] 1.3 `processGeneration`: `turnsUsed` counted at both `streamAndHandleEvents` call sites; `turns_used` and `finish_reason` stamped in the deferred trace closure.
- [x] 1.4 `traceFinishReason` derives the reason from the returned `AgentEvent` and the run context's error; a panic is stamped `error` before it propagates; a terminal output-token stop is `max_tokens`.
- [x] 1.5 Tests: `SetTraceMetadata` root/child/no trace, `traceFinishReason` table, `max_turns` on root and child traces, an end-to-end run ending naturally, one exhausting its budget, one whose background-task wait is cancelled or hits its deadline, one that panics, and one ending on (or re-entering past) a `max_tokens` stop.

## 2. Flow tag on POST /flow

- [x] 2.1 `flowStartOptions.telemetryFlow` set by the POST /flow handler; `flowTags` renders `flow:<id>`; a flow tag alone publishes the run identity.
- [x] 2.2 Tests: identity tags per POST /flow body, flow-only run publishes and clears its identity and carries none of the previous pooled run's key, user or tags, `--flow` auto-start publishes none, `flowTags` blank/trim, `MergeTags` and `ResolveTags` shadow `flow:none`.

## 3. Docs and verification

- [x] 3.1 `docs/telemetry.md`: Flow Tracing tag, Run Metadata section, namespace table.
- [x] 3.2 `go test ./internal/llm/agent/... ./internal/langfuse/... ./internal/llm/provider/... ./internal/llm/runidentity/... ./internal/api/...`, `go vet`, `gofmt -l`.
- [ ] 3.3 `make test` before merge.

## 1. Run metadata

- [x] 1.1 `langfuse.SetTraceMetadata`: trace metadata on a root, observation metadata on a child.
- [x] 1.2 `startRunTrace` stamps `max_turns` after `createLangfuseTrace`; `stampTraceMetadata` namespaces and gates on the Langfuse client only.
- [x] 1.3 `processGeneration`: `turnsUsed` counted at both `streamAndHandleEvents` call sites; `turns_used` and `finish_reason` stamped in the deferred trace closure.
- [x] 1.4 `traceFinishReason` derives the reason from the returned `AgentEvent`.
- [x] 1.5 Tests: `SetTraceMetadata` root/child/no trace, `traceFinishReason` table, `max_turns` on root and child traces, an end-to-end run ending naturally and one exhausting its budget.

## 2. Flow tag on POST /flow

- [x] 2.1 `flowStartOptions.telemetryFlow` set by the POST /flow handler; `flowTags` renders `flow:<id>`; a flow tag alone publishes the run identity.
- [x] 2.2 Tests: identity tags per POST /flow body, flow-only run publishes and clears its identity, `--flow` auto-start publishes none, `flowTags` blank/trim, `MergeTags` and `ResolveTags` shadow `flow:none`.

## 3. Docs and verification

- [x] 3.1 `docs/telemetry.md`: Flow Tracing tag, Run Metadata section, namespace table.
- [x] 3.2 `go test ./internal/llm/agent/... ./internal/langfuse/... ./internal/llm/provider/... ./internal/llm/runidentity/... ./internal/api/...`, `go vet`, `gofmt -l`.
- [ ] 3.3 `make test` before merge.

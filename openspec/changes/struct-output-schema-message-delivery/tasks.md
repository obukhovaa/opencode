# Tasks: struct-output-schema-message-delivery

## 1. Tool surface

- [x] 1.1 `internal/llm/tools/struct_output.go`: add `SchemaDelivery` (`message` /
  `tool`), `NewStructOutputToolWithDelivery`; keep `NewStructOutputTool` as the
  `tool`-mode shim
- [x] 1.2 Static `Info()` for `message` mode: fixed description + `{"output": {"type":
  "object"}}`, required `["output"]`
- [x] 1.3 `Run`: lenient unwrap of `output` (wrapped, flat, or error result), then the
  existing defaults + required validation against the full schema
- [x] 1.4 Schema fingerprint (`sha256` over `json.Marshal`, 12 hex) and envelope
  renderer

## 2. Injection

- [x] 2.1 `internal/llm/agent/tools.go`: resolve the delivery mode and pass it to the
  tool constructor
- [x] 2.2 `internal/llm/agent/agent.go`: hold the resolved schema + envelope on the
  agent; `injectStructOutputSchema` mirroring `injectDeferredDelta`
- [x] 2.3 Wire it into the run path after `createUserMessage`, before the first
  generation

## 3. Config

- [x] 3.1 `internal/config/config.go`: top-level + per-agent `structOutputSchemaDelivery`
- [x] 3.2 `cmd/schema/main.go`: declare the field and its enum; add both values to the
  `probe` corpus in `cmd/schema/main_test.go`
- [x] 3.3 `make schema` and commit `opencode-schema.json`
- [x] 3.4 `internal/config/` viper round-trip test for the new field

## 4. Tests

- [x] 4.1 Tool: identical `ToolInfo` across differing schemas; `tool` mode unchanged
- [x] 4.2 Tool: wrapped / flat / neither payload shapes; defaults + required still
  enforced
- [x] 4.3 Provider: two requests differing only in output schema produce byte-identical
  `tools` and `system` blocks with unchanged breakpoint positions
- [x] 4.4 Agent: inject once; skip on matching fingerprint; re-inject on differing
  fingerprint (fork); re-inject after compaction; inject on auto-resume
- [x] 4.5 `scripts/test/` e2e covering a two-step flow with differing schemas

## 5. Docs

- [x] 5.1 `docs/flows.md`, `docs/agents.md`, `AGENTS.md`
- [x] 5.2 `make test`

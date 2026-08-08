# Tasks: deferred-tools

## 1. Config & capability surface

- [ ] 1.1 Add `DeferredTools map[string]bool` to `config.Agent` (`internal/config/config.go`) and `AgentInfo` (`internal/agent/registry.go`, yaml-tagged); merge in `applyConfigOverrides` and `mergeMarkdownIntoExisting` (mirror the `Tools` `maps.Copy` blocks at registry.go:456, :520)
- [ ] 1.2 Declare `deferredTools` in `cmd/schema/main.go` (both the `agents.*` and standalone agent defs); regenerate `opencode-schema.json`
- [ ] 1.3 Add `IsToolDeferred(name string, cfg map[string]bool) bool` to `internal/permission/evaluate.go`: case-insensitive exact > wildcard (reuse `MatchWildcard`), hard exclusions `toolsearch`/`struct_output`, nil map ⇒ false
- [ ] 1.4 Viper round-trip test in `internal/config/` proving uppercase `deferredTools` keys still match after case-folding (per CLAUDE.md map-key rule)
- [ ] 1.5 Add `SupportsToolSearch` to `models.Model`; set true for Claude models on anthropic/bedrock/vertexai; false for kimi, openai-compatible, gemini families

## 2. Tool layer

- [ ] 2.1 Add `IsDeferred() bool` to `BaseTool`; `return false` on all concrete tools (incl. `mcpTool`, `agentTool`)
- [ ] 2.2 `deferredWrapper` in `internal/llm/tools/tools.go`: `inner BaseTool` + `active atomic.Bool`; delegates everything, `IsDeferred() == !active`, `Activate()`
- [ ] 2.3 Wrap matching tools in `NewToolSet` (builtin, MCP, and LSP delivery paths); track has-deferred; register `toolsearch` only when deferred tools exist AND the resolved model lacks `SupportsToolSearch`
- [ ] 2.4 Implement `internal/llm/tools/toolsearch.go`: exact / `select:` / `+term` / scored keyword matching over deferred tools via the agent's resolved toolset; activation; `<system-reminder>`-wrapped contract output; no-match lists deferred names; excludes already-activated tools

## 3. Providers

- [ ] 3.1 anthropic.go native path (model `SupportsToolSearch` + agent has deferrals): full schema + `DeferLoading: true` on deferred tools (flag permanent), append `tool_search_tool_regex_20251119` server tool, move tools cache breakpoint to last non-deferred tool
- [ ] 3.2 Persist + replay `server_tool_use` / `tool_search_tool_result` / `tool_reference` message parts (mirror thinking-block-replay); mark wrapper activated when a replayed reference names its tool
- [ ] 3.3 anthropic.go fallback branch (kimi & other non-flagged models on this client): skip non-activated deferred tools, append activated ones after the stable ordering
- [ ] 3.4 openai.go / gemini.go: same skip/append-after-prefix behavior in `convertTools`
- [ ] 3.5 Unit tests per provider: no-config byte-identical payloads; deferred flags/skips; breakpoint placement on last non-deferred tool; append-only activation ordering

## 4. Prompt & agent loop

- [ ] 4.1 `prompt.go`: when the agent defers ≥1 tool, append the `<system-reminder>` convention explainer + deferred-builtin-names block (config-computed, cache-stable); absent otherwise
- [ ] 4.2 Agent loop: announced-set tracking + MCP delta user message injected only when the deferred pool changes (after `resolveTools`, re-checked per outer turn)
- [ ] 4.3 Tests: prompt block presence/absence; delta injected exactly once per pool change; interaction with lean-prompts budget tests (block exempt — dynamic)

## 5. End-to-end & docs

- [ ] 5.1 Unit-level flow tests: native single-turn (recorded fixture with tool_search results replayed) and fallback two-turn (search → next-request inclusion)
- [ ] 5.2 E2E script under `scripts/test/` exercising fallback activation cross-process (per CLAUDE.md e2e guidance)
- [ ] 5.3 Add superseded banner to `spec/20260405T120000-deferred-tools-and-toolsearch.md` pointing here; update TODO.md reference
- [ ] 5.4 Docs: CLAUDE.md agent-fields entry for `deferredTools` (+ never-defer exclusions, case-insensitivity), `docs/` agent configuration section
- [ ] 5.5 `make test` + `make test-e2e` green; record before/after per-turn input tokens on an MCP-heavy agent (Langfuse) in the PR description

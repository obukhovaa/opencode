# Cap webfetch Output Size

## Why

`webfetch` is the only tool in the harness that can inject an unbounded, model-visible payload into the context with no per-call ceiling. It reads up to 5MB of a response body, converts it (HTML → markdown by default) and returns the **entire** result via `NewTextResponse`. The only guard is the global `MaxToolResponseTokens = 300_000` backstop (~1.2MB) — head-only truncation with no way to recover the rest. A modern documentation page converts to hundreds of KB of markdown, so two or three fetches can consume a whole context window.

This is not hypothetical. Langfuse trace [`f486ce10…0320daa`](https://langfuse.de-prod.cxense.com/project/cmmvv6oi3000bui07fzm1qx7f/traces?peek=f486ce107382427f871e7f77c0320daa) — session `1789984476-developer-review-merge-request-resolve-team`, flow `developer-review-merge-request`, step `review-code`, agent `piano-code-reviewer` on `eu-claude-sonnet-5`, opencode v0.19.1 — shows three `webfetch` calls taking the step from 104,914 to 303,851 input tokens:

| # | URL | input tokens before → after | delta |
|---|---|---|---|
| 1 | `gist.github.com/mculp/c082bd1e…` | 104,914 → 110,535 | +5,621 |
| 2 | `code.claude.com/docs/en/plugin-marketplaces` | 110,535 → 150,595 | +40,060 |
| 3 | `code.claude.com/docs/en/settings-reference` | 150,595 → 303,851 | **+153,256** |

~199k tokens — **64% of the step's final 309,606-token context** — came from three tool calls. Re-measuring the same URLs today through the tool's own code path (`io.LimitReader` → `htmltomarkdown.ConvertString`) gives 374KB / ~93k tokens for `settings-reference` (5,615 lines) and 107KB / ~26k tokens for `plugin-marketplaces`; the live pages in September were larger. The agent was looking for two settings keys and paid for 5,615 lines of navigation, prose and code samples — then re-sent all of it on every subsequent turn. The seven generations after the third fetch cost $1.26 of the step's $2.12 (59%), almost entirely re-transmitted page bodies.

The harness already solved this problem twice. The bash tool spills oversized stdout/stderr to a temp file and returns a head+tail preview pointing the agent at the file; `mcp-tool-output-limit` (archived 2026-08-12) gave MCP tool calls the same treatment via the reusable `tools.PersistLargeOutput` helper after an identical production incident. `webfetch` was never wired to it.

## What Changes

- `webfetch` output is capped at a configurable byte budget (default 50KB, matching the bash and MCP caps). Oversized results spill to the per-process scratch directory and are replaced by a header + byte-aligned head+tail preview naming the file path, so the agent explores the page with `grep`/`read`/`sed` instead of carrying it in context — reusing `tools.PersistLargeOutput` unchanged.
- The cap is applied **after** format conversion, so it measures what actually enters the context, and the spilled file holds the same converted text the preview shows — `grep` hits on the preview and the file agree.
- New top-level config object `webFetch` with `maxOutputBytes` (mirrors the existing `webSearch` object): positive overrides the default, negative disables the cap, zero/omitted uses the default.
- The 5MB body limit stops truncating silently. Today `io.ReadAll(io.LimitReader(body, 5MB))` cuts a chunked/compressed response mid-HTML and converts the torn document, and the agent is never told. The tool detects the overflow and says so in the returned text.
- Tool description and `docs/` state the cap and the spill-file workflow; each spill is logged like the MCP one.

## Capabilities

### New Capabilities

- `webfetch-output-limit`: a configurable cap on a single `webfetch` call's context footprint, with overflow spilled to a temp file and replaced by a head+tail preview the agent can explore with its existing file tools, plus explicit reporting when the 5MB body limit truncates the response.

### Modified Capabilities

<!-- none — no existing spec covers webfetch result handling -->

## Impact

- Modified: `internal/llm/tools/webfetch.go` (single capped return path, overflow detection, description), `internal/config/config.go` (`WebFetchConfig`), `cmd/schema/main.go` + regenerated `opencode-schema.json`, `README.md` / `docs/`.
- `.opencode.json` public contract: additive `webFetch.maxOutputBytes` field.
- Behavior change: `webfetch` results over 50KB are previewed + spilled rather than returned inline. Setups that need whole pages inline set `webFetch.maxOutputBytes` to a larger value or to `-1`.
- No new runtime dependencies; `tools.PersistLargeOutput` and the scratch-dir machinery already exist and are covered by tests.

# Design — Cap webfetch Output Size

## Context

- `internal/llm/tools/webfetch.go` `Run` ends in a `switch format` with four independent `NewTextResponse` returns (text / markdown-from-HTML / markdown-passthrough / html / default). Every branch returns the full converted body. There is no size logic anywhere in the file beyond the 5MB read limit.
- `NewTextResponse` runs the global `validateAndTruncate` backstop (`MaxToolResponseTokens = 300_000`, ~1.2MB, head-only, unrecoverable). That is a catastrophe guard, not a context budget.
- `tools.PersistLargeOutput(content, label, source string, maxBytes int) (preview, filePath string)` in `internal/llm/tools/tempdir.go` already does exactly what is needed: pass-through under the cap, otherwise spill the full content to the process scratch dir (`os.CreateTemp`, 0600, sanitized prefix, 100MB on-disk cap, cleaned up at shutdown) and return a header + byte-aligned, rune-safe head+tail preview. It was added by `mcp-tool-output-limit` and is exercised by `tempdir_test.go`.
- The `read` tool joins relative paths against the working directory but accepts absolute paths unchanged (`read.go:92`), so the agent can read a spill file under `os.TempDir()` with no permission or path changes. `grep` and `bash` sed likewise.
- The MCP precedent's config is per-server (`mcpServers.<name>.callToolMaxOutputBytes`). `webfetch` has no per-server axis, so the knob is global — and there is already a top-level `webSearch` object to mirror.

## Goals / Non-goals

- Goal: bound a single `webfetch` call's context footprint by default, without losing data — the full converted page stays on disk and the agent is told how to query it.
- Goal: reuse `PersistLargeOutput` verbatim; do not invent a second spill path or a second preview format.
- Goal: stop the 5MB read limit from silently producing a torn document.
- Non-goal: content extraction / readability (stripping nav, footers, sidebars). Measured on the incident's own URL this buys nothing: `script`/`style` are already dropped by the converter, a `main, article, [role=main]` selector matches nothing on the client-rendered docs site, and the 363KB is genuine page content. A cap the agent can grep past is the robust answer; extraction is a separate, best-effort optimization if we ever want it.
- Non-goal: touching `MaxToolResponseTokens` (stays, on top).
- Non-goal: per-agent or per-URL caps. One global knob, same as the other output caps, until there is a case for more.

## Key decisions

### 1. Cap after conversion, spill the converted text

The cap is applied to the **final formatted string**, not the raw body, for two reasons: the number that matters is what enters the context (a 433KB HTML page becomes 374KB of markdown — different budgets), and the spilled file must contain the same text the preview shows, or the agent's `grep` on the file would search different content than it was shown.

Mechanically this means collapsing the four `NewTextResponse` returns in the `switch` into one: each branch computes `out string` (and may return early on a conversion error), then a single tail applies the cap and returns. This also removes the existing asymmetry where the `text` branch returns whitespace-collapsed text and the markdown passthrough wraps content in a fenced block — both now flow through the same accounting.

### 2. Default 50KB, global `webFetch.maxOutputBytes`

`WebFetchConfig{ MaxOutputBytes int }` on `Config` as `webFetch`, mirroring `WebSearchConfig`. Resolution mirrors `resolveCallToolMaxOutputBytes` exactly: `> 0` → use it; `< 0` → unlimited; `0`/omitted → `webFetchMaxOutputBytes = 50 * 1024`.

50KB ≈ 12.5k tokens: generous for an article or an API response, small against any context window, and identical to the bash and MCP thresholds so operators learn one number. On the incident's pages it turns 93k and 26k tokens into ~12.5k each. It is one config line away from any other value, so the choice is cheap to revise.

A **new object** rather than a bare `webFetchMaxOutputBytes` scalar keeps room for the knobs this tool will plausibly grow (timeout default, user agent, allow/deny host lists) without another top-level key each time.

### 3. Head+tail preview is kept as-is, deliberately

`PersistLargeOutput` splits the budget 50/50 head and tail. For a web page the head is partly chrome and the tail is partly footer, so a head-weighted split is tempting — but it would mean a new parameter, a second code path in a helper shared with MCP, and a preview whose shape depends on the caller. The preview's job here is orientation, not delivery: the header tells the agent to grep the file, and a 25KB head is already several screens of real content on any page whose nav is not enormous. If profiling later shows the tail is consistently worthless we can add a weighting parameter behind one call site; it is not worth coupling this change to it.

### 4. Report the 5MB truncation instead of hiding it

`io.ReadAll(io.LimitReader(resp.Body, maxSize))` silently returns exactly 5MB when the body is larger and `Content-Length` was absent (chunked or compressed responses — the common case). The tool then converts a document cut mid-tag and returns it as if complete. Fix: read `maxSize+1` bytes, and when the extra byte materializes, trim back to `maxSize` and state in the returned text that the response exceeded the 5MB limit and was truncated at the byte level before conversion. Still not an error — a truncated large page is often usable — but the agent must know the tail is missing before it concludes "the page does not mention X".

### 5. Observability

Log each spill (`logging.Info`: url, format, totalBytes, cap, file), mirroring the MCP spill log, so context-pressure incidents like the one that motivated this are greppable in agent-container logs rather than reconstructed from Langfuse token deltas.

### 6. The read tool's size ceiling shapes the guidance

`read` rejects any file larger than `MaxReadSize` (250KB) on size alone, *before* it looks at `offset`/`limit`, so the pre-existing overflow header — "read specific ranges with the read tool (offset/limit)" — is false for exactly the spills this change creates: a page big enough to trip a 50KB cap is frequently bigger than 250KB. The e2e caught this on its first run against a 423KB spill.

`buildOutputOverflowHeader` is therefore corrected to lead with the tools that have no size ceiling (`grep`, and `sed` in bash) and to name `MaxReadSize` as the limit above which `read` declines. `grep` with its context options is a complete recovery path even for a read-only agent with no bash, which is what makes the capped result usable rather than merely small. The header is shared with the MCP spill path, which has the same defect for multi-MB build logs, so the correction lands there too.

Raising or windowing `MaxReadSize` would be the more ambitious fix and is deliberately **out of scope**: `read` is used by every agent, and letting a windowed read escape the size guard raises its worst-case output from 250KB to ~4MB (2000 lines × 2000 chars), which is a context-budget decision with its own spec and tests. Tracked as a follow-up rather than smuggled in here.

## Risks / mitigations

- **An agent stops at the preview and answers from partial content.** Mitigation: the header explicitly names the file and instructs grep/read/sed, wording already proven with bash and MCP output; and the truncation notice is inside the returned text, not out-of-band.
- **A flow that genuinely needs a whole page inline regresses.** Mitigation: additive config, `-1` restores today's behavior, documented in README and the tool description.
- **Extra round trips.** A capped fetch the agent must then grep costs one or two cheap tool calls against a small file — versus, in the incident, ~150k tokens re-sent on each of seven subsequent turns.

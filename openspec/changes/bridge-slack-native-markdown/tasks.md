# Tasks: bridge-slack-native-markdown

## 1. Shared `internal/bridge/markdown` package

- [x] 1.1 Create `internal/bridge/markdown/markdown.go` (new package, stdlib-only, no
  dependency on `internal/bridge` or any platform SDK — importable by both `slack` and
  `telegram` packages without an import cycle).

- [x] 1.2 Implement `NormalizeSlackLinks(text string) string`: rewrites `<https://x|label>`
  → `[label](https://x)` and bare `<https://x>` → `https://x`. MUST leave `<@U123>` mentions
  and non-URL angle-bracket text (e.g. `a < b && b > c`) untouched. Only match content
  starting with a URL scheme (`http://` / `https://`) inside the angle brackets.

- [x] 1.3 Implement the fence-aware chunker per `design.md § Chunking algorithm`
  (implemented as `Split(text string, limit, maxChunks int, marker string) (chunks
  []string, truncated bool)` — named `Split`, not `SplitMarkdown`, and takes an explicit
  `maxChunks` cap rather than only a budget derivation, per the task's "adjust names for
  idiomatic Go" latitude):
  `SplitMarkdown(text string, limit int, marker string) (chunks []string, truncated bool)`
  (or equivalent signature). Must: (a) prefer blank-line, then heading-line, then
  any-line-boundary cuts, accepting a semantic boundary only if it retains at least half of
  `limit`; (b) never cut inside an open ` ``` ` fence — close and reopen the fence across the
  boundary, preserving the info string; (c) hard-wrap rune-safely (never split a UTF-8
  codepoint) any single line exceeding `limit` on its own; (d) pack the last chunk to the
  limit rather than stopping at a tidy boundary; (e) reserve room for `marker` before the
  final cut when truncating, append the marker, and re-close any fence the final cut broke.

- [x] 1.4 Implement `BuildBlockChunks(text string, payloadBudget, perBlockTarget int,
  marker string) (chunks []string, truncated bool)` (named `BuildBlockChunks`, not
  `SplitMarkdownBudget`): derives `blockCount = ceil(payloadBudget/perBlockTarget)` and
  `perChunkLimit = floor(payloadBudget/blockCount)` (see deviation note below — design.md's
  own worked example uses floor, not the task text's `ceil(budget/blockCount)`, and floor is
  required for the `sum(len(chunks)) <= budgetChars` guarantee this item itself demands),
  per `design.md § Slack budget arithmetic`.

- [x] 1.5 Implement `ToTelegramHTML(text string) string` per `design.md § Telegram GFM → HTML
  mapping`: extract code spans/fences to placeholders first (protect from inline
  transformation), convert headings/bold/italic/strikethrough/links/lists/blockquotes/tables/
  `---`/images per the mapping table, escape `& < >` in prose text runs only, then restore
  placeholders as escaped `<code>`/`<pre>` content.

- [x] 1.6 Define the truncation marker as an exported constant, e.g.
  `const TruncationMarker = "\n\n_\u2026truncated\u2026_"` (italic prose).

- [x] 1.7 Unit tests in `internal/bridge/markdown/markdown_test.go` (deviation: `ToTelegramHTML`
  tests landed in a sibling file, `internal/bridge/markdown/telegram_test.go`, mirroring the
  `telegram.go`/`markdown.go` file split — same package, same `go test` target):
  - `NormalizeSlackLinks`: rewrites `<url|label>` and bare `<url>`; leaves `<@U123>` and
    `a < b && b > c` untouched; table-driven.
  - `SplitMarkdown`/chunker: fits-in-one-chunk fast path; blank-line boundary preferred;
    heading-line boundary when no blank line; line-boundary fallback; fence-spanning split
    closes and reopens the fence with the same info string; hard-wrap of an oversized single
    line never splits a multi-byte rune (test with multi-byte UTF-8 content, e.g. emoji or
    CJK characters, at the wrap boundary); last-chunk packing behavior; truncation marker
    reserved-room arithmetic (marker never pushes a chunk over `limit`); truncation marker
    closes a fence broken by the re-cut.
  - `SplitMarkdownBudget`: derived per-chunk limit arithmetic matches
    `design.md`'s worked example (12,000 / 3,000-target → 4 chunks of 3,000); sum of chunk
    lengths never exceeds the budget.
  - `ToTelegramHTML`: each row of the GFM→HTML mapping table in `design.md` as a distinct
    test case; escaping test for `& < >` in prose without corrupting emitted tags (the
    `a < b && b > c` scenario from the spec); code span/fence content is escaped for `&<>`
    but not further transformed (e.g. underscores inside a code span are not turned into
    `<i>`).

## 2. Slack adapter: markdown-block prose rendering

- [x] 2.1 In `internal/bridge/slack/adapter.go`'s `Send` (~lines 885-947): replace the
  `slackgo.MsgOptionText(text, false)` call for the prose text part with construction of one
  or more `slackgo.NewMarkdownBlock("", chunk)` blocks via
  `internal/bridge/markdown.BuildBlockChunks` (12,000-char budget, 3,000-char per-block
  target, `internal/bridge/markdown.TruncationMarker` via the local
  `markdownTruncationMarker` alias). Pass the resulting blocks via
  `slackgo.MsgOptionBlocks(...)`. Keep the top-level `text` field set (as the notification
  fallback) alongside the blocks — do not remove it.

- [x] 2.2 Call `internal/bridge/markdown.NormalizeSlackLinks` on the outbound text before
  chunking (done inside `BuildBlockChunks` itself), so legacy `<url|label>` mrkdwn link
  syntax an agent might emit is rewritten to `[label](url)` before block construction.

- [x] 2.3 Enforce the 50-block-per-message cap defensively (e.g. an assertion/clamp after
  chunking) so a future change to the budget constants cannot silently exceed Slack's block
  count ceiling.

- [x] 2.4 Add a per-`Adapter`-instance sticky latch (implemented as
  `markdownBlocksUnsupported atomic.Bool`, following the existing `lastError`/`lastFailureAt`
  pattern on `Adapter`). When set, `Send` skips markdown-block construction entirely and
  calls `slackgo.MsgOptionText` directly.

- [x] 2.5 Detect block-related API errors from `PostMessageContext` (error string/code
  matching `invalid_blocks`, `invalid_block`, `invalid_arguments`, `blocks_too_long`,
  `msg_blocks_too_long`, `invalid_block_id` via `isBlockError`). On match: log a one-shot WARN
  (`logging.Warn("bridge: slack markdown blocks rejected, falling back to plain text", ...)`
  — only once per latch transition, not per send, via `atomic.Bool.CompareAndSwap`), set the
  latch, and retry the same logical send as plain text via `MsgOptionText` before returning
  the `Send` result.

- [x] 2.6 Verify `truncateRunes` (existing, ~lines 973-989) and `MaxTextLength = 39_000`
  (existing, ~line 28) remain applied to the top-level `text` field exactly as today — this
  change does not alter the notification-fallback truncation behavior, only the blocks path.

- [x] 2.7 (Follow-up refinement, added during the Telegram half of this change) Split the
  latch-triggering predicate from the retry-triggering predicate: `isBlockError` (retry-only,
  keeps `invalid_arguments`) stays broad because a false positive there just costs one extra
  plain-text send; a new `isBlockCapabilityError` (latch-only, excludes `invalid_arguments`)
  gates `markdownBlocksUnsupported.CompareAndSwap` so an ambiguous, possibly-unrelated error
  code can no longer permanently downgrade formatting for the identity. Covered by
  `TestSendAmbiguousBlockErrorRetriesWithoutLatching` in `adapter_markdown_test.go`.

## 3. Telegram adapter: HTML prose rendering

- [x] 3.1 In `internal/bridge/telegram/adapter.go`'s `Send` (~lines 849-884): replace the
  `chunkText(text, MaxTextLength)` call (rune-count chunking with no markdown awareness) with
  `internal/bridge/markdown.SplitMarkdown` at a conservative source-level limit (~3,500
  characters) plus `internal/bridge/markdown.TruncationMarker` for any content dropped beyond
  what the adapter chooses to send.

- [x] 3.2 For each source chunk, convert via `internal/bridge/markdown.ToTelegramHTML` and
  send with `&tgbot.SendMessageParams{ChatID: chatID, Text: htmlChunk, ParseMode:
  models.ParseModeHTML}` — replacing the current no-`ParseMode` call.

- [x] 3.3 Detect entity/parse errors from `SendMessage` (error string matching `can't parse
  entities` or equivalent go-telegram/bot error surface). On match: retry the same chunk
  (original markdown text, unconverted) with no `ParseMode` field set, before returning the
  `Send` result for that chunk. Do NOT set any sticky/latched state — every message's chunks
  attempt `ParseModeHTML` fresh.

- [x] 3.4 Confirm `chunkText` (existing, ~lines 926-960) is either removed if no longer used
  by any caller, or retained only if still referenced elsewhere (e.g. by
  `SendQueuedAck`/`UpdateQueuedAck`, which per the proposal's out-of-scope section stay plain
  text) — check all call sites before deleting. (`chunkText`'s only caller was `Send`; removed.
  `SendQueuedAck`/`UpdateQueuedAck` were not routed through it and remain unchanged.)

- [x] 3.5 Verify `MaxTextLength = 4096` (existing, ~line 31) is still respected as the hard
  ceiling for each `sendMessage` call's *post-conversion* text — the ~3,500-character
  source-level chunk limit exists specifically to keep the converted output under this cap
  with margin for HTML tag overhead.

## 4. Tests: Slack adapter

- [x] 4.1 `internal/bridge/slack/adapter_test.go` (existing file, add cases) or a new
  `internal/bridge/slack/adapter_markdown_test.go`:
  - `Send` with GFM prose (headings, bold, list, link, fenced code) produces a
    `chat.postMessage` call whose blocks include `markdown`-type blocks with the expected
    (link-normalized) text; assert against the mock/httptest Slack API used by existing
    adapter tests.
  - `Send` with text exceeding the 12,000-character budget produces blocks whose combined
    length stays within budget and whose last block ends with `TruncationMarker`.
  - `Send` where the mock API returns an `invalid_blocks`-class error on the blocks attempt:
    assert a subsequent plain-text `MsgOptionText` call is made and `Send` reports delivered.
  - A second `Send` call on the same `Adapter` after the latch is set: assert no blocks
    attempt is made (mock records only a plain-text call), and no duplicate WARN log fires
    for the same latch.
  - `Send` with `<url|label>` legacy link syntax: assert the posted block text contains
    `[label](url)` (or the equivalent post-normalization form), not the raw mrkdwn syntax.

## 5. Tests: Telegram adapter

- [x] 5.1 `internal/bridge/telegram/adapter_test.go` (existing file, add cases) or a new
  `internal/bridge/telegram/adapter_markdown_test.go` (landed as the new file):
  - `Send` with GFM prose produces a `sendMessage` call with `ParseMode: ParseModeHTML` and
    text matching the expected HTML mapping (bold heading, `<b>`, `<a href>`, `<pre><code
    class="language-...">`, etc.) per the mapping table in `design.md`.
  - `Send` with text longer than the ~3,500-character source chunk limit, containing a fence
    that straddles the cut, produces two (or more) `sendMessage` calls, each independently
    valid HTML, each under Telegram's 4,096-character cap, with the fence correctly closed on
    the first chunk and reopened on the second.
  - `Send` where the mock bot API returns a `can't parse entities`-class error for one chunk:
    assert a retry `sendMessage` call is made for that chunk with no `ParseMode` field and
    the original markdown text; assert the *next* message's `Send` still attempts
    `ParseModeHTML` (no sticky latch).
  - Escaping test: outbound text containing literal `<`, `>`, `&` outside of any markdown
    construct is escaped correctly in the sent HTML without corrupting adjacent emitted tags.

## 6. Confirm out-of-scope paths are untouched

- [x] 6.1 Confirm (via `git diff` review, not code edits) that
  `internal/bridge/slack/render.go` (the `RichRenderer` Block Kit path for tool calls,
  lists, tables, status) has zero changes.
- [x] 6.2 Confirm `internal/bridge/telegram/render.go` (the `RichRenderer` path using
  `ParseModeMarkdownV1`) has zero changes.
- [x] 6.3 Confirm `internal/bridge/mattermost/adapter.go` has zero changes.
- [x] 6.4 Confirm `SendQueuedAck` / `UpdateQueuedAck` on both the Slack and Telegram adapters
  (`slack/adapter.go:994-1033`, and the Telegram equivalent) are NOT routed through the new
  markdown rendering — they continue sending plain short status strings unchanged.
- [x] 6.5 Confirm `internal/bridge/service/dispatch.go`'s `handleTerminalEvent` and
  `agentMessageText` (~lines 585-640) have zero changes — this change only affects how each
  adapter's `Send` renders the `Outbound.Text` it already receives.
- [x] 6.6 Confirm no `Config` struct field, `cmd/schema/main.go` entry, or
  `opencode-schema.json` change is introduced — this change has no `.opencode.json` surface.
  No schema regeneration step is required because no `Config` field changes.

## 7. Tests + Verification

- [x] 7.1 `go test -short ./internal/bridge/...` passes, including the new
  `internal/bridge/markdown` package tests and the updated Slack adapter tests (Telegram
  adapter tests, including the new `ToTelegramHTML`/`Send` HTML-path coverage, pass as-is).
- [x] 7.2 `go test -race ./internal/bridge/...` passes (no data race from the new
  per-adapter `atomic.Bool` latch or any shared state in `internal/bridge/markdown`).
- [x] 7.3 `make test` run for this change; see the workhorse report for the verbatim output.
  `opencode-schema.json` and generated mocks are unchanged (`git status --short` confirms —
  this change introduces no `Config` field).
- [x] 7.4 `./scripts/check_hidden_chars.sh` passes (no hidden Unicode introduced by the
  truncation marker's ellipsis character or any other new string constants — verify the
  marker uses an explicit `\u2026` escape rather than a literal `…` glyph, or confirm the
  script's allowlist covers it).
- [x] 7.5 `go vet ./internal/bridge/...` clean.

## 8. Docs

- [x] 8.1 `docs/bridge.md`: document outbound prose rendering per platform (Slack Block Kit
  `markdown` blocks + latch, Telegram HTML + per-chunk fallback, Mattermost unchanged) and note
  the `RichRenderer` tool-card paths are separate and untouched.

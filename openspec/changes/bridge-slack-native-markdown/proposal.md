## Why

The bridge sends the agent's final assistant text to Slack via
`slackgo.MsgOptionText(text, false)` (`internal/bridge/slack/adapter.go:901`) — Slack's
top-level `text` field, which is parsed as **mrkdwn**, a dialect that is NOT GitHub-Flavored
Markdown (GFM). LLM output is GFM. Observed in production: `## headings`, `**bold**`, `- `
bullets, `[label](url)` links, and pipe tables all render as literal characters; only
backtick code spans survive by coincidence (both dialects use single backticks for inline
code). Mattermost renders the identical text correctly today because `Post.Message` is
parsed as real GFM server-side. Telegram has the mirror-image bug: `Send`
(`internal/bridge/telegram/adapter.go:849`) sets **no** `ParseMode` at all, so every markup
character — including code fences — renders verbatim as plain text.

## What Changes

- **Slack: post agent prose as Block Kit `markdown` blocks.** Slack's `markdown` block type
  (`{"type":"markdown","text":...}`) is parsed as real standard Markdown server-side —
  distinct from the legacy mrkdwn dialect used by `text` and `section` blocks. The adapter
  builds one or more `slack.NewMarkdownBlock(...)` blocks (already available in
  `github.com/slack-go/slack v0.25.0`, no new dependency) instead of passing raw text to
  `MsgOptionText`. The top-level `text` field is still sent alongside the blocks as the
  notification / accessibility fallback.
- **Telegram: convert GFM to Telegram HTML (`ParseModeHTML`), not MarkdownV2.** Telegram
  HTML supports a small fixed tag set (`<b> <i> <u> <s> <a href> <code> <pre>
  <blockquote>`) and requires escaping only three characters (`& < >`), making it far more
  robust than MarkdownV2's ~18 reserved characters. `internal/bridge/telegram/render.go`
  already documents why MarkdownV2 was rejected for the `RichRenderer` path; this change
  extends that same reasoning to the `Send` prose path.
- **New shared package `internal/bridge/markdown` (stdlib-only).** Provides:
  `NormalizeSlackLinks` (rewrites legacy `<url|label>` / `<url>` mrkdwn link syntax some
  agents emit into `[label](url)` / bare `url`, without touching `<@U123>` mentions or
  incidental `<`/`>` characters), a fence-aware chunker shared by both platforms (never
  splits inside a ` ``` ` fence; closes and reopens fences across chunk boundaries; packs the
  final permitted chunk to the limit instead of stopping at a tidy boundary; hard-wraps
  rune-safely any single line that exceeds the limit on its own), a visible truncation
  marker appended when content is dropped, and `ToTelegramHTML` (the GFM→HTML converter,
  which protects code spans/fences from inline transformation before escaping `& < >` in
  prose runs). This package has no dependency on `internal/bridge` (which is deliberately
  dependency-free) and is imported by both the `slack` and `telegram` adapter packages
  without introducing an import cycle.
- **Slack payload budget:** 12,000 characters cumulative across all `markdown` blocks in one
  `chat.postMessage` call (Slack's documented per-payload limit for the block type), a
  3,000-character per-block target, and a hard cap of 50 blocks per message (Slack's
  overall block-count limit). The chunk limit is derived by dividing the budget into
  `ceil(budget/3000)` equal parts so the sum across emitted blocks never exceeds the budget.
- **Telegram chunk budget:** the *source* markdown is chunked at a conservative ~3,500
  characters so that neither the raw chunk nor its HTML-converted, entity-expanded output
  can exceed Telegram's 4,096-character `sendMessage` limit; each chunk is a self-contained,
  independently valid markdown fragment (fences closed/reopened across chunk boundaries) so
  conversion per chunk is correct in isolation.
- **Resilience — a formatting failure MUST NEVER drop a message on either platform.**
  - Slack: if `chat.postMessage` fails with a block-related API error (`invalid_blocks`,
    `invalid_block`, `invalid_arguments`, `blocks_too_long`, or similar), the adapter
    transparently retries the *same* send as plain text (today's behavior), logs a one-shot
    WARN, and latches a per-adapter flag so subsequent sends on that adapter instance skip
    the blocks path entirely — a workspace/app that cannot render `markdown` blocks degrades
    permanently to plain text rather than failing repeatedly.
  - Telegram: if `sendMessage` fails with an entity/parse error (e.g. `can't parse
    entities`), the adapter retries that one chunk, unchanged, with no `ParseMode` and the
    original markdown text. This is content-specific (a single malformed chunk, not a
    platform-wide capability gap), so there is no sticky latch — every message gets a fresh
    attempt at HTML rendering.

### Out of scope

- The Slack `RichRenderer` tool-card path (`internal/bridge/slack/render.go`) and the
  Telegram `RichRenderer` (`internal/bridge/telegram/render.go`) are unchanged — they
  hand-author valid per-platform markup already and are not affected by this change.
- `SendQueuedAck` / `UpdateMessage` short status strings stay plain text on both platforms.
- Mattermost is unchanged — its existing GFM rendering via `Post.Message` is already
  correct.
- No file-upload-on-truncation fallback. `c2-agent`'s orchestrator has one; this change
  accepts a visible truncation marker (`_…truncated…_`) instead, to keep the blast radius
  small.
- No new `.opencode.json` config field. The corrected rendering is always on with automatic
  per-platform degradation on failure; there is no opt-out switch.

## Capabilities

### Modified Capabilities

- `chat-bridge-adapters`: amends the Slack adapter requirement so prose outbound text is
  rendered as GFM via Block Kit `markdown` blocks (with plain-text fallback on
  block-rendering failure), and amends the Telegram adapter requirement so prose outbound
  text is rendered as GFM converted to Telegram HTML (with plain-text fallback on
  parse-entity failure). Adds the shared chunking/truncation-marker contract both adapters
  must honor.

## Impact

**`github.com/opencode-ai/opencode`**

- `internal/bridge/markdown/` (new package): `NormalizeSlackLinks`, the fence-aware
  chunker, the truncation marker, `ToTelegramHTML`. Stdlib only — no new dependency.
- `internal/bridge/slack/adapter.go` (`Send`, ~lines 882-947): replace
  `slackgo.MsgOptionText(text, false)` for the prose text part with `markdown` block
  construction; add the block-error detection, plain-text retry, and per-adapter sticky
  latch.
- `internal/bridge/telegram/adapter.go` (`Send`, ~lines 849-884; `chunkText`, ~926-960):
  replace the plain `chunkText` + no-`ParseMode` `sendMessage` with markdown-aware chunking
  (source-level, via `internal/bridge/markdown`) + `ToTelegramHTML` conversion +
  `models.ParseModeHTML`; add the per-chunk entity-error retry with no `ParseMode`.
- `internal/bridge/slack/render.go`, `internal/bridge/telegram/render.go`: unchanged (out of
  scope, confirmed by reading — the `RichRenderer` paths hand-author valid markup already).
- `internal/bridge/mattermost/adapter.go`: unchanged (out of scope).
- `internal/bridge/service/dispatch.go` (`handleTerminalEvent`, `agentMessageText`):
  unchanged — these produce the `Outbound.Text` this change formats differently at the
  adapter tier; no change to the dispatcher itself.
- No new dependency: `github.com/slack-go/slack v0.25.0` (already in `go.mod`) already
  provides `slack.NewMarkdownBlock` / `slack.MBTMarkdown`; Telegram HTML uses
  `models.ParseModeHTML`, already present in `github.com/go-telegram/bot`.
- No `.opencode.json` / `Config` field changes — no `cmd/schema/main.go` or
  `opencode-schema.json` update is required by this change.
- No database schema change.
- `docs/bridge.md`: out of scope for this planning change per the task boundary (planning
  artifacts only); a follow-up implementation PR should note the corrected rendering
  behavior there.

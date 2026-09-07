## Purpose

Delta spec for the `chat-bridge-adapters` capability. Amends the Slack and Telegram adapter
requirements so outbound prose text (the agent's GFM-formatted assistant messages) renders
correctly on each platform instead of showing literal markdown syntax, and adds the shared
chunking/truncation and failure-degradation contract both adapters must honor. The
`RichRenderer` requirements (tool-call cards, lists, tables, status) and the Mattermost
adapter requirement are unaffected.

## MODIFIED Requirements

### Requirement: Slack adapter via slack-go/slack

The Slack adapter SHALL use `github.com/slack-go/slack` Socket Mode. The adapter MUST
handle: `app_mention`, `message.im`, file uploads. Files via `files.upload`. Socket Mode
handshake retries and reconnects MUST rely on the library's built-in behavior rather than
re-implementing in-process retry logic.

**Outbound prose text MUST be rendered as GitHub-Flavored Markdown (GFM), not the legacy
mrkdwn dialect.** `chat.postMessage` calls for agent-authored prose (the `Send` method's
text part) SHALL construct one or more Block Kit `markdown` blocks
(`slack.NewMarkdownBlock`, `type: "markdown"`) from the outbound text, rather than passing
the text only through the top-level `text` field. The `markdown` block type is parsed by
Slack as standard Markdown server-side — headings, bold/italic, strikethrough, ordered and
unordered lists (including task lists), links, blockquotes, syntax-highlighted fenced code
blocks, tables, and `---` dividers all render correctly, unlike mrkdwn which recognizes none
of these except single-backtick inline code and `>` blockquotes. The top-level `text` field
MUST still be set (as the notification/accessibility fallback) alongside the blocks.

Before block construction, outbound text SHALL be passed through
`internal/bridge/markdown.NormalizeSlackLinks`, which rewrites legacy mrkdwn link syntax
(`<https://x|label>` → `[label](https://x)`, `<https://x>` → `https://x`) that some agents
emit, without altering `<@U123>`-style mentions or unrelated angle-bracket text. Text SHALL
be split into `markdown` blocks using the shared fence-aware chunker
(`internal/bridge/markdown`), respecting a cumulative 12,000-character budget across all
`markdown` blocks in one `chat.postMessage` call and a 50-block-per-message cap, with a
visible truncation marker appended when content is dropped.

If `chat.postMessage` with `markdown` blocks fails with a block-related API error
(`invalid_blocks`, `invalid_block`, `invalid_arguments`, `blocks_too_long`,
`msg_blocks_too_long`, or `invalid_block_id`), the adapter MUST transparently retry the same
send as plain text via the pre-existing `MsgOptionText` path. Only the unambiguous subset of
that same list — MINUS `invalid_arguments` — MUST also set a per-`Adapter`-instance sticky
latch (with a one-shot WARN) so subsequent `Send` calls on that adapter instance skip the
`markdown`-block path and go directly to plain text; `invalid_arguments` is excluded from the
latch because Slack also returns it for reasons unrelated to block support (e.g. a bad
channel or timestamp), and latching on it would permanently downgrade formatting for the
identity based on an ambiguous signal. A message MUST NEVER be dropped because of a
block-construction or block-rendering failure.

#### Scenario: Bot mentioned in channel

- **WHEN** the Slack `app_mention` event arrives for the configured bot identity
- **THEN** the adapter normalizes the event to an `Inbound` (stripping the bot mention) and forwards it to the orchestrator

#### Scenario: File attached to inbound message

- **WHEN** a Slack message includes a file attachment
- **THEN** the adapter downloads the file to the bridge media store and the agent receives the local path as an attachment

#### Scenario: GFM prose renders correctly via markdown blocks

- **GIVEN** the agent's outbound text contains `## Summary`, a `**bold**` phrase, a
  `- bullet` list, a `[link](https://example.com)`, and a fenced ` ```go ` code block
- **WHEN** `Send` posts this text to a Slack channel
- **THEN** `chat.postMessage` is called with one or more `markdown`-type blocks containing
  the text verbatim (after link normalization); Slack renders a real heading, bold text, a
  bulleted list, a clickable link, and a syntax-highlighted code block — none of the source
  markdown characters are visible literally in the rendered message

#### Scenario: Legacy mrkdwn link syntax is normalized before block construction

- **GIVEN** the agent's outbound text contains `<https://example.com|click here>`
- **WHEN** `Send` posts this text
- **THEN** `NormalizeSlackLinks` rewrites it to `[click here](https://example.com)` before
  the `markdown` block is constructed, so the `markdown` block parser (which does not
  understand mrkdwn's `<url|label>` syntax) renders it as a proper hyperlink

#### Scenario: User mentions are left untouched by link normalization

- **GIVEN** the agent's outbound text contains `<@U12345>` and the literal comparison
  `a < b && b > c`
- **WHEN** `NormalizeSlackLinks` processes the text
- **THEN** `<@U12345>` and `a < b && b > c` are returned unchanged — neither is mistaken for
  URL link syntax

#### Scenario: Block-related API error falls back to plain text and latches

- **GIVEN** a Slack workspace/app whose `chat.postMessage` rejects `markdown` blocks with an
  `invalid_blocks` error
- **WHEN** `Send` is called with agent prose
- **THEN** the adapter retries the same send as plain text via `MsgOptionText`, the message
  is delivered, a one-shot WARN is logged, and a per-adapter latch is set

#### Scenario: Latched adapter skips the blocks path on subsequent sends

- **GIVEN** the per-adapter latch from the prior scenario is set
- **WHEN** a second, unrelated `Send` call is made on the same `Adapter` instance
- **THEN** the adapter sends plain text directly via `MsgOptionText` without attempting
  `markdown` blocks first, and without emitting a second WARN for the same latch

#### Scenario: Ambiguous invalid_arguments failure retries as plain text but does not latch

- **GIVEN** a Slack workspace/app whose `chat.postMessage` rejects `markdown` blocks with an
  `invalid_arguments` error (which Slack also returns for unrelated reasons such as a bad
  channel or timestamp)
- **WHEN** `Send` is called with agent prose
- **THEN** the adapter retries the same send as plain text via `MsgOptionText` and the message
  is delivered, but no per-adapter latch is set — a subsequent, unrelated `Send` call on the
  same `Adapter` instance attempts `markdown` blocks again rather than skipping straight to
  plain text

#### Scenario: Oversized prose is truncated with a visible marker, not silently cut

- **GIVEN** the agent's outbound text exceeds the 12,000-character cumulative `markdown`
  block budget
- **WHEN** `Send` posts this text
- **THEN** the emitted `markdown` blocks together stay within the 12,000-character budget,
  the last block ends with a visible truncation marker (e.g. `_…truncated…_`), and any code
  fence open at the truncation point is closed before the marker so the marker does not
  render inside a code block

### Requirement: Telegram adapter via go-telegram/bot

The Telegram adapter SHALL use `github.com/go-telegram/bot` for long-polling. The adapter
MUST implement: private/public access mode per identity, mention extraction, media download
into the bridge media store, outbound text chunking, file upload, and reply-to-thread. The
adapter MUST NOT use webhook mode (no inbound HTTP exposure required).

**Outbound prose text MUST be rendered as GitHub-Flavored Markdown (GFM) converted to
Telegram HTML, not sent unparsed.** The `Send` method's outbound text part SHALL be
converted via `internal/bridge/markdown.ToTelegramHTML` and sent with
`models.ParseModeHTML`, rather than the current behavior of setting no `ParseMode` at all
(which renders every markdown character, including code fences, as literal text). Telegram
HTML supports `<b> <i> <u> <s> <a href> <code> <pre> <blockquote>`; the converter maps GFM
headings to bold text, unordered list items to `•`-prefixed lines, tables to `<pre>`-wrapped
column-aligned text, and `---` to a literal dash rule, and escapes only `& < >` in prose
text runs (never inside already-converted tags or code content beyond the same three
characters).

Before conversion, outbound text SHALL be split at the *source markdown* level using the
shared fence-aware chunker (`internal/bridge/markdown`) at a conservative ~3,500-character
limit — not the raw 4,096-character `MaxTextLength` — so that neither the pre-conversion
chunk nor its HTML-entity-expanded, post-conversion form can exceed Telegram's 4,096-
character `sendMessage` cap. Each chunk MUST be a self-contained, independently valid
markdown fragment (any code fence open at a chunk boundary is closed on the outgoing chunk
and reopened with the same info string on the next), since each chunk is converted to HTML
independently.

If `sendMessage` with `ParseModeHTML` fails with an entity/parse error (e.g. `can't parse
entities`), the adapter MUST retry that one chunk, unchanged, with no `ParseMode` field and
the original markdown text — a content-specific retry, not a sticky per-adapter latch (each
message's chunks attempt HTML conversion fresh). A chunk MUST NEVER be dropped because of an
HTML conversion or entity-parse failure.

#### Scenario: Long-poll loop

- **WHEN** the Telegram adapter starts for an identity with a valid token
- **THEN** it begins long-polling `getUpdates`; received messages are normalized to the bridge's `Inbound` type and forwarded to the orchestrator

#### Scenario: Pairing-code flow

- **WHEN** a peer sends a pairing code matching the `pairingCodeHash` configured under `router.channels.telegram.bots[].pairingCodeHash`
- **THEN** the peer is added to `bridge_allowlist` for that identity

#### Scenario: Inbound media

- **WHEN** an inbound Telegram message contains a photo or document
- **THEN** the adapter downloads the file to `<config.Data.Directory>/bridge/media/` and the orchestrator passes the path to the agent as an attachment

#### Scenario: GFM prose renders correctly via Telegram HTML

- **GIVEN** the agent's outbound text contains `## Summary`, a `**bold**` phrase, a
  `- bullet` list, a `[link](https://example.com)`, and a fenced ` ```go ` code block
- **WHEN** `Send` posts this text to a Telegram chat
- **THEN** the text is converted to Telegram HTML (`<b>Summary</b>` for the heading,
  `<b>bold</b>`, a `•`-prefixed line for the bullet, `<a href="https://example.com">link</a>`,
  and `<pre><code class="language-go">...</code></pre>` for the fenced block) and sent with
  `ParseModeHTML`; none of the source markdown characters are visible literally in the
  rendered message

#### Scenario: Ampersand, less-than, and greater-than are escaped in prose but not in tags

- **GIVEN** the agent's outbound text contains the literal string `a < b && b > c`
- **WHEN** `ToTelegramHTML` converts this text
- **THEN** the output is `a &lt; b &amp;&amp; b &gt; c`, and this escaping does not corrupt
  any `<b>`, `<i>`, `<a href="...">`, `<code>`, or `<pre>` tags the converter itself emitted
  for other constructs in the same message

#### Scenario: Entity/parse error falls back to no-ParseMode for that chunk only

- **GIVEN** a chunk's converted HTML triggers a `can't parse entities` error from
  `sendMessage`
- **WHEN** `Send` processes that chunk
- **THEN** the adapter retries the same chunk with no `ParseMode` field and the original
  markdown text, the message is delivered (with visible markdown syntax for that chunk
  only), and no per-adapter latch is set — the next message's chunks attempt
  `ParseModeHTML` normally

#### Scenario: Long prose is chunked at the source markdown level, not after conversion

- **GIVEN** the agent's outbound text is 6,000 characters of GFM prose containing a fenced
  code block that straddles the natural 3,500-character cut point
- **WHEN** `Send` chunks and converts this text
- **THEN** the source text is split into two markdown-valid chunks (the fence is closed on
  the first chunk and reopened with the same info string on the second) before either chunk
  is converted to HTML, and neither resulting `sendMessage` call exceeds the 4,096-character
  Telegram limit

## ADDED Requirements

### Requirement: Outbound prose rendering never drops a message on formatting failure

Neither the Slack nor the Telegram adapter MAY drop an outbound message as a consequence of
a markdown-rendering failure. Every rendering failure path (Slack block-construction/
API-rejection, Telegram HTML-conversion/entity-parse-rejection) MUST have a defined
degradation to a simpler, always-valid representation (Slack: plain `MsgOptionText`;
Telegram: `sendMessage` with no `ParseMode`), and that degraded send MUST be attempted before
the adapter reports the `Send` call as failed.

#### Scenario: Formatting failure never surfaces as a lost message

- **GIVEN** either adapter's markdown-rendering path fails for any reason (API rejection,
  conversion bug, or unexpected input)
- **WHEN** `Send` is called
- **THEN** the adapter's defined plain-text degradation path is attempted and, if it
  succeeds, `Send` returns a delivered result; the message is not silently discarded solely
  because the richer rendering path failed

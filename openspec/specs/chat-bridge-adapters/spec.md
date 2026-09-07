# Chat Bridge Adapters

## Purpose

Defines the per-platform adapter contract for the chat bridge: a common `Adapter` interface implemented by Telegram (`go-telegram/bot`), Slack (`slack-go/slack` Socket Mode), and Mattermost (hand-rolled WebSocket + REST). Each adapter handles inbound normalization, outbound text/file delivery, echo prevention, and per-platform media-size limits. Adapters may optionally satisfy `InteractiveQuestionSender` for platform-native question UI; non-supporting adapters and runtime failures fall back to the bridge's numbered-options text rendering.

## Requirements

### Requirement: Adapter interface

The bridge SHALL define a single `Adapter` interface in `internal/bridge/adapter.go` that each platform implementation satisfies. The interface MUST support: connect/disconnect, inbound event subscription, outbound text message send, outbound file send, and per-identity health reporting. Adapters MUST run independently — failure or disconnection of one adapter MUST NOT affect others.

#### Scenario: Adapter implements common contract

- **WHEN** a new platform adapter is added under `internal/bridge/<platform>/`
- **THEN** it implements the `Adapter` interface and is constructed via a per-platform factory invoked from the orchestrator

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
limit — not the raw 4,096-character `MaxTextLength`. Telegram measures its cap on the
PARSED text ("1-4096 characters after entities parsing"), so HTML tag overhead and
`&amp;`-style escaping do not count against it and virtually every GFM construct converts
to the same length or shorter. The one construct that GROWS is a `---` horizontal rule,
which becomes 10 em-dashes; text consisting almost entirely of rule lines can therefore
still breach the cap, and the adapter MUST treat a `message is too long` rejection as a
degradation trigger (see the retry contract below) rather than a lost message.

Each chunk MUST be a self-contained, independently valid markdown fragment (any code fence
open at a chunk boundary is closed on the outgoing chunk and reopened with the same info
string on the next), since each chunk is converted to HTML independently. The chunker MUST
terminate on every input: because the synthetic reopen delimiter is pushed back onto the
unconsumed remainder, a fence whose info string rivals the chunk limit would otherwise
make the chunker consume less than it pushes back and loop forever. The chunker SHALL
guarantee strict forward progress per pass, dropping the close/reopen fixup for that one
boundary if that is the only way to make progress — losing the fence's syntax highlighting
across the split, never its content.

The converter MUST NOT emit a `<code>` or `<pre>` tag nested inside any other entity. The
Bot API's nesting rules state that bold/italic/underline/strikethrough/spoiler entities
"can contain and can be part of any other entities, **except pre and code**", and that
"all other entities can't contain each other"; the sole documented exception is the
`<pre><code class="language-x">` language-specifier form. A code span appearing inside a
heading (`## Fix ` + "`foo.go`" + `` — extremely common in agent prose), inside emphasis,
inside a link label, inside a blockquote, or inside a table cell SHALL therefore be
emitted as plain escaped text rather than a nested `<code>` tag. URLs interpolated into an
`href` attribute SHALL additionally escape `"` as `&quot;` so a URL containing a double
quote cannot terminate the attribute early and inject an unsupported attribute into the
tag.

If `sendMessage` with `ParseModeHTML` fails with an entity/parse error (e.g. `can't parse
entities`) or a `message is too long` rejection, the adapter MUST retry that one chunk,
unchanged, with no `ParseMode` field and the original markdown text — a content-specific
retry, not a sticky per-adapter latch (each message's chunks attempt HTML conversion
fresh). A chunk MUST NEVER be dropped because of an HTML conversion or entity-parse
failure.

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

#### Scenario: Code spans are never nested inside another entity

- **GIVEN** the agent's outbound text contains a heading with a code span
  (`## Fix ` + "`foo.go`" + ``), a bold phrase wrapping a code span, a link whose label is a
  code span, and a table cell containing a code span
- **WHEN** `ToTelegramHTML` converts this text
- **THEN** no `<code>` or `<pre>` tag appears inside any other tag (the only permitted
  nesting is `<pre><code class="language-x">`); each such code span is emitted as plain
  escaped text inside its wrapper, so Telegram accepts the message instead of rejecting it
  with `can't parse entities` and stripping the whole chunk's formatting

#### Scenario: A URL containing a double quote cannot break out of the href attribute

- **GIVEN** the agent's outbound text contains `[label](https://x.com" onclick="alert(1))`
- **WHEN** `ToTelegramHTML` converts this text
- **THEN** the `"` inside the URL is escaped as `&quot;`, the emitted `<a>` tag carries
  exactly one `href` attribute and no injected attributes, and the only unescaped double
  quotes in the output are the two that delimit `href`

#### Scenario: Post-conversion overflow degrades instead of dropping the message

- **GIVEN** a source chunk within the 3,500-character limit whose conversion nonetheless
  exceeds Telegram's 4,096-character post-parse cap (text made almost entirely of `---`
  rules, each expanding to 10 em-dashes) and `sendMessage` returns `message is too long`
- **WHEN** `Send` processes that chunk
- **THEN** the adapter retries the chunk with no `ParseMode` and the shorter original
  markdown source, and the message is delivered rather than reported as a failed send

#### Scenario: Chunker terminates on a fence whose info string rivals the chunk limit

- **GIVEN** outbound text opening a code fence whose info string is as long as (or longer
  than) the chunk limit itself
- **WHEN** `Send` chunks this text
- **THEN** the chunker terminates, every chunk stays within the rune limit, and no content
  is lost — the fence's close/reopen fixup is dropped for that boundary if that is the only
  way to guarantee forward progress

#### Scenario: Long prose is chunked at the source markdown level, not after conversion

- **GIVEN** the agent's outbound text is 6,000 characters of GFM prose containing a fenced
  code block that straddles the natural 3,500-character cut point
- **WHEN** `Send` chunks and converts this text
- **THEN** the source text is split into two markdown-valid chunks (the fence is closed on
  the first chunk and reopened with the same info string on the second) before either chunk
  is converted to HTML, and neither resulting `sendMessage` call exceeds the 4,096-character
  Telegram limit

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

### Requirement: Mattermost adapter

The Mattermost adapter SHALL connect to a Mattermost server via WebSocket and the REST API v4. The adapter MUST subscribe to `posted`, `post_edited`, and `post_deleted` events. Outbound text via REST `POST /api/v4/posts`; file attach via multipart upload to `POST /api/v4/files` followed by `POST /api/v4/posts` with `file_ids`. The adapter MUST implement an exponential-backoff reconnect loop with **1s → 30s, 20 attempts max**, matching the existing TS bridge behavior.

The implementation MAY use either `github.com/mattermost/mattermost/server/public/model` (the official driver) or a hand-rolled HTTP + WebSocket client. The Go port chooses the hand-rolled path because the official lib pulls ~272 transitive dependencies (grpc, protobuf, mlog, msgpack, etc.) which busts the +15 MB binary growth budget for what amounts to a thin JSON-over-WebSocket protocol — the TS bridge already hand-rolls the equivalent contract.

#### Scenario: DM routing

- **WHEN** an inbound `posted` event arrives on a direct-message channel
- **THEN** the adapter forwards it to the orchestrator without requiring a bot mention

#### Scenario: Group DM honors per-identity groupsEnabled

- **WHEN** an inbound `posted` event arrives on a group-DM channel and the identity has `groupsEnabled: false`
- **THEN** the adapter ignores the message; **WHEN** the identity has `groupsEnabled: true` it forwards the message

#### Scenario: Channel message requires mention

- **WHEN** an inbound `posted` event arrives on a regular channel without an @mention of the bot
- **THEN** the adapter ignores the message

#### Scenario: WebSocket disconnect triggers backoff reconnect

- **WHEN** the WebSocket connection drops
- **THEN** the adapter waits 1s, attempts reconnect, doubles the delay (cap 30s) on failure, and gives up after 20 attempts marking the identity disabled

### Requirement: from_bot/from_webhook filtering

All adapters SHALL filter out inbound messages flagged as originating from bots, webhooks, or the bridge's own bot identity. Echo prevention is the adapter's responsibility, not the orchestrator's.

#### Scenario: Mattermost from_webhook filtered

- **WHEN** a Mattermost `posted` event arrives with `props.from_webhook == "true"`
- **THEN** the adapter ignores the event

#### Scenario: Self-message filtered

- **WHEN** an inbound event's author matches the bridge's own bot user for that identity
- **THEN** the adapter ignores the event

### Requirement: Outbound media size limits

For outbound file delivery each adapter MUST enforce the platform's media size limit at upload time and return an error to the agent rather than attempt the upload: Telegram 50 MB, Slack 1 GB, Mattermost configurable (the adapter SHALL query the server config at startup or accept a configured override).

#### Scenario: Oversize file rejected pre-upload

- **WHEN** the agent emits a `FILE:<path>` token pointing at a 100 MB file via the Telegram adapter
- **THEN** the adapter does not attempt the upload, returns an error message to the chat surface, and surfaces the error to the agent via the FILE: protocol's error path

### Requirement: Adapter test parity with TS bridge

Each adapter MUST be covered by a Go test suite that mirrors the scenario coverage of the corresponding TS test file: Mattermost (`mattermost.test.js` 1377 LOC), Slack (`slack.test.js` 273 LOC), Telegram (`telegram.test.js` 204 LOC). Port success per adapter is binary: every existing TS test scenario MUST have a passing Go equivalent. Tests use Go std `testing` + `gorilla/websocket` for WebSocket mocking.

#### Scenario: Mattermost test suite green

- **WHEN** `go test ./internal/bridge/mattermost/...` runs
- **THEN** every scenario from the TS `mattermost.test.js` has a passing equivalent, covering WebSocket lifecycle, post dispatch, group DMs, and file flow

### Requirement: Optional InteractiveQuestionSender per-adapter capability

Each adapter MAY satisfy an optional `bridge.InteractiveQuestionSender` interface to render question-tool prompts using platform-native interactive UI. The interface is opt-in — adapters that do not satisfy it fall back to the bridge's numbered-options text rendering automatically. Adapters that DO satisfy it but fail at send time (missing scope, deprecated feature, network error) MUST return an error so the bridge's question router can retry with the text fallback for that peer.

The interface signature is:

```go
type InteractiveQuestionSender interface {
    SendInteractiveQuestion(ctx context.Context, peer PeerRef, prompt string, choices []QuestionChoice) error
}

type QuestionChoice struct {
    Label string
    Value string
}
```

Click callbacks (Slack `block_actions`, Telegram `callback_query`) MUST be normalized into the same `bridge.Inbound` shape as text replies, with `Inbound.Text == QuestionChoice.Value` for the clicked button. This keeps the inbound reply parser path identical between interactive and text-fallback flows.

Per-adapter support status in v1:

- **Slack**: satisfies the interface via `chat.postMessage` + actions block (one button per choice).
- **Telegram**: satisfies the interface via `sendMessage` + `reply_markup.inline_keyboard` (one row per choice).
- **Mattermost**: does NOT satisfy the interface. Mattermost interactive attachments (`actions[].integration.url`) require a Mattermost-callable webhook URL that the bridge does not host without additional infrastructure. Mattermost peers always use the numbered-options text fallback.

#### Scenario: Slack adapter implements InteractiveQuestionSender

- **WHEN** the bridge type-asserts a Slack adapter to `bridge.InteractiveQuestionSender`
- **THEN** the assertion succeeds; calling `SendInteractiveQuestion` produces a `chat.postMessage` with a single actions block whose buttons match the supplied choices in order

#### Scenario: Telegram adapter implements InteractiveQuestionSender

- **WHEN** the bridge type-asserts a Telegram adapter to `bridge.InteractiveQuestionSender`
- **THEN** the assertion succeeds; calling `SendInteractiveQuestion` produces a `sendMessage` with `reply_markup.inline_keyboard` populated with one row per choice; clicking a button delivers a `callback_query` that the adapter normalizes into a `bridge.Inbound` whose `Text` is the clicked choice's `Value`

#### Scenario: Mattermost adapter does NOT implement InteractiveQuestionSender

- **WHEN** the bridge type-asserts a Mattermost adapter to `bridge.InteractiveQuestionSender`
- **THEN** the assertion fails; the bridge's question router uses the numbered-options text fallback for every Mattermost peer regardless of `cfg.Router.QuestionMode`

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

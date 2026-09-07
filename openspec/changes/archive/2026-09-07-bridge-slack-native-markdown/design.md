# Design: bridge-slack-native-markdown

## Context

See `proposal.md § Why` for motivation. The relevant existing architecture:

- `internal/bridge/slack/adapter.go:901` builds the outbound text part with
  `slackgo.MsgOptionText(text, false)` — Slack's top-level `text` field, parsed as mrkdwn.
- `internal/bridge/slack/adapter.go:27-28` defines `MaxTextLength = 39_000` (Slack's
  `text`-field character cap) and `truncateRunes` (`:973-989`) trims to that cap without an
  ellipsis.
- `internal/bridge/slack/render.go` is a separate, already-correct code path: the
  `RichRenderer` (`renderToolCall`, `renderToolResult`, `renderList`, `renderTable`,
  `renderStatus`) hand-authors Block Kit `section`/`context` blocks with `slackgo.MarkdownType`
  text objects, which is mrkdwn — appropriate there because the content (tool params,
  previews) is composed directly in mrkdwn syntax, not passed through unmodified from an
  LLM. This path is unaffected by this change.
- `internal/bridge/telegram/adapter.go:849-884` (`Send`) calls `chunkText` (`:926-960`, rune-
  safe, no markdown awareness) and posts each chunk via `bot.SendMessage` with **no**
  `ParseMode` field set at all.
- `internal/bridge/telegram/adapter.go:31` defines `MaxTextLength = 4096` (Telegram's
  `sendMessage` character cap, counted after entity parsing, not before).
- `internal/bridge/telegram/render.go:255-262` documents why the `RichRenderer` path uses
  `models.ParseModeMarkdownV1` (legacy Markdown) rather than MarkdownV2: "MarkdownV2 is more
  forgiving... tool params and previews contain too many otherwise-reserved chars to escape
  reliably (e.g. '.' in decimal durations is reserved in MarkdownV2)." That reasoning
  motivates going one step further here and using Telegram HTML for the prose `Send` path,
  which needs zero syntax escaping beyond three characters.
- `internal/bridge/mattermost/adapter.go:514-570` (`Send`) posts `text` directly as
  `CreatePostInput.Message`; Mattermost's server parses `Post.Message` as standard GFM, so
  this path already renders correctly and needs no change. It is the reference for what
  "renders correctly" looks like.
- `internal/bridge/service/dispatch.go:585-621` (`handleTerminalEvent`) and `:627-640`
  (`agentMessageText`) concatenate the agent's `TextContent` parts (GFM, as the LLM emits
  it) into `Outbound.Text` and fan it out via `Service.SendBySessionID` to each bound
  adapter's `Send`. This change does not touch the dispatcher — it only changes how each
  adapter's `Send` renders the `Outbound.Text` it receives.
- `internal/bridge/bridge.go` (package `bridge`) is deliberately dependency-free — it
  defines only the shared `Adapter` interface and value types, imported by every platform
  package. `internal/bridge/markdown` follows the same discipline (stdlib only) so it can be
  imported by `slack`, `telegram`, and (if ever needed) `bridge` itself without a cycle.

## Goals / Non-Goals

**Goals:**

- Slack and Telegram render the same GFM prose Mattermost already renders correctly, with no
  behavior change to Mattermost.
- Neither platform drops a message because of a formatting failure — automatic degradation
  to plain text/no-parse-mode on error, not a lost send.
- No new dependency, no new config field, no schema change.
- The existing `RichRenderer` tool-card paths are untouched.

**Non-Goals:**

- Hand-rolling a full GFM→mrkdwn converter (see Rejected Alternatives).
- A config opt-out for the new rendering behavior (see Rejected Alternatives).
- Telegram MarkdownV2 (see Rejected Alternatives).
- File-upload-on-truncation fallback (see Rejected Alternatives).
- Any change to Mattermost's adapter, the `RichRenderer` paths, or the dispatcher's message
  assembly (`handleTerminalEvent` / `agentMessageText`).

## Dialect comparison: mrkdwn vs. GFM vs. Mattermost

| Construct | Slack `text`/mrkdwn (today, broken) | Slack `markdown` block (this change) | Mattermost `Post.Message` (unchanged, reference) |
|---|---|---|---|
| `**bold**` / `__bold__` | Not recognized (mrkdwn bold is single `*bold*`) — renders literal `**bold**` | Recognized, renders bold | Recognized, renders bold |
| `*italic*` / `_italic_` | mrkdwn italic is single `_italic_`; `*italic*` instead renders BOLD (mrkdwn's own bold syntax) — wrong emphasis, not literal text | Recognized, renders italic | Recognized, renders italic |
| `## heading` | Not recognized — renders literal `## heading` | Recognized; all heading levels render the same size (Slack doc-confirmed) | Recognized, renders sized headings |
| `- item` / `* item` | Not recognized as a list — renders literal `- item` | Recognized, renders a bulleted list (ordered, unordered, and task lists) | Recognized, renders a bulleted list |
| `` `code` `` | Recognized (mrkdwn also uses single backtick) — renders correctly today | Recognized, renders correctly | Recognized, renders correctly |
| ` ```lang\ncode\n``` ` | Fence recognized but **no syntax highlighting**; `lang` info string ignored | Recognized WITH syntax highlighting per Slack docs | Recognized WITH syntax highlighting |
| `[label](url)` | Not recognized — renders literal `[label](url)`; mrkdwn's own link syntax is `<url\|label>` | Recognized, renders a real hyperlink | Recognized, renders a real hyperlink |
| `~~strike~~` | mrkdwn strike is single `~strike~`; `~~strike~~` instead renders STRIKETHROUGH with a stray leftover tilde (the outer `~`/`~` pair triggers mrkdwn's strikethrough, leaving one `~` literal) — not simply unrecognized | Recognized, renders strikethrough | Recognized, renders strikethrough |
| `> quote` | Recognized (mrkdwn also uses `>`) | Recognized, renders blockquote | Recognized, renders blockquote |
| `\| a \| b \|` tables | Not recognized — renders literal pipe text | Recognized, renders a real table | Recognized, renders a real table |
| `---` divider | Not recognized — renders literal dashes | Recognized, renders a divider | Recognized, renders a horizontal rule |
| `![alt](url)` images | Not recognized — renders literal text | Rendered as a hyperlink (per Slack docs; not an inline image) | Recognized, renders an inline image |
| `<url\|label>` (mrkdwn's own link syntax, sometimes emitted by agents) | Recognized (this IS the native mrkdwn syntax) | NOT recognized by the `markdown` block parser — must be normalized to `[label](url)` before emission (see `NormalizeSlackLinks`) | N/A (Mattermost never receives this syntax) |
| `<@U123>` user mention | Recognized | Recognized (passed through unmodified) | N/A (different mention syntax) |

Slack facts cited from `docs.slack.dev/reference/block-kit/blocks/markdown-block/` and the
blocks reference: the `markdown` block type is available on the Messages surface; the
cumulative character limit across all `markdown` blocks in one payload is 12,000 characters;
`block_id` is accepted but ignored; a message may contain at most 50 blocks total (all block
types combined, not just `markdown`).

## Slack budget arithmetic

- **Payload budget**: 12,000 characters, cumulative across every `markdown` block in one
  `chat.postMessage` call. This is Slack's documented hard limit — exceeding it produces a
  `blocks_too_long`-class API error.
- **Per-block target**: 3,000 characters. Chosen as a sub-limit well under any theoretical
  per-block cap, keeping each block a reasonably sized, independently renderable chunk of
  markdown (relevant for the fence-aware chunker, which must reason about "how much room is
  left in this block").
- **Chunk-count derivation**: `blockCount = ceil(budget / perBlockTarget)` =
  `ceil(12000 / 3000)` = 4 blocks. The chunker then divides the *actual budget* evenly across
  that many blocks: `perChunkLimit = floor(budget / blockCount)` = `floor(12000 / 4)` = 3,000.
  This guarantees `sum(chunkLengths) <= budget` exactly, rather than allowing 4 blocks each
  chunked independently at 3,000 which could total up to 12,000 but risks off-by-one
  overruns from multi-byte rune boundaries; deriving from the same budget/count pair keeps
  the arithmetic exact.
- **Block-count cap**: 50 total blocks per message is Slack's absolute ceiling (shared across
  all block types in the payload, not just `markdown`). The adapter MUST cap emitted
  `markdown` blocks such that this can never be exceeded — with a 4-block target this is
  never in practice a binding constraint, but the cap MUST be enforced defensively (e.g. if a
  future change lowers `perBlockTarget`, the block count must not silently grow past 50).
- If content exceeds the total 12,000-character budget across all blocks, the excess is
  dropped and the visible truncation marker (see below) is appended to the last emitted
  block, with room for the marker reserved from the budget *before* the final chunk cut is
  computed (not appended after, which could itself overflow the budget).

## Chunking algorithm (shared, `internal/bridge/markdown`)

Applies to both the Slack per-block chunker and the Telegram source-chunker, parameterized
by a caller-supplied `limit` (Slack: derived per-chunk limit from the budget arithmetic
above; Telegram: ~3,500).

1. If the input fits within `limit` characters (rune count, not byte count), return it as a
   single chunk with `truncated = false`. No further steps run.
2. Otherwise, find the best cut point at or before `limit`, in priority order:
   a. The last blank line (`\n\n`) at or before `limit` — preferred because it is the
      strongest semantic boundary (paragraph break).
   b. Failing that, the last markdown heading line (`^#{1,6} `) at or before `limit` — a
      weaker but still meaningful semantic boundary.
   c. Failing that, the last newline at or before `limit` — any line boundary.
   A semantic boundary from (a) or (b) is accepted only if it retains at least half of
   `limit` characters in the chunk (i.e. the cut point is not absurdly early in the buffer);
   otherwise the algorithm falls through to (c), and if even a line boundary retains less
   than half of `limit`, falls through to step 4 (hard-wrap).
3. The cut point MUST NOT fall inside an open ` ``` ` fence. Track fence state (open/closed,
   and the fence's info string, e.g. `python`) by scanning lines up to the candidate cut
   point. If the candidate cut point is inside an open fence, close the fence at the cut
   (append a synthetic ` ``` ` line to the outgoing chunk) and reopen it at the start of the
   next chunk (prepend ` ```<info-string>\n` before the continuation content). This keeps
   every emitted chunk independently valid markdown.
4. If a single line (with no internal blank-line or heading boundary) exceeds `limit` on its
   own — e.g. a very long unbroken paragraph or a single long code line — hard-wrap it at
   exactly `limit` runes, walking the string rune-by-rune (never splitting a multi-byte UTF-8
   codepoint), and continue chunking the remainder as a new logical line.
4b. **Termination invariant.** Step 3's reopen delimiter is *synthetic text pushed back onto
   the unconsumed remainder*, so the loop only terminates if each pass consumes strictly more
   than it pushes back. Two shapes break that and must be handled explicitly, or the chunker
   spins forever while growing the remainder without bound:
   - the cut shrunk to zero because the closing delimiter alone exceeds `limit`; and
   - a reopen delimiter at least as long as the slice consumed, which happens when the
     fence's info string rivals `limit` (a fence whose opening line runs on with no newline —
     a minified JSON blob or base64 excerpt right after the backticks).
   Mitigation is two-layered: the info string carried into the *synthetic* reopen delimiter is
   clamped (`maxFenceInfoRunes`, 64 — long enough for any real language tag; the original
   opening line is always emitted verbatim), and, as an unconditional backstop, a pass that
   still cannot make forward progress drops the close/reopen fixup for that one boundary and
   hard-cuts instead. Content is always preserved in full; only the fence's syntax
   highlighting is lost across that split.
5. On the LAST chunk the caller is willing to emit (i.e. the point where the per-message
   budget — Slack's 12,000/4-block cap, or the "no more chunks" decision for Telegram —
   would be exceeded by continuing), the chunker packs content to fill the limit exactly
   rather than stopping at the nearest tidy semantic boundary; this chunk is where truncation
   happens (step 6), so maximizing content density here matters more than aesthetics.
6. If content remains after the last permitted chunk (truncation is occurring): reserve room
   for the truncation marker FIRST — recompute the last chunk's cut point at
   `limit - len(marker)`, not `limit`, so the marker is added within budget rather than
   pushing the chunk over — then append the marker (`\n\n_…truncated…_`, italic prose so it
   is visually distinct from agent content). If this final re-cut broke an open fence, close
   it (step 3's fence-closing logic applies again) before appending the marker, so the marker
   itself never renders inside a code block.
7. Every function implementing this algorithm returns `(chunks []string, truncated bool)` (or
   `(chunk string, truncated bool)` for a single-chunk caller); callers MUST propagate
   `truncated` so upstream code (tests, logs) can assert on it without re-deriving it from
   chunk count.

## Telegram GFM → HTML mapping

Telegram HTML (`models.ParseModeHTML`) supports exactly: `<b> <strong>`, `<i> <em>`, `<u>
<ins>`, `<s> <strike> <del>`, `<a href="...">`, `<code>`, `<pre>`, `<pre><code
class="language-x">`, `<blockquote>` (and `<blockquote expandable>`), `<tg-spoiler>`. Escaping
is required only for `&` → `&amp;`, `<` → `&lt;`, `>` → `&gt;`, and only in text runs — never
inside an already-emitted tag's attribute syntax or inside code content (Telegram does not
recognize entities inside `<code>`/`<pre>`, so code content must still be escaped for the three
characters but not further transformed).

| GFM construct | Telegram HTML output | Notes |
|---|---|---|
| `**bold**` / `__bold__` | `<b>bold</b>` | |
| `*italic*` / `_italic_` | `<i>italic</i>` | |
| `~~strike~~` | `<s>strike</s>` | |
| `` `code` `` | `<code>code</code>` | Content escaped for `&<>`, not further parsed |
| ` ```lang\ncode\n``` ` | `<pre><code class="language-lang">code</code></pre>` | `lang` omitted if no info string |
| `[label](url)` | `<a href="url">label</a>` | `url` escaped for `&<>` in the attribute |
| `# / ## / ... heading` | `<b>heading text</b>` followed by a line break | No native heading tag; bold is the closest degrade |
| `- item` / `* item` (unordered list) | `• item` (bullet glyph substitution, one per line) | No native list tag |
| `1. item` (ordered list) | `1. item` (kept verbatim; numbering is already plain text) | No native list tag; GFM numbering is preserved as-is |
| `> quote` | `<blockquote>quote</blockquote>` | |
| `\| a \| b \|` table | Wrapped in `<pre>...</pre>` preserving the original column-aligned text | Preserves alignment as monospace; no native table tag |
| `---` horizontal rule | A literal line of dashes (e.g. `——————————`) | No native rule; visual approximation |
| `![alt](url)` image | `<a href="url">alt</a>` (or `url` if `alt` is empty) | Same hyperlink degrade as Slack's `markdown` block |
| Plain prose text | Escaped for `&<>` only | No tag wrapping |

Implementation shape: `ToTelegramHTML` first extracts code spans and fenced code blocks into
placeholders (so inline emphasis/link parsing never fires inside code content), performs
line-oriented and inline substitution for the remaining constructs, escapes `&<>` in plain
text runs, then restores the placeholders as properly escaped `<code>`/`<pre>` content. This
placeholder-extraction order is the safeguard against, e.g., an underscore inside a code span
being misinterpreted as italic markup.

## Fallback / latch state machine

### Slack

```
        send with markdown blocks
                 |
         success ----------------------------> done (latch unaffected)
                 |
     block-related API error
     (invalid_blocks / invalid_block /
      invalid_arguments / blocks_too_long)
                 |
                 v
     log one-shot WARN; set adapter-level
     latch (sticky, in-memory, per Adapter
     instance) = "blocks disabled"
                 |
                 v
     retry the SAME logical send as plain
     text (today's MsgOptionText behavior)
                 |
                 v
              done (message delivered either way)

  Any subsequent Send on this Adapter instance,
  while the latch is set, skips the markdown-block
  path entirely and goes straight to plain text —
  no repeated failed attempts, no repeated WARN spam.
```

The latch is a boolean guarded the same way `lastError` / `lastFailureAt` already are
(`atomic.Value` / `atomic.Bool` on the `Adapter` struct) — no new locking primitive. It is
per-`Adapter`-instance (i.e. per Slack identity/workspace connection), not global, since block
support is a workspace/app-level capability that will not vary within one adapter's lifetime
but could differ across identities.

### Telegram

```
        send chunk with ParseModeHTML
                 |
         success ----------------------------> next chunk / done
                 |
      entity/parse error
      (e.g. "can't parse entities")
                 |
                 v
     retry THIS chunk, unchanged text,
     with NO ParseMode field
                 |
                 v
       done for this chunk (no latch —
       next message's chunks attempt
       ParseModeHTML fresh)
```

No sticky latch on Telegram: unlike Slack's block-type support (a capability of the
workspace/app), a Telegram entity-parse failure is specific to the content of one chunk (an
edge case in the GFM→HTML conversion producing invalid nesting, or a genuinely malformed
tag), not a capability the bot lacks. Latching would incorrectly degrade all future messages
because of one bad chunk.

## Rejected alternatives

1. **Hand-roll a GFM→mrkdwn converter for Slack, instead of using the `markdown` block.**
   Rejected: mrkdwn cannot represent headings, tables, ordered lists, or syntax-highlighted
   code fences at all — no converter output could round-trip those constructs faithfully, so
   any hand-rolled converter would still lose information the LLM intended to convey. Using
   Slack's native `markdown` block, which the platform itself parses as standard Markdown,
   eliminates the entire class of "which mrkdwn escape sequence approximates a GFM table"
   problems by not needing an approximation at all.

2. **A config opt-out flag (e.g. `router.slackNativeMarkdownEnabled`).** Rejected: the
   change is a bug fix, not a new feature with a debatable default — mrkdwn rendering of raw
   GFM is strictly worse for every existing user in every existing configuration; there is no
   scenario where a user would want literal `**bold**` asterisks over rendered bold text. A
   flag would only add a schema-update obligation (per `CLAUDE.md`) and a permanently-true
   toggle nobody would ever set to `false`. The automatic per-message fallback already
   provides the safety net a flag would otherwise exist for.

3. **Telegram MarkdownV2 instead of HTML.** Rejected: MarkdownV2 requires escaping roughly 18
   reserved characters (`_ * [ ] ( ) ~ \` > # + - = | { } . !`) anywhere they appear outside
   of an intentional markup construct, including inside things as mundane as a decimal
   number (`.`) or a version string (`-`). `internal/bridge/telegram/render.go`'s own
   comment already documents this exact problem for the `RichRenderer` path and chose
   `ParseModeMarkdownV1` for that reason. HTML requires escaping only 3 characters and has an
   unambiguous, well-specified tag grammar, making the converter far less likely to produce
   an unparseable message from LLM-authored GFM.

4. **File upload on truncation, mirroring `c2-agent`'s orchestrator.** Rejected for this
   change's scope: it requires plumbing a file-upload path through both adapters' truncation
   handling, choosing a filename/extension convention, and deciding how the two mechanisms
   (visible marker vs. attached file) compose when both platforms are bound to the same
   session. The visible truncation marker is a strictly smaller, self-contained change that
   already solves the "silent data loss" problem the marker exists to prevent; file upload on
   truncation can be proposed as an independent follow-up if truncation in practice proves
   disruptive.

## Risks / Trade-offs

**[Risk] Slack `markdown` block support could vary by workspace/app configuration in ways
not fully covered by the documented error codes.** Mitigated by treating any block-related
API error class as latch-triggering (broad error-code matching, not an exhaustive enum) —
false positives (latching for an unrelated but coincidentally block-shaped error) degrade to
plain text, which is always safe; false negatives (a block failure not recognized as such)
would retry as blocks again next message rather than latching, at worst repeating one log
line per occurrence.

**[Risk] Telegram HTML conversion producing invalid nesting from adversarial or malformed
GFM input.** Mitigated by the per-chunk fallback retry (no `ParseMode`) — a conversion bug
degrades that one chunk to plain text rather than failing the send.

**[Trade-off] The chunking algorithm is more complex than a naive length-based split.** The
complexity (semantic boundaries, fence-awareness, budget-aware final-chunk packing) is
justified because a naive split reintroduces exactly the failure mode this change fixes: a
fence split mid-block breaks syntax highlighting and can leave a dangling, unclosed code
block for the rest of the conversation view.

## Open Questions

None that would change the spec, approach, or task breakdown.

// Package markdown provides shared, stdlib-only markdown utilities used by
// the chat-bridge adapters (Slack and Telegram). It has no dependency on
// internal/bridge or any platform SDK — mirroring the dependency-free
// discipline of internal/bridge itself — so it can be imported by every
// platform adapter package without an import cycle.
//
// Two responsibilities live here (a third, the GFM -> Telegram-HTML
// converter, lives in telegram.go):
//
//   - NormalizeSlackLinks rewrites Slack mrkdwn's own link syntax
//     (<https://x|label> / <https://x>) into standard Markdown
//     ([label](https://x) / https://x) without disturbing <@U123>-style
//     mentions or incidental angle-bracket text.
//   - Split is a fence-aware chunker that splits long markdown text into
//     platform-sized pieces without ever cutting inside an open ``` fence,
//     never splitting a multi-byte UTF-8 rune, and packing the final
//     permitted chunk to the limit (appending a visible truncation marker)
//     when a hard chunk-count cap is reached with content still remaining.
package markdown

import (
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// TruncationMarker is appended to the last emitted chunk when content is
// dropped because a caller-supplied chunk-count limit was reached with
// text still remaining. Italic prose visually distinguishes it from
// agent-authored content. Uses an explicit \u2026 escape (not a literal
// … glyph) so the source file stays plain ASCII per repo convention.
const TruncationMarker = "\n\n_\u2026truncated\u2026_"

// DefaultPayloadBudget and DefaultBlockTarget are the fallback values
// BuildBlockChunks clamps to when the caller passes a non-positive or
// otherwise unusable budget/target. They mirror Slack's documented
// markdown-block limits (see the slack package's own constants, which are
// defined independently to avoid a markdown -> slack import).
const (
	DefaultPayloadBudget = 12_000
	DefaultBlockTarget   = 3_000
)

// slackLinkPattern matches Slack mrkdwn's own link syntax: <https://x|label>
// or bare <https://x>. Anchored on a URL scheme so `<@U123>` mentions,
// `<#C123|chan>` channel references, `<!here>`, and incidental angle-bracket
// text (e.g. "a < b && b > c") are never matched — only content that starts
// with http:// or https:// immediately after the opening `<` qualifies.
var slackLinkPattern = regexp.MustCompile(`<(https?://[^<>|]+)(?:\|([^<>]*))?>`)

// NormalizeSlackLinks rewrites Slack mrkdwn's own link syntax into standard
// Markdown so it renders correctly through a parser that only understands
// GFM-style links (e.g. Slack's `markdown` block type). `<https://x|label>`
// becomes `[label](https://x)`; `<https://x>` (or `<https://x|>` — an empty
// label) becomes the bare URL `https://x`. Non-URL angle-bracket content
// (user/channel mentions, `<!here>`, or incidental `<`/`>` characters) is
// left untouched.
func NormalizeSlackLinks(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	return slackLinkPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := slackLinkPattern.FindStringSubmatch(m)
		url := sub[1]
		label := strings.TrimSpace(sub[2])
		if label == "" {
			return url
		}
		return "[" + label + "](" + url + ")"
	})
}

// fenceState tracks whether we are currently inside an open ``` fence,
// the length of the backtick run that opened it (a fence is only closed by
// a run of at least that many backticks — CommonMark's own rule, which
// also means a SHORTER backtick run inside an open fence is just content,
// not a toggle), and the fence's info string (e.g. "python").
type fenceState struct {
	open      bool
	markerLen int
	info      string
}

// maxFenceInfoRunes bounds the info string retained for the purpose of
// REOPENING a fence on the far side of a chunk boundary. A real info string
// is a language tag ("go", "json", "python"); text far longer than that is
// not a tag at all, and carrying it verbatim into the synthetic reopen
// delimiter would let the delimiter rival the chunk limit itself — which
// made Split spin forever, since the reopen text is pushed back onto the
// unconsumed remainder. Clamping affects only the synthetic delimiter; the
// original opening line is always emitted untouched.
const maxFenceInfoRunes = 64

// clampRunes truncates s to at most n runes, always cutting on a codepoint
// boundary.
func clampRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// scanFence walks s line by line, updating fs for every fence-toggling line
// it finds (a line whose left-trimmed content starts with 3+ backticks).
// It is meant to be called incrementally, once per emitted chunk, so state
// carries forward correctly across chunk boundaries.
func scanFence(fs *fenceState, s string) {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		n := countLeadingBackticks(trimmed)
		if n < 3 {
			continue
		}
		if !fs.open {
			fs.open = true
			fs.markerLen = n
			fs.info = clampRunes(strings.TrimSpace(trimmed[n:]), maxFenceInfoRunes)
			continue
		}
		// Already inside a fence: only a run at least as long as the
		// opening one closes it. A shorter run is literal content.
		if n >= fs.markerLen {
			fs.open = false
			fs.markerLen = 0
			fs.info = ""
		}
	}
}

func countLeadingBackticks(s string) int {
	n := 0
	for n < len(s) && s[n] == '`' {
		n++
	}
	return n
}

// fenceCloseText returns the text to append to head to close the fence
// described by fs. It adds a leading newline only if head doesn't already
// end with one, so the closing delimiter always starts its own line.
func fenceCloseText(head string, fs fenceState) string {
	if head != "" && !strings.HasSuffix(head, "\n") {
		return "\n" + strings.Repeat("`", fs.markerLen)
	}
	return strings.Repeat("`", fs.markerLen)
}

// fenceOpenText returns the text to prepend to the next chunk to reopen
// the fence described by fs, preserving its original info string.
func fenceOpenText(fs fenceState) string {
	return strings.Repeat("`", fs.markerLen) + fs.info + "\n"
}

var headingLineRe = regexp.MustCompile(`(?m)^#{1,6} `)

// findBoundary picks the best cut point for remaining, at or before limit
// runes, in priority order: blank line, heading line, any newline, falling
// through to a hard-wrap at exactly limit runes when no candidate retains
// at least half of limit. The returned value is a rune index into
// remaining (NOT a byte offset).
func findBoundary(remaining string, limit int) int {
	runeLen := utf8.RuneCountInString(remaining)
	if limit >= runeLen {
		return runeLen
	}
	prefixByteLen := runeIndexToByte(remaining, limit)
	prefix := remaining[:prefixByteLen]
	half := limit / 2

	if idx := strings.LastIndex(prefix, "\n\n"); idx >= 0 {
		cut := idx + 2
		if rc := utf8.RuneCountInString(remaining[:cut]); rc >= half {
			return rc
		}
	}

	if locs := headingLineRe.FindAllStringIndex(prefix, -1); len(locs) > 0 {
		last := locs[len(locs)-1]
		cut := last[0]
		if cut > 0 {
			if rc := utf8.RuneCountInString(remaining[:cut]); rc >= half {
				return rc
			}
		}
	}

	if idx := strings.LastIndex(prefix, "\n"); idx >= 0 {
		cut := idx + 1
		if rc := utf8.RuneCountInString(remaining[:cut]); rc >= half {
			return rc
		}
	}

	return limit
}

// runeIndexToByte returns the byte offset of the n-th rune in s (or
// len(s) if s has fewer than n runes).
func runeIndexToByte(s string, n int) int {
	if n <= 0 {
		return 0
	}
	count := 0
	for i := range s {
		if count == n {
			return i
		}
		count++
	}
	return len(s)
}

// buildChunk cuts remRunes at rune index cut, closing any fence the cut
// broke (per fs, the fence state carried in from prior chunks). If the
// closing overhead (plus the caller's reserve, e.g. room for a truncation
// marker) would push the chunk's rune length past limit, cut is shrunk and
// the fence re-scanned until it fits — this is what guarantees every
// chunk, including ones with a broken-fence close appended, stays within
// limit runes.
//
// Returns the finalized chunk text (head), the fence state to carry into
// the next chunk (already "closed" if a close/reopen fixup happened —
// scanning the next chunk's reopening delimiter line naturally reopens
// it), the text to prepend to the next chunk (empty unless a fence was
// reopened), and the rune index actually consumed from remRunes (<= cut;
// only differs from the input cut when shrinking occurred).
func buildChunk(remRunes []rune, cut, limit, reserve int, fs fenceState) (head string, newFS fenceState, reopen string, usedCut int) {
	if cut > len(remRunes) {
		cut = len(remRunes)
	}
	if cut < 0 {
		cut = 0
	}
	for {
		h := string(remRunes[:cut])
		fsCopy := fs
		scanFence(&fsCopy, h)

		closing := ""
		reopenText := ""
		finalFS := fsCopy
		if fsCopy.open {
			closing = fenceCloseText(h, fsCopy)
			reopenText = fenceOpenText(fsCopy)
			finalFS = fenceState{}
		}

		total := utf8.RuneCountInString(h) + utf8.RuneCountInString(closing) + reserve
		if total <= limit || cut == 0 {
			return h + closing, finalFS, reopenText, cut
		}
		overflow := total - limit
		next := cut - overflow
		if next >= cut {
			next = cut - 1
		}
		cut = next
		if cut < 0 {
			cut = 0
		}
	}
}

// Split divides text into chunks of at most limit runes each, never
// cutting inside an open ``` fence (closing and reopening it across the
// boundary instead) and never splitting a multi-byte UTF-8 rune. It
// prefers blank-line, then heading-line, then any-newline cut points,
// falling back to a hard rune-wrap when no such boundary retains at least
// half of limit.
//
// maxChunks caps the number of chunks emitted; <= 0 means unlimited. When
// the cap is reached with text still remaining, the final chunk is packed
// to the limit (rather than stopping at a tidy boundary) and marker is
// appended within budget — the marker's own rune length, and any fence
// the final cut broke, are reserved for BEFORE the cut is made, so the
// marker is never pushed over limit and never renders inside a code
// fence. truncated is true exactly when this happened.
//
// Whitespace-only input returns (nil, false).
func Split(text string, limit, maxChunks int, marker string) ([]string, bool) {
	if strings.TrimSpace(text) == "" {
		return nil, false
	}
	if limit <= 0 {
		limit = 1
	}
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}, false
	}

	markerLen := utf8.RuneCountInString(marker)
	var chunks []string
	remaining := text
	fs := fenceState{}
	truncated := false

	for utf8.RuneCountInString(remaining) > limit {
		remRunes := []rune(remaining)
		isLast := maxChunks > 0 && len(chunks)+1 >= maxChunks

		if isLast {
			cut := limit - markerLen
			head, _, _, _ := buildChunk(remRunes, cut, limit, markerLen, fs)
			chunks = append(chunks, head+marker)
			truncated = true
			remaining = ""
			break
		}

		cut := findBoundary(remaining, limit)
		head, newFS, reopen, usedCut := buildChunk(remRunes, cut, limit, 0, fs)

		// Forward-progress guarantee. `reopen` is synthetic text pushed
		// BACK onto the unconsumed remainder, so this loop only terminates
		// if every pass consumes strictly more than it pushes back. Two
		// pathological shapes break that: a cut shrunk all the way to zero
		// (a limit smaller than the fence delimiter itself), and a reopen
		// delimiter at least as long as the slice consumed. Either one used
		// to spin forever while growing `remaining` without bound. Give up
		// the close/reopen fixup for this one boundary instead: the content
		// is still emitted verbatim and in full, only the fence's syntax
		// highlighting is lost across the split.
		if usedCut <= 0 || utf8.RuneCountInString(reopen) >= usedCut {
			hardCut := cut
			if hardCut < 1 {
				hardCut = 1
			}
			if hardCut > len(remRunes) {
				hardCut = len(remRunes)
			}
			head = string(remRunes[:hardCut])
			newFS = fs
			scanFence(&newFS, head)
			reopen = ""
			usedCut = hardCut
		}

		chunks = append(chunks, head)
		fs = newFS
		remaining = reopen + string(remRunes[usedCut:])
	}

	if remaining != "" {
		chunks = append(chunks, remaining)
	}

	return chunks, truncated
}

// BuildBlockChunks is the Slack-facing helper: it normalizes text via
// NormalizeSlackLinks, derives a per-chunk rune limit from the payload
// budget and per-block target (blockCount = ceil(payloadBudget /
// perBlockTarget); perChunkLimit = floor(payloadBudget / blockCount), so
// blockCount * perChunkLimit never exceeds payloadBudget), and calls
// Split with that limit and blockCount as the chunk cap. The result
// always satisfies sum(runeLen(chunk)) <= payloadBudget.
//
// Non-positive payloadBudget clamps to DefaultPayloadBudget; non-positive
// or oversized perBlockTarget (bigger than the budget) clamps to
// DefaultBlockTarget (itself capped to the budget).
func BuildBlockChunks(text string, payloadBudget, perBlockTarget int, marker string) ([]string, bool) {
	if payloadBudget <= 0 {
		payloadBudget = DefaultPayloadBudget
	}
	if perBlockTarget <= 0 || perBlockTarget > payloadBudget {
		perBlockTarget = DefaultBlockTarget
		if perBlockTarget > payloadBudget {
			perBlockTarget = payloadBudget
		}
	}

	normalized := NormalizeSlackLinks(text)
	if strings.TrimSpace(normalized) == "" {
		return nil, false
	}

	blockCount := int(math.Ceil(float64(payloadBudget) / float64(perBlockTarget)))
	if blockCount < 1 {
		blockCount = 1
	}
	perChunkLimit := payloadBudget / blockCount // floor division
	if perChunkLimit < 1 {
		// Budget smaller than blockCount: shrink blockCount so the
		// sum(chunkLengths) <= payloadBudget guarantee still holds.
		perChunkLimit = 1
		blockCount = payloadBudget
		if blockCount < 1 {
			blockCount = 1
		}
	}

	return Split(normalized, perChunkLimit, blockCount, marker)
}

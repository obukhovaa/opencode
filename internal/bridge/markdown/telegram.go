// ToTelegramHTML converts GFM-flavored markdown (as an LLM emits it) into
// Telegram's restricted HTML dialect (models.ParseModeHTML). Telegram's
// parser supports only a small, fixed tag set — <b>/<strong>, <i>/<em>,
// <u>/<ins>, <s>/<strike>/<del>, <a href="...">, <code>, <pre>,
// <pre><code class="language-x">, <blockquote>, <tg-spoiler> — and
// requires escaping exactly three characters (&, <, >) everywhere else.
// See design.md "Telegram GFM -> HTML mapping" for the full construct
// table this file implements.
package markdown

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	headingRe    = regexp.MustCompile(`^#{1,6}\s+(.*)$`)
	hrRe         = regexp.MustCompile(`^(-{3,}|\*{3,}|_{3,})$`)
	taskItemRe   = regexp.MustCompile(`^(\s*)[-*+] \[([ xX])\] (.*)$`)
	ulItemRe     = regexp.MustCompile(`^(\s*)[-*+] (.*)$`)
	inlineCodeRe = regexp.MustCompile("`([^`\n]+)`")
)

// placeholder holds the two renderings of one extracted code construct.
//
// html is the real Telegram tag (<code>.../<pre>...), used when the
// construct sits at the top level of the message. plain is the same
// content escaped but UNTAGGED, used when the construct would otherwise
// land inside another entity: the Bot API's "Formatting options" nesting
// rules state that bold/italic/underline/strikethrough/spoiler entities
// "can contain and can be part of any other entities, EXCEPT pre and
// code", and that "all other entities can't contain each other". A
// <code> nested in <b> (e.g. the very common "## Fix `foo.go`" heading),
// in <pre> (a table cell holding a code span) or in <a> is therefore
// rejected by Telegram with "can't parse entities", costing the whole
// chunk its formatting. Flattening to plain keeps the message valid.
type placeholder struct {
	html  string
	plain string
}

// ToTelegramHTML converts s from GFM markdown to Telegram-safe HTML.
//
// Implementation shape (see design.md): code spans and fenced code
// blocks are extracted into opaque placeholder tokens FIRST, so no later
// inline-emphasis or link substitution can ever fire inside code
// content. Line-oriented block transforms (headings, lists, blockquotes,
// tables, horizontal rules) and inline transforms (bold/italic/strike/
// links) run on what remains, escaping "&<>" in plain-text runs as they
// go. Finally the placeholders are restored as escaped <code>/<pre>
// content — except where a placeholder ended up inside another entity,
// in which case it is flattened to plain escaped text (see placeholder).
//
// Never panics; unbalanced markers (unclosed "**", "[label](" with no
// closing paren) are left as literal escaped text rather than emitting a
// broken tag. An unclosed ``` fence is the one exception called out by
// the design: because we already commit to opening a <pre> once we see
// the opening delimiter, we auto-close it with whatever content follows,
// rather than leaving a dangling, unparseable code fence marker.
func ToTelegramHTML(s string) string {
	if s == "" {
		return ""
	}
	placeholders := map[string]placeholder{}
	counter := 0
	withoutFences := extractFences(s, &counter, placeholders)
	withoutCode := extractInlineCode(withoutFences, &counter, placeholders)
	converted := convertBlocks(withoutCode, placeholders)
	return restorePlaceholders(converted, placeholders)
}

// newPlaceholder returns the next opaque placeholder token. The token
// body is restricted to a NUL byte, the letters "md", and digits — none
// of which are markdown metacharacters or HTML-escapable characters, so
// the token can pass through every later transform untouched and is
// restored verbatim in the final pass.
func newPlaceholder(counter *int) string {
	*counter++
	return "\x00md" + strconv.Itoa(*counter) + "\x00"
}

// restorePlaceholders replaces every placeholder token still present with
// its final (already-escaped) tagged HTML content. Tokens that were
// flattened earlier by flattenPlaceholders are already gone by this point.
func restorePlaceholders(s string, placeholders map[string]placeholder) string {
	if len(placeholders) == 0 {
		return s
	}
	for token, ph := range placeholders {
		s = strings.ReplaceAll(s, token, ph.html)
	}
	return s
}

// flattenPlaceholders replaces every placeholder token in s with its
// UNTAGGED escaped content. Callers apply it to text they are about to
// wrap in an entity tag (<b>, <i>, <s>, <a>, <pre>, <blockquote>), because
// Telegram forbids code/pre entities nested inside any other entity —
// see the placeholder type for the exact rule and the failure mode.
func flattenPlaceholders(s string, placeholders map[string]placeholder) string {
	if len(placeholders) == 0 || !strings.Contains(s, "\x00") {
		return s
	}
	for token, ph := range placeholders {
		s = strings.ReplaceAll(s, token, ph.plain)
	}
	return s
}

// extractFences scans s line by line for ``` fences (reusing the same
// countLeadingBackticks helper markdown.go's chunker uses) and replaces
// each fenced block — delimiters, info string and all — with a single
// placeholder line. A fence with no matching close consumes the rest of
// the text as its content rather than being left unrecognized, so the
// eventual restoration always emits a well-formed, closed <pre>.
func extractFences(s string, counter *int, placeholders map[string]placeholder) string {
	if !strings.Contains(s, "```") {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimLeft(line, " \t")
		n := countLeadingBackticks(trimmed)
		if n < 3 {
			out = append(out, line)
			i++
			continue
		}
		lang := strings.TrimSpace(trimmed[n:])

		closeIdx := -1
		for j := i + 1; j < len(lines); j++ {
			t := strings.TrimLeft(lines[j], " \t")
			cn := countLeadingBackticks(t)
			if cn >= n && strings.TrimSpace(t[cn:]) == "" {
				closeIdx = j
				break
			}
		}

		var content string
		var next int
		if closeIdx >= 0 {
			content = strings.Join(lines[i+1:closeIdx], "\n")
			next = closeIdx + 1
		} else {
			// Unclosed fence: treat everything to the end of the text
			// as code content so we still emit a well-formed <pre>.
			content = strings.Join(lines[i+1:], "\n")
			next = len(lines)
		}

		token := newPlaceholder(counter)
		placeholders[token] = placeholder{
			html:  renderFencedCode(lang, content),
			plain: escapeHTML(content),
		}
		out = append(out, token)
		i = next
	}
	return strings.Join(out, "\n")
}

// renderFencedCode renders one fenced block's final HTML. Content is
// escaped for "&<>" only — never reparsed for markdown — and whitespace/
// newlines are preserved exactly.
func renderFencedCode(lang, content string) string {
	if lang == "" {
		return "<pre>" + escapeHTML(content) + "</pre>"
	}
	return `<pre><code class="language-` + escapeHTML(lang) + `">` + escapeHTML(content) + `</code></pre>`
}

// extractInlineCode replaces every `code` span (single-backtick,
// single-line) with a placeholder token holding its escaped <code>
// content.
func extractInlineCode(s string, counter *int, placeholders map[string]placeholder) string {
	if !strings.Contains(s, "`") {
		return s
	}
	return inlineCodeRe.ReplaceAllStringFunc(s, func(m string) string {
		content := m[1 : len(m)-1]
		token := newPlaceholder(counter)
		placeholders[token] = placeholder{
			html:  "<code>" + escapeHTML(content) + "</code>",
			plain: escapeHTML(content),
		}
		return token
	})
}

// convertBlocks applies the line-oriented GFM->HTML mapping (tables,
// blockquotes, headings, horizontal rules, task/unordered list items)
// and, for every other line, the inline mapping via processInline.
func convertBlocks(s string, ph map[string]placeholder) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if isTableLine(line) {
			j := i
			for j < len(lines) && isTableLine(lines[j]) {
				j++
			}
			if j-i >= 2 {
				run := strings.Join(lines[i:j], "\n")
				out = append(out, "<pre>"+flattenPlaceholders(escapeHTML(run), ph)+"</pre>")
				i = j
				continue
			}
			// A lone `|...|` line isn't a table run (design requires
			// 2+ consecutive lines) — fall through to normal handling.
		}

		if strings.HasPrefix(trimmed, ">") {
			j := i
			var quoteLines []string
			for j < len(lines) {
				t := strings.TrimSpace(lines[j])
				if !strings.HasPrefix(t, ">") {
					break
				}
				inner := strings.TrimPrefix(t, ">")
				inner = strings.TrimPrefix(inner, " ")
				quoteLines = append(quoteLines, processInline(inner, ph))
				j++
			}
			body := flattenPlaceholders(strings.Join(quoteLines, "\n"), ph)
			out = append(out, "<blockquote>"+body+"</blockquote>")
			i = j
			continue
		}

		if m := headingRe.FindStringSubmatch(line); m != nil {
			out = append(out, "<b>"+flattenPlaceholders(processInline(m[1], ph), ph)+"</b>")
			i++
			continue
		}

		if hrRe.MatchString(trimmed) {
			out = append(out, strings.Repeat("\u2014", 10))
			i++
			continue
		}

		if m := taskItemRe.FindStringSubmatch(line); m != nil {
			box := "\u2610"
			if strings.EqualFold(m[2], "x") {
				box = "\u2611"
			}
			out = append(out, m[1]+box+" "+processInline(m[3], ph))
			i++
			continue
		}

		if m := ulItemRe.FindStringSubmatch(line); m != nil {
			out = append(out, m[1]+"\u2022 "+processInline(m[2], ph))
			i++
			continue
		}

		out = append(out, processInline(line, ph))
		i++
	}
	return strings.Join(out, "\n")
}

// isTableLine reports whether line, trimmed, both starts and ends with
// "|" — the shape shared by a GFM table's header, separator, and data
// rows.
func isTableLine(line string) bool {
	t := strings.TrimSpace(line)
	return len(t) >= 2 && strings.HasPrefix(t, "|") && strings.HasSuffix(t, "|")
}

// processInline applies the inline GFM->HTML mapping (bold, italic,
// strikethrough, links, images) to s and escapes "&<>" in every plain-
// text run it does not otherwise transform. Delimiters with no matching
// close are emitted as literal (escaped) text rather than an unclosed
// tag. Bold/italic/strikethrough content is reprocessed recursively so
// nesting (e.g. italic inside bold) round-trips correctly; code
// placeholders inside any emitted tag are flattened to plain text,
// because Telegram rejects code/pre nested in another entity.
func processInline(s string, ph map[string]placeholder) string {
	var b strings.Builder
	i := 0
	n := len(s)
	for i < n {
		c := s[i]
		switch {
		case c == '!' && i+1 < n && s[i+1] == '[':
			if label, url, newPos, ok := matchLink(s, i+1); ok {
				if label == "" {
					b.WriteString(flattenPlaceholders(escapeHTML(url), ph))
				} else {
					writeLink(&b, label, url, ph)
				}
				i = newPos
				continue
			}
			b.WriteByte('!')
			i++

		case c == '[':
			if label, url, newPos, ok := matchLink(s, i); ok {
				writeLink(&b, label, url, ph)
				i = newPos
				continue
			}
			b.WriteByte('[')
			i++

		case i+1 < n && (s[i:i+2] == "**" || s[i:i+2] == "__"):
			marker := s[i : i+2]
			if inner, newPos, ok := matchDelim(s, i, marker); ok {
				b.WriteString("<b>")
				b.WriteString(flattenPlaceholders(processInline(inner, ph), ph))
				b.WriteString("</b>")
				i = newPos
				continue
			}
			b.WriteString(marker)
			i += 2

		case i+1 < n && s[i:i+2] == "~~":
			if inner, newPos, ok := matchDelim(s, i, "~~"); ok {
				b.WriteString("<s>")
				b.WriteString(flattenPlaceholders(processInline(inner, ph), ph))
				b.WriteString("</s>")
				i = newPos
				continue
			}
			b.WriteString("~~")
			i += 2

		case c == '*':
			if inner, newPos, ok := matchDelim(s, i, "*"); ok {
				b.WriteString("<i>")
				b.WriteString(flattenPlaceholders(processInline(inner, ph), ph))
				b.WriteString("</i>")
				i = newPos
				continue
			}
			b.WriteByte('*')
			i++

		case c == '_':
			// Underscore emphasis requires a non-word boundary on both
			// sides (CommonMark's own rule) so "snake_case_name" is
			// never mistaken for italic markup.
			leftOK := i == 0 || !isWordByte(s[i-1])
			if leftOK {
				if inner, newPos, ok := matchDelim(s, i, "_"); ok {
					rightOK := newPos >= n || !isWordByte(s[newPos])
					if rightOK {
						b.WriteString("<i>")
						b.WriteString(flattenPlaceholders(processInline(inner, ph), ph))
						b.WriteString("</i>")
						i = newPos
						continue
					}
				}
			}
			b.WriteByte('_')
			i++

		default:
			switch c {
			case '&':
				b.WriteString("&amp;")
			case '<':
				b.WriteString("&lt;")
			case '>':
				b.WriteString("&gt;")
			default:
				b.WriteByte(c)
			}
			i++
		}
	}
	return b.String()
}

// writeLink emits one <a href="...">label</a>. The URL goes through
// escapeAttr (not escapeHTML) because it lands inside a double-quoted
// attribute: a URL containing `"` would otherwise close the attribute
// early and inject arbitrary attributes into the tag, which Telegram
// rejects as an unsupported tag shape. Label and URL are additionally
// flattened so a code span inside either never becomes a <code> nested
// in the <a> entity.
func writeLink(b *strings.Builder, label, url string, ph map[string]placeholder) {
	b.WriteString(`<a href="`)
	b.WriteString(flattenPlaceholders(escapeAttr(url), ph))
	b.WriteString(`">`)
	b.WriteString(flattenPlaceholders(escapeHTML(label), ph))
	b.WriteString(`</a>`)
}

// matchDelim looks for marker again, starting right after the opening
// occurrence at pos, and returns the text between them plus the index
// just past the closing occurrence. ok is false when no closing
// occurrence exists (caller then treats the opening marker as literal
// text rather than opening a tag it cannot close).
func matchDelim(s string, pos int, marker string) (inner string, newPos int, ok bool) {
	start := pos + len(marker)
	if start > len(s) {
		return "", 0, false
	}
	idx := strings.Index(s[start:], marker)
	if idx < 0 {
		return "", 0, false
	}
	return s[start : start+idx], start + idx + len(marker), true
}

// matchLink parses a `[label](url)` construct starting at pos (where
// s[pos] must be '['). ok is false for `[label](` with no closing paren
// (or no closing bracket at all), so the caller falls back to literal
// text instead of an unclosed <a> tag.
func matchLink(s string, pos int) (label, url string, newPos int, ok bool) {
	if pos >= len(s) || s[pos] != '[' {
		return "", "", 0, false
	}
	closeBracket := strings.IndexByte(s[pos+1:], ']')
	if closeBracket < 0 {
		return "", "", 0, false
	}
	closeBracket += pos + 1
	if closeBracket+1 >= len(s) || s[closeBracket+1] != '(' {
		return "", "", 0, false
	}
	closeParen := strings.IndexByte(s[closeBracket+2:], ')')
	if closeParen < 0 {
		return "", "", 0, false
	}
	closeParen += closeBracket + 2
	return s[pos+1 : closeBracket], s[closeBracket+2 : closeParen], closeParen + 1, true
}

// isWordByte reports whether b is an ASCII letter, digit, or underscore
// — CommonMark's definition of a "word" character used for the
// underscore-emphasis boundary check.
func isWordByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// escapeHTML escapes the three characters Telegram HTML requires
// escaped in plain-text runs: "&" -> "&amp;", "<" -> "&lt;",
// ">" -> "&gt;". Iterates by byte (not rune) — safe because none of the
// three ASCII targets ever appears as a continuation byte of a
// multi-byte UTF-8 sequence, so other bytes pass through unmodified and
// unmodified sequences stay valid UTF-8.
func escapeHTML(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// escapeAttr escapes a value destined for a double-quoted HTML attribute:
// everything escapeHTML handles, plus '"' -> "&quot;" so the value can
// never terminate the attribute early. &quot; is one of the four named
// entities the Bot API documents as supported.
func escapeAttr(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

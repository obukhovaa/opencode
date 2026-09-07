package markdown

import (
	"strings"
	"testing"
)

func TestToTelegramHTML(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bold double star",
			in:   "**bold**",
			want: "<b>bold</b>",
		},
		{
			name: "bold double underscore",
			in:   "__bold__",
			want: "<b>bold</b>",
		},
		{
			name: "italic single star",
			in:   "*italic*",
			want: "<i>italic</i>",
		},
		{
			name: "italic single underscore",
			in:   "_italic_",
			want: "<i>italic</i>",
		},
		{
			name: "strikethrough",
			in:   "~~strike~~",
			want: "<s>strike</s>",
		},
		{
			name: "inline code",
			in:   "`code`",
			want: "<code>code</code>",
		},
		{
			name: "fenced code with lang",
			in:   "```go\nfunc main() {}\n```",
			want: "<pre><code class=\"language-go\">func main() {}</code></pre>",
		},
		{
			name: "fenced code without info string",
			in:   "```\nplain\n```",
			want: "<pre>plain</pre>",
		},
		{
			name: "link",
			in:   "[label](https://example.com)",
			want: `<a href="https://example.com">label</a>`,
		},
		{
			name: "image with alt",
			in:   "![alt text](https://example.com/x.png)",
			want: `<a href="https://example.com/x.png">alt text</a>`,
		},
		{
			name: "image with empty alt",
			in:   "![](https://example.com/x.png)",
			want: "https://example.com/x.png",
		},
		{
			name: "heading level 1",
			in:   "# Title",
			want: "<b>Title</b>",
		},
		{
			name: "heading level 6",
			in:   "###### Small heading",
			want: "<b>Small heading</b>",
		},
		{
			name: "unordered list dash",
			in:   "- item one",
			want: "\u2022 item one",
		},
		{
			name: "unordered list star",
			in:   "* item one",
			want: "\u2022 item one",
		},
		{
			name: "unordered list plus with indentation",
			in:   "  + item one",
			want: "  \u2022 item one",
		},
		{
			name: "ordered list kept verbatim",
			in:   "1. item one",
			want: "1. item one",
		},
		{
			name: "task list unchecked",
			in:   "- [ ] task one",
			want: "\u2610 task one",
		},
		{
			name: "task list checked lowercase",
			in:   "- [x] task one",
			want: "\u2611 task one",
		},
		{
			name: "task list checked uppercase",
			in:   "- [X] task one",
			want: "\u2611 task one",
		},
		{
			name: "single blockquote line",
			in:   "> quoted text",
			want: "<blockquote>quoted text</blockquote>",
		},
		{
			name: "merged consecutive blockquote lines",
			in:   "> line one\n> line two",
			want: "<blockquote>line one\nline two</blockquote>",
		},
		{
			name: "pipe table wrapped in pre",
			in:   "| a | b |\n| - | - |\n| 1 | 2 |",
			want: "<pre>| a | b |\n| - | - |\n| 1 | 2 |</pre>",
		},
		{
			name: "horizontal rule dashes",
			in:   "---",
			want: strings.Repeat("\u2014", 10),
		},
		{
			name: "horizontal rule stars",
			in:   "***",
			want: strings.Repeat("\u2014", 10),
		},
		{
			name: "horizontal rule underscores",
			in:   "___",
			want: strings.Repeat("\u2014", 10),
		},
		{
			name: "plain prose escaped",
			in:   "plain prose & <tag>",
			want: "plain prose &amp; &lt;tag&gt;",
		},

		// --- adversarial cases -------------------------------------
		{
			name: "underscores and angle bracket inside code are not emphasised or unescaped",
			in:   "use `a_b_c` and `x < y`",
			want: "use <code>a_b_c</code> and <code>x &lt; y</code>",
		},
		{
			name: "snake_case identifier stays plain, no italic",
			in:   "snake_case_name stays plain",
			want: "snake_case_name stays plain",
		},
		{
			name: "nested italic inside bold",
			in:   "**bold with _nested italic_ inside**",
			want: "<b>bold with <i>nested italic</i> inside</b>",
		},
		{
			name: "ampersand and angle brackets escaped in prose",
			in:   "a < b && c > d",
			want: "a &lt; b &amp;&amp; c &gt; d",
		},
		{
			name: "unclosed bold left as literal text",
			in:   "**unclosed bold",
			want: "**unclosed bold",
		},
		{
			name: "unclosed link left as literal text",
			in:   "[label](https://example.com",
			want: "[label](https://example.com",
		},
		{
			name: "ampersand in href is escaped",
			in:   "[a](https://x?p=1&q=2)",
			want: `<a href="https://x?p=1&amp;q=2">a</a>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToTelegramHTML(tt.in)
			if got != tt.want {
				t.Errorf("ToTelegramHTML(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToTelegramHTML_UnclosedFenceStillProducesClosedPre(t *testing.T) {
	in := "```go\nfunc main() {\n  x := 1\n"
	got := ToTelegramHTML(in)
	if !strings.HasPrefix(got, `<pre><code class="language-go">`) {
		t.Fatalf("got = %q, want it to start with an opened <pre><code> tag", got)
	}
	if !strings.HasSuffix(got, "</code></pre>") {
		t.Fatalf("got = %q, want a closed </code></pre>, even though the source fence never closed", got)
	}
	assertBalancedTags(t, got)
}

func TestToTelegramHTML_NeverPanics(t *testing.T) {
	inputs := []string{
		"",
		"**",
		"__",
		"~~",
		"[",
		"[a](",
		"![",
		"`",
		"```",
		"```lang",
		"_",
		"*",
		strings.Repeat("*", 500),
		"\x00md1\x00 literal placeholder-looking text",
	}
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("ToTelegramHTML(%q) panicked: %v", in, r)
				}
			}()
			_ = ToTelegramHTML(in)
		}()
	}
}

func TestToTelegramHTML_RealisticReplyProducesBalancedOutput(t *testing.T) {
	in := "# Build Report\n\n" +
		"Status: **passed** with _one_ warning.\n\n" +
		"- Ran `go test ./...`\n" +
		"- Coverage: 87%\n" +
		"- [ ] Follow-up: investigate flaky test\n\n" +
		"```go\nfunc main() {\n\tfmt.Println(\"ok\")\n}\n```\n\n" +
		"| Name | Status |\n| --- | --- |\n| build | ok |\n\n" +
		"See [the pipeline](https://ci.example.com/run?id=42&retry=1) for details.\n\n" +
		"> Reviewer note: looks good <needs a second pass>\n"

	got := ToTelegramHTML(in)

	if strings.Contains(got, "\x00") {
		t.Fatalf("output still contains a raw placeholder token: %q", got)
	}
	assertBalancedTags(t, got)
	assertNoStrayAngleBrackets(t, got)

	for _, want := range []string{
		"<b>Build Report</b>",
		"<b>passed</b>",
		"<i>one</i>",
		"<code>go test ./...</code>",
		"\u2022 Coverage: 87%",
		"\u2610 Follow-up",
		`<pre><code class="language-go">`,
		"<pre>| Name | Status |",
		`<a href="https://ci.example.com/run?id=42&amp;retry=1">the pipeline</a>`,
		"<blockquote>Reviewer note: looks good &lt;needs a second pass&gt;</blockquote>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got %q", want, got)
		}
	}
}

// --- test helpers -----------------------------------------------------

// supportedTelegramTags is the fixed tag set Telegram's HTML parse mode
// recognizes. assertBalancedTags scans for exactly these.
var supportedTelegramTags = []string{
	"b", "strong", "i", "em", "u", "ins", "s", "strike", "del",
	"a", "code", "pre", "blockquote", "tg-spoiler",
}

// assertBalancedTags verifies every opening tag in the supported set has
// a matching closing tag, in a simple stack-based scan. It does not
// validate proper nesting order beyond LIFO matching, which is all
// well-formed HTML requires.
func assertBalancedTags(t *testing.T, html string) {
	t.Helper()
	var stack []string
	i := 0
	for i < len(html) {
		if html[i] != '<' {
			i++
			continue
		}
		end := strings.IndexByte(html[i:], '>')
		if end < 0 {
			t.Fatalf("unterminated tag starting at byte %d in %q", i, html)
		}
		tag := html[i+1 : i+end]
		i += end + 1

		closing := strings.HasPrefix(tag, "/")
		if closing {
			tag = tag[1:]
		}
		// Strip attributes (e.g. `a href="..."`) down to the bare name.
		if sp := strings.IndexByte(tag, ' '); sp >= 0 {
			tag = tag[:sp]
		}
		if !isSupportedTag(tag) {
			t.Fatalf("unsupported tag <%s> in output: %q", tag, html)
		}
		if !closing {
			stack = append(stack, tag)
			continue
		}
		if len(stack) == 0 || stack[len(stack)-1] != tag {
			t.Fatalf("unbalanced tag </%s>: stack=%v in %q", tag, stack, html)
		}
		stack = stack[:len(stack)-1]
	}
	if len(stack) != 0 {
		t.Fatalf("unclosed tags remain: %v in %q", stack, html)
	}
}

func isSupportedTag(tag string) bool {
	for _, t := range supportedTelegramTags {
		if t == tag {
			return true
		}
	}
	return false
}

// assertNoStrayAngleBrackets verifies every '<' in html opens a
// recognized tag and every '>' closes one — i.e. no unescaped '<'/'>'
// leaked through from plain text.
func assertNoStrayAngleBrackets(t *testing.T, html string) {
	t.Helper()
	i := 0
	for i < len(html) {
		switch html[i] {
		case '<':
			end := strings.IndexByte(html[i:], '>')
			if end < 0 {
				t.Fatalf("stray '<' with no closing '>' at byte %d in %q", i, html)
			}
			tag := strings.TrimPrefix(html[i+1:i+end], "/")
			if sp := strings.IndexByte(tag, ' '); sp >= 0 {
				tag = tag[:sp]
			}
			if !isSupportedTag(tag) {
				t.Fatalf("stray '<' not part of a recognized tag at byte %d in %q", i, html)
			}
			i += end + 1
		case '>':
			t.Fatalf("stray unescaped '>' at byte %d in %q", i, html)
		default:
			i++
		}
	}
}

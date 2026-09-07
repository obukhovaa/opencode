package markdown

import (
	"strings"
	"testing"
)

// Telegram's Bot API nesting rules (see "Formatting options"):
//
//	bold, italic, underline, strikethrough, and spoiler entities can
//	contain and can be part of any other entities, except pre and code.
//	[...] All other entities can't contain each other.
//
// So a <code> or <pre> inside ANY other tag is rejected with "can't parse
// entities", which costs the whole chunk its formatting via the plain-text
// retry. These tests pin that no such nesting is ever emitted.
func TestToTelegramHTMLNeverNestsCodeInsideAnotherEntity(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"heading with inline code", "## Fix `foo.go` now"},
		{"heading with code and bold", "### Use `--flag` for **speed**"},
		{"bold wrapping inline code", "**run `make test` first**"},
		{"italic wrapping inline code", "*see `config.yaml`*"},
		{"underscore italic wrapping code", "_see `config.yaml` here_"},
		{"strikethrough wrapping code", "~~old `api.Call()` removed~~"},
		{"link label containing code", "[`pkg.Func`](https://example.com)"},
		{"blockquote with inline code", "> note: run `go vet`\n> then build"},
		{"table cell with inline code", "| col | val |\n| --- | --- |\n| `code` | x |"},
		{"nested bold inside italic with code", "*outer **inner `c`** tail*"},
		{"list item with code (top level, allowed)", "- run `make test`"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ToTelegramHTML(tc.in)
			assertNoNestedCodeOrPre(t, got)
		})
	}
}

// assertNoNestedCodeOrPre walks html and fails if a <code> or <pre> open
// tag appears while any other tag is still open, or if any tag is open
// inside a <code>/<pre>. The one exception is the documented
// `<pre><code class="language-x">` language-specifier form.
func assertNoNestedCodeOrPre(t *testing.T, html string) {
	t.Helper()
	var stack []string
	i := 0
	for i < len(html) {
		lt := strings.IndexByte(html[i:], '<')
		if lt < 0 {
			return
		}
		i += lt
		gt := strings.IndexByte(html[i:], '>')
		if gt < 0 {
			t.Fatalf("unterminated tag at byte %d in %q", i, html)
		}
		raw := html[i+1 : i+gt]
		i += gt + 1

		closing := strings.HasPrefix(raw, "/")
		name := strings.TrimPrefix(raw, "/")
		if sp := strings.IndexByte(name, ' '); sp >= 0 {
			name = name[:sp]
		}

		if closing {
			if len(stack) > 0 && stack[len(stack)-1] == name {
				stack = stack[:len(stack)-1]
			}
			continue
		}

		if name == "code" || name == "pre" {
			// `<pre><code class="language-x">` is the one documented,
			// supported nesting.
			preCode := name == "code" && len(stack) == 1 && stack[0] == "pre" &&
				strings.Contains(raw, `class="language-`)
			if len(stack) > 0 && !preCode {
				t.Fatalf("<%s> nested inside %v — Telegram rejects code/pre inside another entity: %q",
					name, stack, html)
			}
		} else if len(stack) > 0 && (stack[len(stack)-1] == "code" || stack[len(stack)-1] == "pre") {
			t.Fatalf("<%s> nested inside <%s> — code/pre may not contain other entities: %q",
				name, stack[len(stack)-1], html)
		}
		stack = append(stack, name)
	}
}

// TestToTelegramHTMLFlattensCodeButKeepsContent asserts the flattening
// degrades markup only, never the code text itself.
func TestToTelegramHTMLFlattensCodeButKeepsContent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "heading",
			in:   "## Fix `foo.go`",
			want: "<b>Fix foo.go</b>",
		},
		{
			name: "bold",
			in:   "**run `make test`**",
			want: "<b>run make test</b>",
		},
		{
			name: "blockquote",
			in:   "> run `go vet`",
			want: "<blockquote>run go vet</blockquote>",
		},
		{
			name: "code content is still escaped when flattened",
			in:   "## compare `a < b && c > d`",
			want: "<b>compare a &lt; b &amp;&amp; c &gt; d</b>",
		},
		{
			name: "top-level code keeps its tag",
			in:   "plain `code` here",
			want: "plain <code>code</code> here",
		},
		{
			name: "top-level fence keeps pre+code",
			in:   "```go\nx := 1\n```",
			want: "<pre><code class=\"language-go\">x := 1</code></pre>",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToTelegramHTML(tc.in); got != tc.want {
				t.Errorf("ToTelegramHTML(%q)\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestToTelegramHTMLEscapesHrefAttribute covers the attribute-injection
// bug: escapeHTML does not escape '"', so a URL containing one closed the
// href early and injected an unsupported attribute into the <a> tag,
// which Telegram rejects.
func TestToTelegramHTMLEscapesHrefAttribute(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "quote in url cannot break out of the attribute",
			in:   `[label](https://x.com" onclick="alert(1))`,
			want: `<a href="https://x.com&quot; onclick=&quot;alert(1">label</a>)`,
		},
		{
			name: "ampersand in query string stays escaped once",
			in:   `[a](https://x?a=1&b=2)`,
			want: `<a href="https://x?a=1&amp;b=2">a</a>`,
		},
		{
			name: "angle brackets in url are escaped",
			in:   `[a](https://x?q=<script>)`,
			want: `<a href="https://x?q=&lt;script&gt;">a</a>`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ToTelegramHTML(tc.in)
			if got != tc.want {
				t.Errorf("ToTelegramHTML(%q)\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
			if strings.Count(got, `"`) != 2 {
				t.Errorf("expected exactly the two href-delimiting quotes, got %q", got)
			}
		})
	}
}

package skill

import (
	"reflect"
	"testing"
)

func TestSubstituteContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		params  SubstituteParams
		want    string
	}{
		{
			name:    "no placeholders no args",
			content: "Just plain text",
			params:  SubstituteParams{},
			want:    "Just plain text",
		},
		{
			name:    "$ARGUMENTS substitution",
			content: "Fix issue $ARGUMENTS now",
			params:  SubstituteParams{Args: "123"},
			want:    "Fix issue 123 now",
		},
		{
			name:    "$ARGUMENTS multiple occurrences",
			content: "$ARGUMENTS and $ARGUMENTS",
			params:  SubstituteParams{Args: "foo"},
			want:    "foo and foo",
		},
		{
			name:    "append when $ARGUMENTS absent",
			content: "Do something",
			params:  SubstituteParams{Args: "my-arg"},
			want:    "Do something\n\nARGUMENTS: my-arg",
		},
		{
			name:    "no append when args empty",
			content: "Do something",
			params:  SubstituteParams{},
			want:    "Do something",
		},
		{
			name:    "$ARGUMENTS[N] positional",
			content: "Migrate $ARGUMENTS[0] from $ARGUMENTS[1] to $ARGUMENTS[2]",
			params:  SubstituteParams{Args: "SearchBar React Vue"},
			want:    "Migrate SearchBar from React to Vue",
		},
		{
			name:    "$N shorthand positional",
			content: "Migrate $0 from $1 to $2",
			params:  SubstituteParams{Args: "SearchBar React Vue"},
			want:    "Migrate SearchBar from React to Vue",
		},
		{
			name:    "out of range positional returns empty",
			content: "$ARGUMENTS[0] and $ARGUMENTS[5]",
			params:  SubstituteParams{Args: "only-one"},
			want:    "only-one and ",
		},
		{
			name:    "out of range shorthand returns empty",
			content: "$0 and $5",
			params:  SubstituteParams{Args: "only-one"},
			want:    "only-one and ",
		},
		{
			name:    "${SKILL_DIR} substitution",
			content: "Read ${SKILL_DIR}/features/foo.md",
			params:  SubstituteParams{SkillDir: "/home/user/.agents/skills/my-skill"},
			want:    "Read /home/user/.agents/skills/my-skill/features/foo.md",
		},
		{
			name:    "${CLAUDE_SKILL_DIR} substitution",
			content: "Read ${CLAUDE_SKILL_DIR}/data.json",
			params:  SubstituteParams{SkillDir: "/path/to/skill"},
			want:    "Read /path/to/skill/data.json",
		},
		{
			name:    "${SESSION_ID} substitution",
			content: "Log to ${SESSION_ID}.log",
			params:  SubstituteParams{SessionID: "abc-123"},
			want:    "Log to abc-123.log",
		},
		{
			name:    "${CLAUDE_SESSION_ID} substitution",
			content: "Session: ${CLAUDE_SESSION_ID}",
			params:  SubstituteParams{SessionID: "xyz-456"},
			want:    "Session: xyz-456",
		},
		{
			name:    "both SKILL_DIR and CLAUDE_SKILL_DIR",
			content: "${SKILL_DIR} and ${CLAUDE_SKILL_DIR}",
			params:  SubstituteParams{SkillDir: "/dir"},
			want:    "/dir and /dir",
		},
		{
			name:    "combined substitution",
			content: "!`cat ${SKILL_DIR}/features/$0.md`\nSession: ${SESSION_ID}",
			params: SubstituteParams{
				Args:      "cron-tool",
				SkillDir:  "/skills/feature-guide",
				SessionID: "sess-1",
			},
			want: "!`cat /skills/feature-guide/features/cron-tool.md`\nSession: sess-1",
		},
		{
			name:    "mixed $ARGUMENTS and $N",
			content: "All: $ARGUMENTS, first: $0, second: $1",
			params:  SubstituteParams{Args: "alpha beta"},
			want:    "All: alpha beta, first: alpha, second: beta",
		},
		{
			name:    "$ARGUMENTS present prevents append",
			content: "Args: $ARGUMENTS",
			params:  SubstituteParams{Args: "test"},
			want:    "Args: test",
		},
		{
			name:    "empty args with $ARGUMENTS placeholder",
			content: "Fix $ARGUMENTS issue",
			params:  SubstituteParams{},
			want:    "Fix  issue",
		},
		{
			name:    "dollar amounts not matched",
			content: "costs $50 per month and $100 total",
			params:  SubstituteParams{Args: "foo"},
			want:    "costs $50 per month and $100 total\n\nARGUMENTS: foo",
		},
		{
			name:    "single digit matched but multi-digit left alone",
			content: "$0 and $10 and $100",
			params:  SubstituteParams{Args: "X"},
			want:    "X and $10 and $100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SubstituteContent(tt.content, tt.params)
			if got != tt.want {
				t.Errorf("SubstituteContent() =\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestSplitArgs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"single", "one", []string{"one"}},
		{"three bare words", "one two three", []string{"one", "two", "three"}},
		{"collapses runs of spaces", "  spaced   out  ", []string{"spaced", "out"}},
		{"tabs and newlines separate", "a\tb\nc", []string{"a", "b", "c"}},
		{"double quotes group", `one "two three" four`, []string{"one", "two three", "four"}},
		{"single quotes group", `one 'two three' four`, []string{"one", "two three", "four"}},
		{"quotes are literal inside single quotes", `'he said "hi"'`, []string{`he said "hi"`}},
		{"apostrophe inside double quotes", `"don't"`, []string{"don't"}},
		{"escaped quote inside double quotes", `"say \"hi\""`, []string{`say "hi"`}},
		{"escaped backslash inside double quotes", `"a\\b"`, []string{`a\b`}},
		{"empty quoted value is preserved", `a "" b`, []string{"a", "", "b"}},
		{"quotes adjacent to bare text join", `pre"quoted arg"post`, []string{"prequoted argpost"}},
		{"backslash outside quotes is literal", `C:\tmp\x`, []string{`C:\tmp\x`}},
		// Unbalanced quotes are a parse failure: fall back to a plain whitespace
		// split so a half-typed argument still yields usable positionals.
		{"unbalanced double quote falls back", `a "b c`, []string{"a", `"b`, "c"}},
		{"unbalanced single quote falls back", `a 'b c`, []string{"a", "'b", "c"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitArgs(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SplitArgs(%q) = %#v, want %#v", tt.input, got, tt.want)
			}
		})
	}
}

func TestQuoteArg(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{"plain", "plain"},
		{"", `""`},
		{"two words", `"two words"`},
		{"tab\there", "\"tab\there\""},
		{`say "hi"`, `"say \"hi\""`},
		{"don't", `"don't"`},
		{`a\b`, `"a\\b"`},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := QuoteArg(tt.value); got != tt.want {
				t.Errorf("QuoteArg(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

// TestQuoteArgsRoundTrip is the contract the argument dialog relies on: values
// collected in the dialog are written into the editor as text, re-parsed at
// submit time, and must come back byte-identical.
func TestQuoteArgsRoundTrip(t *testing.T) {
	cases := [][]string{
		{"one"},
		{"one", "two", "three"},
		{"two words", "third"},
		{"HEAD~3", "src/internal tools"},
		{`say "hi"`, "don't"},
		{`C:\tmp\x`, "plain"},
		{"a", "", "b"},
		{"trailing space "},
	}

	for _, values := range cases {
		joined := QuoteArgs(values)
		got := SplitArgs(joined)
		if !reflect.DeepEqual(got, values) {
			t.Errorf("round trip of %#v via %q = %#v", values, joined, got)
		}
	}
}

package slashcmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/skill"
)

// TestExpandLeavesShellMarkupEnvRefsAlone: a $UPPERCASE inside !`cmd` markup is
// an environment reference belonging to the command, not a parameter. Treating
// it as one both corrupted the shell command and — because a bound name
// suppresses the ARGUMENTS fallback — dropped the user's arguments entirely.
func TestExpandLeavesShellMarkupEnvRefsAlone(t *testing.T) {
	reg := Registry{Commands: []CommandInfo{{
		ID:      "deploy",
		Content: "Run !`ls $HOME` and summarize.",
	}}}

	got, err := Expand("/deploy the logs", reg, ExpandOptions{Interactive: true})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !strings.Contains(got.Prompt, "!`ls $HOME`") {
		t.Errorf("shell markup was rewritten: %q", got.Prompt)
	}
	if !strings.Contains(got.Prompt, "the logs") {
		t.Errorf("arguments were dropped: %q", got.Prompt)
	}
}

// TestNamedPlaceholdersSkipsShellMarkup is the dialog-side half of the same
// rule: an env reference must not become an argument field.
func TestNamedPlaceholdersSkipsShellMarkup(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"only an env ref", "Root is !`echo $PWD`.", nil},
		{"env ref plus a real placeholder", "Look at $TARGET in !`echo $HOME`.", []string{"TARGET"}},
		{"same name inside and outside", "$HOME then !`echo $HOME`", []string{"HOME"}},
		{"unterminated markup is not a span", "!`echo $HOME", []string{"HOME"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NamedPlaceholders(tt.content)
			if len(got) != len(tt.want) {
				t.Fatalf("NamedPlaceholders = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("NamedPlaceholders = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestExpandInsertsArgumentValuesLiterally: a value the user typed must land in
// the prompt as typed. Binding names before SubstituteContent fed the value
// back through the positional passes, so `/scope $5 src/` bound $TARGET to the
// empty string instead of to "$5".
func TestExpandInsertsArgumentValuesLiterally(t *testing.T) {
	reg := Registry{Commands: []CommandInfo{{
		ID:      "scope",
		Content: "Look at $TARGET inside $SCOPE.",
	}}}

	tests := []struct {
		in   string
		want string
	}{
		{`/scope $5 src/`, "Look at $5 inside src/."},
		{`/scope '${SESSION_ID}' x`, "Look at ${SESSION_ID} inside x."},
		{`/scope '$ARGUMENTS' x`, "Look at $ARGUMENTS inside x."},
		{`/scope plain src/`, "Look at plain inside src/."},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := Expand(tt.in, reg, ExpandOptions{Interactive: true, SessionID: "S1"})
			if err != nil {
				t.Fatalf("Expand: %v", err)
			}
			if got.Prompt != tt.want {
				t.Errorf("Prompt = %q, want %q", got.Prompt, tt.want)
			}
		})
	}
}

// TestExpandRejectsEmptyResult: a command whose body is empty expands to
// nothing, and sending that would create a blank user message.
func TestExpandRejectsEmptyResult(t *testing.T) {
	reg := Registry{Commands: []CommandInfo{{ID: "stub", Content: ""}}}

	if _, err := Expand("/stub", reg, ExpandOptions{Interactive: true}); !errors.Is(err, ErrEmptyExpansion) {
		t.Fatalf("err = %v, want ErrEmptyExpansion", err)
	}

	// Prose alongside it still makes a sendable message.
	got, err := Expand("/stub\nplease look at the diff", reg, ExpandOptions{Interactive: true})
	if err != nil {
		t.Fatalf("Expand with prose: %v", err)
	}
	if !strings.Contains(got.Prompt, "please look at the diff") {
		t.Errorf("Prompt = %q", got.Prompt)
	}
}

// TestScanInlineCodeSpanIsNotAFence: a prose line beginning with an inline code
// span used to open a fence that never closed, silently swallowing every later
// invocation in the message.
func TestScanInlineCodeSpanIsNotAFence(t *testing.T) {
	reg := Registry{Commands: []CommandInfo{{ID: "commit", Content: "Commit it."}}}

	tests := []struct {
		name string
		text string
		want int
	}{
		{"inline span in prose", "```/commit``` is a command\n/commit", 1},
		{"real fence still hides its body", "```\n/commit\n```\n/commit", 1},
		{"fence with info string", "```go\n/commit\n```\n/commit", 1},
		{"four-backtick fence still opens", "````\n/commit\n````\n/commit", 1},
		{"tilde fence still opens", "~~~\n/commit\n~~~\n/commit", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(Scan(tt.text, reg)); got != tt.want {
				t.Errorf("Scan found %d invocations, want %d", got, tt.want)
			}
		})
	}
}

// TestQuoteArgsRoundTripsUnicodeWhitespace: QuoteArg decided what to quote with
// an ASCII-only test while SplitArgs split on unicode.IsSpace, so a value
// carrying a non-breaking space — ordinary in text pasted from a document —
// was re-split and shifted every later positional by one.
func TestQuoteArgsRoundTripsUnicodeWhitespace(t *testing.T) {
	tests := [][]string{
		{"Q1 2026", "src/"},
		{"a b"},
		{" "},
		{"a\vb", "c"},
		{"plain", "two words", ""},
	}
	for _, values := range tests {
		quoted := skill.QuoteArgs(values)
		got := skill.SplitArgs(quoted)
		if len(got) != len(values) {
			t.Fatalf("SplitArgs(QuoteArgs(%q)) = %q (quoted %q)", values, got, quoted)
		}
		for i := range values {
			if got[i] != values[i] {
				t.Fatalf("SplitArgs(QuoteArgs(%q)) = %q (quoted %q)", values, got, quoted)
			}
		}
	}
}

// TestSubstituteContentKeepsLongerArgumentsNames: $ARGUMENTS is a whole token.
// Rewriting it inside a longer name turned $ARGUMENTS_DIR into "<args>_DIR",
// and made the reordered named binding unsafe.
func TestSubstituteContentKeepsLongerArgumentsNames(t *testing.T) {
	got := skill.SubstituteContent("look in $ARGUMENTS_DIR now", skill.SubstituteParams{Args: "X"})
	if !strings.Contains(got, "$ARGUMENTS_DIR") {
		t.Errorf("SubstituteContent rewrote a longer name: %q", got)
	}

	// The bare token and the indexed form still substitute.
	if got := skill.SubstituteContent("use $ARGUMENTS.", skill.SubstituteParams{Args: "X"}); got != "use X." {
		t.Errorf("bare $ARGUMENTS = %q", got)
	}
	if got := skill.SubstituteContent("use $ARGUMENTS[0].", skill.SubstituteParams{Args: "X Y"}); got != "use X." {
		t.Errorf("$ARGUMENTS[0] = %q", got)
	}
	// Content declaring only $ARGUMENTS[N] must not also get the appended line.
	if got := skill.SubstituteContent("use $ARGUMENTS[0].", skill.SubstituteParams{Args: "X"}); strings.Contains(got, "ARGUMENTS: ") {
		t.Errorf("indexed-only content got the append: %q", got)
	}
}

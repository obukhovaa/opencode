package skill

import "testing"

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
		input string
		want  int
	}{
		{"", 0},
		{"one", 1},
		{"one two three", 3},
		{"  spaced   out  ", 2},
	}

	for _, tt := range tests {
		got := splitArgs(tt.input)
		if len(got) != tt.want {
			t.Errorf("splitArgs(%q) = %d items, want %d", tt.input, len(got), tt.want)
		}
	}
}

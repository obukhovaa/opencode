package slashcmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/skill"
)

// testRegistry mirrors the shapes that matter: an action command, prompt
// commands with each placeholder style, a plain skill, a skill whose body
// itself starts a line with a command name, and a skill that is not
// user-invocable.
func testRegistry() Registry {
	return Registry{
		Commands: []CommandInfo{
			{ID: "compact", Title: "Compact", TUIOnly: true},
			{ID: "new", Title: "New", TUIOnly: true},
			{ID: "commit", Title: "Commit", Content: "Commit the staged changes."},
			{ID: "review", Title: "Review", Content: "Review $ARGUMENTS carefully."},
			{ID: "project:scope", Title: "Scope", Content: "Look at $TARGET inside $SCOPE."},
			{ID: "project:pos", Title: "Positional", Content: "From $0 to $1."},
			{ID: "project:rename", Title: "Rename", TUIOnly: true, ArgumentHint: "[new title]"},
		},
		Skills: []skill.Info{
			{
				Name:     "reviewer",
				Location: "/skills/reviewer/SKILL.md",
				Content:  "Review the diff.",
			},
			{
				Name:     "argy",
				Location: "/skills/argy/SKILL.md",
				Content:  "Handle $ARGUMENTS then stop.",
			},
			{
				Name:     "posy",
				Location: "/skills/posy/SKILL.md",
				Content:  "First $0 then $1.",
			},
			{
				Name:     "dirry",
				Location: "/skills/dirry/SKILL.md",
				Content:  "Read ${SKILL_DIR}/ref.md for ${SESSION_ID}.",
			},
			{
				// Its body contains a line that would itself scan as an
				// invocation if the output were ever re-scanned.
				Name:     "nested",
				Location: "/skills/nested/SKILL.md",
				Content:  "Do this:\n/review the diff\nthen stop.",
			},
			{
				Name:          "internal-helper",
				Location:      "/skills/internal-helper/SKILL.md",
				Content:       "internal",
				UserInvocable: boolPtr(false),
			},
		},
	}
}

func TestScan(t *testing.T) {
	reg := testRegistry()

	type want struct {
		line int
		kind Kind
		name string
		args string
	}

	tests := []struct {
		name  string
		text  string
		wants []want
	}{
		{
			name:  "single leading invocation",
			text:  "/skill:reviewer look at HEAD~1",
			wants: []want{{0, KindPrompt, "skill:reviewer", "look at HEAD~1"}},
		},
		{
			name: "two invocations with prose between",
			text: "/skill:reviewer first\nmind the tests\n/commit",
			wants: []want{
				{0, KindPrompt, "skill:reviewer", "first"},
				{2, KindPrompt, "commit", ""},
			},
		},
		{
			name:  "action command",
			text:  "/compact",
			wants: []want{{0, KindAction, "compact", ""}},
		},
		{
			name:  "indented slash is not an invocation",
			text:  "  /commit",
			wants: nil,
		},
		{
			name:  "mid-line mention is not an invocation",
			text:  "remember that /commit runs the git flow",
			wants: nil,
		},
		{
			name:  "unknown name is unresolved",
			text:  "/notacommand do a thing",
			wants: []want{{0, KindUnresolved, "notacommand", "do a thing"}},
		},
		{
			name:  "backtick fence hides invocations",
			text:  "look:\n```\n/review\n/commit\n```\ndone",
			wants: nil,
		},
		{
			name:  "tilde fence hides invocations",
			text:  "look:\n~~~\n/review\n~~~",
			wants: nil,
		},
		{
			name:  "a tilde inside a backtick fence does not close it",
			text:  "```\n~~~\n/review\n```\n/commit",
			wants: []want{{4, KindPrompt, "commit", ""}},
		},
		{
			name:  "unclosed fence swallows the rest",
			text:  "/commit\n```\n/review\n/skill:reviewer",
			wants: []want{{0, KindPrompt, "commit", ""}},
		},
		{
			name:  "escaped line is not resolved",
			text:  `\/review this by hand`,
			wants: []want{{0, KindUnresolved, "", ""}},
		},
		{
			name:  "base name resolves a prefixed custom command",
			text:  "/scope HEAD~3 src/",
			wants: []want{{0, KindPrompt, "scope", "HEAD~3 src/"}},
		},
		{
			name:  "tab separates name from args",
			text:  "/review\tHEAD~1",
			wants: []want{{0, KindPrompt, "review", "HEAD~1"}},
		},
		{
			name:  "bare slash is skipped",
			text:  "/",
			wants: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Scan(tt.text, reg)
			if len(got) != len(tt.wants) {
				t.Fatalf("Scan(%q) returned %d invocations, want %d: %#v", tt.text, len(got), len(tt.wants), got)
			}
			for i, w := range tt.wants {
				if got[i].Line != w.line || got[i].Kind != w.kind || got[i].Name != w.name || got[i].Args != w.args {
					t.Errorf("invocation %d = {line:%d kind:%v name:%q args:%q}, want {line:%d kind:%v name:%q args:%q}",
						i, got[i].Line, got[i].Kind, got[i].Name, got[i].Args, w.line, w.kind, w.name, w.args)
				}
			}
		})
	}
}

func TestScanFlagsNonUserInvocableSkill(t *testing.T) {
	got := Scan("/skill:internal-helper go", testRegistry())
	if len(got) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(got))
	}
	if !errors.Is(got[0].Err, ErrNotUserInvocable) {
		t.Errorf("Err = %v, want ErrNotUserInvocable", got[0].Err)
	}
}

func TestExpand(t *testing.T) {
	reg := testRegistry()
	opts := ExpandOptions{SessionID: "sess-abc", Interactive: true}

	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "no invocation is byte-identical",
			text: "just a message\nwith two lines\n",
			want: "just a message\nwith two lines\n",
		},
		{
			name: "skill is wrapped",
			text: "/skill:reviewer",
			want: "<skill_content name=\"reviewer\">\nReview the diff.\n</skill_content>",
		},
		{
			name: "skill args fill $ARGUMENTS",
			text: "/skill:argy the tests",
			want: "<skill_content name=\"argy\">\nHandle the tests then stop.\n</skill_content>",
		},
		{
			name: "skill args are appended when no placeholder is declared",
			text: "/skill:reviewer only the README",
			want: "<skill_content name=\"reviewer\">\nReview the diff.\n\nARGUMENTS: only the README\n</skill_content>",
		},
		{
			name: "quoted positional stays whole",
			text: `/skill:posy "two words" third`,
			want: "<skill_content name=\"posy\">\nFirst two words then third.\n</skill_content>",
		},
		{
			name: "skill dir and session id substitute",
			text: "/skill:dirry",
			want: "<skill_content name=\"dirry\">\nRead /skills/dirry/ref.md for sess-abc.\n</skill_content>",
		},
		{
			name: "command is not wrapped",
			text: "/commit",
			want: "Commit the staged changes.",
		},
		{
			name: "command $ARGUMENTS binds the whole string",
			text: "/review the last two commits",
			want: "Review the last two commits carefully.",
		},
		{
			name: "named placeholders bind by first appearance",
			text: "/scope HEAD~3 src/",
			want: "Look at HEAD~3 inside src/.",
		},
		{
			name: "unfilled named placeholder binds empty and is not restated",
			text: "/scope HEAD~3",
			want: "Look at HEAD~3 inside .",
		},
		{
			name: "command positional placeholders bind",
			text: "/pos a b",
			want: "From a to b.",
		},
		{
			name: "two invocations and prose keep their order",
			text: "/skill:reviewer first\nmind the tests\n/commit",
			want: "<skill_content name=\"reviewer\">\nReview the diff.\n\nARGUMENTS: first\n</skill_content>\nmind the tests\nCommit the staged changes.",
		},
		{
			name: "unresolved line is left alone",
			text: "/notacommand do a thing",
			want: "/notacommand do a thing",
		},
		{
			name: "escape is stripped without expanding",
			text: `\/review this by hand`,
			want: "/review this by hand",
		},
		{
			name: "escape works alongside an expansion",
			text: "/commit\n\\/review by hand",
			want: "Commit the staged changes.\n/review by hand",
		},
		{
			name: "fenced invocations are untouched",
			text: "look:\n```\n/review\n```",
			want: "look:\n```\n/review\n```",
		},
		{
			// The expanded body of `nested` contains a line starting with
			// /review. Expansion runs once, so that line must survive verbatim.
			name: "expanded content is not re-expanded",
			text: "/skill:nested",
			want: "<skill_content name=\"nested\">\nDo this:\n/review the diff\nthen stop.\n</skill_content>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Expand(tt.text, reg, opts)
			if err != nil {
				t.Fatalf("Expand() error = %v", err)
			}
			if got.Action != nil {
				t.Fatalf("Expand() returned action %q, want a prompt", got.Action.ID)
			}
			if got.Prompt != tt.want {
				t.Errorf("Expand() prompt =\n%q\nwant\n%q", got.Prompt, tt.want)
			}
		})
	}
}

func TestExpandAction(t *testing.T) {
	reg := testRegistry()
	opts := ExpandOptions{Interactive: true}

	t.Run("bare action returns the command", func(t *testing.T) {
		got, err := Expand("/compact", reg, opts)
		if err != nil {
			t.Fatalf("Expand() error = %v", err)
		}
		if got.Action == nil || got.Action.ID != "compact" {
			t.Fatalf("Action = %#v, want compact", got.Action)
		}
		if got.Prompt != "" {
			t.Errorf("Prompt = %q, want empty", got.Prompt)
		}
	})

	t.Run("surrounding blank lines still count as bare", func(t *testing.T) {
		got, err := Expand("\n/compact\n  \n", reg, opts)
		if err != nil {
			t.Fatalf("Expand() error = %v", err)
		}
		if got.Action == nil {
			t.Fatal("Action = nil, want compact")
		}
	})

	t.Run("action args are reported", func(t *testing.T) {
		got, err := Expand("/rename a new title", reg, opts)
		if err != nil {
			t.Fatalf("Expand() error = %v", err)
		}
		if got.Action == nil || got.ActionArgs != "a new title" {
			t.Fatalf("Action=%#v ActionArgs=%q", got.Action, got.ActionArgs)
		}
	})
}

func TestExpandErrors(t *testing.T) {
	reg := testRegistry()

	tests := []struct {
		name    string
		text    string
		opts    ExpandOptions
		wantErr error
	}{
		{
			name:    "action mixed with prose",
			text:    "/new\nand then review the diff",
			opts:    ExpandOptions{Interactive: true},
			wantErr: ErrActionNotComposable,
		},
		{
			// /new takes no arguments, so trailing text on its own line is the
			// user meaning something else, not an argument to drop.
			name:    "argument-less action with trailing text",
			text:    "/new and then review the diff",
			opts:    ExpandOptions{Interactive: true},
			wantErr: ErrActionNotComposable,
		},
		{
			name:    "action mixed with a prompt invocation",
			text:    "/new\n/commit",
			opts:    ExpandOptions{Interactive: true},
			wantErr: ErrActionNotComposable,
		},
		{
			name:    "two action commands",
			text:    "/new\n/compact",
			opts:    ExpandOptions{Interactive: true},
			wantErr: ErrActionNotComposable,
		},
		{
			name:    "non-user-invocable skill",
			text:    "/skill:internal-helper go",
			opts:    ExpandOptions{Interactive: true},
			wantErr: ErrNotUserInvocable,
		},
		{
			name:    "tui-only command outside the tui",
			text:    "/compact",
			opts:    ExpandOptions{Interactive: false},
			wantErr: ErrTUIOnly,
		},
		{
			name:    "over the invocation cap",
			text:    strings.Repeat("/commit\n", MaxInvocationsPerMessage+1),
			opts:    ExpandOptions{Interactive: true},
			wantErr: ErrTooManyInvocations,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Expand(tt.text, reg, tt.opts)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Expand() error = %v, want %v", err, tt.wantErr)
			}
			if got.Prompt != "" || got.Action != nil {
				t.Errorf("Expand() returned %#v on error, want the zero Expansion", got)
			}
		})
	}
}

func TestExpandAtTheCap(t *testing.T) {
	reg := testRegistry()
	text := strings.Repeat("/commit\n", MaxInvocationsPerMessage)
	got, err := Expand(text, reg, ExpandOptions{Interactive: true})
	if err != nil {
		t.Fatalf("Expand() error = %v at exactly the cap", err)
	}
	if got.Count != MaxInvocationsPerMessage {
		t.Errorf("Count = %d, want %d", got.Count, MaxInvocationsPerMessage)
	}
}

func TestExpandShellMarkupAppliesOnlyInsideBlocks(t *testing.T) {
	reg := Registry{
		Commands: []CommandInfo{{ID: "commit", Content: "Commit !`git rev-parse HEAD`."}},
	}
	opts := ExpandOptions{
		Interactive: true,
		ShellExpand: func(s string) string {
			return strings.ReplaceAll(s, "!`git rev-parse HEAD`", "abc123")
		},
	}

	got, err := Expand("/commit\nalso check !`git status`", reg, opts)
	if err != nil {
		t.Fatalf("Expand() error = %v", err)
	}
	want := "Commit abc123.\nalso check !`git status`"
	if got.Prompt != want {
		t.Errorf("Expand() prompt = %q, want %q", got.Prompt, want)
	}
}

func TestExpandNilShellHookLeavesMarkup(t *testing.T) {
	reg := Registry{Commands: []CommandInfo{{ID: "commit", Content: "Commit !`git rev-parse HEAD`."}}}
	got, err := Expand("/commit", reg, ExpandOptions{Interactive: true})
	if err != nil {
		t.Fatalf("Expand() error = %v", err)
	}
	if got.Prompt != "Commit !`git rev-parse HEAD`." {
		t.Errorf("Expand() prompt = %q, want the markup untouched", got.Prompt)
	}
}

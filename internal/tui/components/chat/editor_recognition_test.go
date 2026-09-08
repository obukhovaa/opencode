package chat

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/skill"
	"github.com/opencode-ai/opencode/internal/slashcmd"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
)

// testScanner is the real scanner over a small registry, so these tests
// exercise the resolution the hint actually reports.
func testScanner() InvocationScanner {
	reg := slashcmd.Registry{
		Commands: []slashcmd.CommandInfo{
			{ID: "commit", Content: "Commit the staged changes."},
			{ID: "compact", TUIOnly: true},
		},
		Skills: []skill.Info{
			{Name: "reviewer", Location: "/skills/reviewer/SKILL.md", Content: "Review the diff."},
		},
	}
	return func(text string) []slashcmd.Invocation {
		return slashcmd.Scan(text, reg)
	}
}

func newRecognizingEditor() *editorCmp {
	ed := newTestEditor()
	ed.scan = testScanner()
	return ed
}

// typeDraft replaces the draft and refreshes the hint, the same effect a
// keystroke has via the Update wrapper (asserted separately by
// TestEditorRecognitionRefreshedFromUpdate).
func typeDraft(ed *editorCmp, draft string) *editorCmp {
	ed.textarea.SetValue(draft)
	ed.refreshRecognition()
	return ed
}

func TestEditorRecognitionHint(t *testing.T) {
	tests := []struct {
		name  string
		draft string
		want  []recognizedInvocation
	}{
		{
			name:  "plain prose is not recognized",
			draft: "just a message about /commit in passing",
			want:  nil,
		},
		{
			name:  "a resolvable skill is recognized",
			draft: "/skill:reviewer the diff",
			want:  []recognizedInvocation{{label: "/skill:reviewer"}},
		},
		{
			// The signal the user asked for: a bare skill name does not resolve,
			// so no chip appears and they can see it will be sent as plain text.
			name:  "a bare skill name is not recognized",
			draft: "/reviewer the diff",
			want:  nil,
		},
		{
			name:  "an unknown command is not recognized",
			draft: "/notacommand do a thing",
			want:  nil,
		},
		{
			name:  "an indented invocation is not recognized",
			draft: "  /commit",
			want:  nil,
		},
		{
			name:  "a fenced invocation is not recognized",
			draft: "look:\n```\n/commit\n```",
			want:  nil,
		},
		{
			name:  "an escaped invocation is not recognized",
			draft: `\/commit`,
			want:  nil,
		},
		{
			name:  "an action command is marked as an action",
			draft: "/compact",
			want:  []recognizedInvocation{{label: "/compact", action: true}},
		},
		{
			name:  "several invocations are reported in order",
			draft: "/skill:reviewer first\nmind the tests\n/commit",
			want: []recognizedInvocation{
				{label: "/skill:reviewer"},
				{label: "/commit"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ed := typeDraft(newRecognizingEditor(), tt.draft)

			if len(ed.recognized) != len(tt.want) {
				t.Fatalf("recognized = %#v, want %#v", ed.recognized, tt.want)
			}
			for i, w := range tt.want {
				if ed.recognized[i] != w {
					t.Errorf("recognized[%d] = %#v, want %#v", i, ed.recognized[i], w)
				}
			}
		})
	}
}

// TestEditorRecognitionRefreshedFromUpdate asserts the wiring: typing through
// Update refreshes the hint, so no branch has to remember to do it.
func TestEditorRecognitionRefreshedFromUpdate(t *testing.T) {
	ed := newRecognizingEditor()
	ed.SetSize(80, 10) //nolint:errcheck

	var model tea.Model = ed
	for _, r := range "/commit" {
		model, _ = model.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ed = model.(*editorCmp)

	if len(ed.recognized) != 1 || ed.recognized[0].label != "/commit" {
		t.Fatalf("recognized = %#v after typing, want one /commit chip", ed.recognized)
	}
	if got := ed.textarea.Height(); got != 9 {
		t.Errorf("textarea height = %d, want 9 with the hint row reserved", got)
	}
}

// TestEditorRecognitionClearsWhenDraftChanges: the hint must track the draft in
// both directions, or a stale chip promises an expansion that will not happen.
func TestEditorRecognitionClearsWhenDraftChanges(t *testing.T) {
	ed := typeDraft(newRecognizingEditor(), "/commit")
	if len(ed.recognized) != 1 {
		t.Fatalf("recognized = %#v, want one chip", ed.recognized)
	}

	ed = typeDraft(ed, "commit the changes")
	if len(ed.recognized) != 0 {
		t.Errorf("recognized = %#v after the invocation was edited away, want none", ed.recognized)
	}
}

// TestEditorRecognitionIgnoredInShellMode: in shell mode the draft is a shell
// command, so a leading slash is a path, not an invocation.
func TestEditorRecognitionIgnoredInShellMode(t *testing.T) {
	ed := newRecognizingEditor()
	ed.mode = modeShell
	ed = typeDraft(ed, "/commit")

	if len(ed.recognized) != 0 {
		t.Errorf("recognized = %#v in shell mode, want none", ed.recognized)
	}
}

// TestEditorRecognitionWithoutAScanner keeps the component usable with no chat
// page behind it — the pre-existing editor tests construct it that way.
func TestEditorRecognitionWithoutAScanner(t *testing.T) {
	ed := newTestEditor()
	ed.SetSize(80, 10) //nolint:errcheck
	ed = typeDraft(ed, "/commit")
	if len(ed.recognized) != 0 {
		t.Errorf("recognized = %#v with a nil scanner, want none", ed.recognized)
	}
	if got := ed.textarea.Height(); got != 10 {
		t.Errorf("textarea height = %d, want the full height when no row is reserved", got)
	}
}

// TestEditorRecognitionHeightSync asserts the chat-editor-layout invariant for
// the new affordance: the reserved row is computed in Update, and it is one row
// whether the attachment bar, the hint, or both are present.
func TestEditorRecognitionHeightSync(t *testing.T) {
	ed := newRecognizingEditor()
	ed.SetSize(80, 10) //nolint:errcheck

	if got := ed.textarea.Height(); got != 10 {
		t.Fatalf("empty draft: height = %d, want 10", got)
	}

	ed = typeDraft(ed, "/commit")
	if got := ed.textarea.Height(); got != 9 {
		t.Fatalf("with a recognized invocation: height = %d, want 9", got)
	}

	// Both affordances share the single row, so the reservation stays at one.
	model, _ := ed.Update(dialog.AttachmentAddedMsg{
		Attachment: message.Attachment{FileName: "test.png"},
	})
	ed = model.(*editorCmp)
	if got := ed.textarea.Height(); got != 9 {
		t.Fatalf("with an attachment and the hint: height = %d, want 9", got)
	}

	// Editing the invocation away leaves the attachment row, so still one row.
	ed = typeDraft(ed, "no invocation here")
	if got := ed.textarea.Height(); got != 9 {
		t.Fatalf("attachment only: height = %d, want 9", got)
	}

	ed.attachments = nil
	ed.syncTextareaHeight()
	if got := ed.textarea.Height(); got != 10 {
		t.Fatalf("no affordances: height = %d, want 10", got)
	}
}

// TestEditorCmpNoOverflowWithRecognitionHint extends the no-overflow invariant
// to the new affordance row. Mirrors TestEditorCmpNoOverflowWithAttachment: only
// widths where the chip itself fits are asserted.
func TestEditorCmpNoOverflowWithRecognitionHint(t *testing.T) {
	widths := []int{20, 40, 80, 120}

	for _, w := range widths {
		t.Run(fmt.Sprintf("w=%d/hint", w), func(t *testing.T) {
			ed := newRecognizingEditor()
			ed.SetSize(w, 10) //nolint:errcheck
			ed = typeDraft(ed, "/skill:reviewer the diff\n/commit")
			ed.SetSize(w, 10) //nolint:errcheck

			view := ed.View()
			if got := lipgloss.Width(view.Content); got > w {
				t.Errorf("View width %d exceeds container %d", got, w)
			}
			for i, line := range strings.Split(view.Content, "\n") {
				if got := lipgloss.Width(line); got > w {
					t.Errorf("line %d width %d exceeds container %d", i, got, w)
				}
			}
		})
	}
}

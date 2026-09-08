package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/skill"
	"github.com/opencode-ai/opencode/internal/slashcmd"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

// testExpander is the real expander over a small registry, so these tests
// exercise the same code path the chat page wires up.
func testExpander() SubmissionExpander {
	reg := slashcmd.Registry{
		Commands: []slashcmd.CommandInfo{
			{ID: "compact", Title: "Compact", TUIOnly: true},
			{ID: "commit", Title: "Commit", Content: "Commit the staged changes."},
		},
		Skills: []skill.Info{
			{Name: "reviewer", Location: "/skills/reviewer/SKILL.md", Content: "Review the diff."},
			{Name: "hidden", Location: "/skills/hidden/SKILL.md", Content: "no", UserInvocable: new(bool)},
		},
	}
	return func(text string) (slashcmd.Expansion, error) {
		return slashcmd.Expand(text, reg, slashcmd.ExpandOptions{
			SessionID:   "test-session",
			Interactive: true,
		})
	}
}

// TestEditor_send_BusyEnqueuesExpandedPrompt is the regression that motivated
// moving expansion in front of the queue: the drain worker hands
// QueuedMessage.Text straight to agent.Run, so an unexpanded queue entry means
// the model receives the literal "/skill:reviewer ..." string.
func TestEditor_send_BusyEnqueuesExpandedPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ed, a := newEditorForTest(ctx, &editorFakeAgent{busy: true})
	ed.expand = testExpander()
	ed.textarea.SetValue("/skill:reviewer the diff")

	if cmd := ed.send(); cmd != nil {
		t.Fatalf("send() returned a cmd while busy: %#v", cmdProducesMsg(cmd))
	}

	queued := a.QueuedMessages("test-session")
	if len(queued) != 1 {
		t.Fatalf("queue holds %d messages, want 1", len(queued))
	}
	if !strings.Contains(queued[0].Text, `<skill_content name="reviewer">`) {
		t.Errorf("queued text was not expanded: %q", queued[0].Text)
	}
	if strings.Contains(queued[0].Text, "/skill:reviewer") {
		t.Errorf("queued text still carries the raw invocation: %q", queued[0].Text)
	}
	if ed.textarea.Value() != "" {
		t.Errorf("textarea = %q after a successful submit, want empty", ed.textarea.Value())
	}
}

func TestEditor_send_IdleDispatchesExpandedPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ed, _ := newEditorForTest(ctx, &editorFakeAgent{})
	ed.expand = testExpander()
	ed.textarea.SetValue("/commit\nand mention the ticket")

	msg := cmdProducesMsg(ed.send())
	send, ok := msg.(SendMsg)
	if !ok {
		t.Fatalf("send() produced %T, want SendMsg", msg)
	}
	want := "Commit the staged changes.\nand mention the ticket"
	if send.Text != want {
		t.Errorf("SendMsg.Text = %q, want %q", send.Text, want)
	}
}

// TestEditor_send_FailedExpansionPreservesInput covers every rejection path:
// the message must survive so the user can fix it.
func TestEditor_send_FailedExpansionPreservesInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"action command mixed with prose", "/compact and then look at the diff"},
		{"action command mixed with an invocation", "/compact\n/commit"},
		{"skill that is not user-invocable", "/skill:hidden go"},
		{"over the invocation cap", strings.Repeat("/commit\n", slashcmd.MaxInvocationsPerMessage+1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			ed, a := newEditorForTest(ctx, &editorFakeAgent{})
			ed.expand = testExpander()
			ed.textarea.SetValue(tt.input)

			msg := cmdProducesMsg(ed.send())
			info, ok := msg.(util.InfoMsg)
			if !ok {
				t.Fatalf("send() produced %T, want a warning InfoMsg", msg)
			}
			if info.Type != util.InfoTypeWarn {
				t.Errorf("InfoMsg.Type = %v, want warn", info.Type)
			}
			if ed.textarea.Value() != tt.input {
				t.Errorf("textarea = %q, want the submitted text preserved (%q)", ed.textarea.Value(), tt.input)
			}
			if n := len(a.QueuedMessages("test-session")); n != 0 {
				t.Errorf("queue holds %d messages after a rejected submit, want 0", n)
			}
		})
	}
}

func TestEditor_send_BareActionCommandRunsAction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ed, a := newEditorForTest(ctx, &editorFakeAgent{})
	ed.expand = testExpander()
	ed.textarea.SetValue("/compact")

	msg := cmdProducesMsg(ed.send())
	action, ok := msg.(RunActionMsg)
	if !ok {
		t.Fatalf("send() produced %T, want RunActionMsg", msg)
	}
	if action.Command == nil || action.Command.ID != "compact" {
		t.Errorf("RunActionMsg.Command = %#v, want compact", action.Command)
	}
	if ed.textarea.Value() != "" {
		t.Errorf("textarea = %q, want empty after an action command", ed.textarea.Value())
	}
	if n := len(a.QueuedMessages("test-session")); n != 0 {
		t.Errorf("queue holds %d messages, want 0 — an action command sends nothing", n)
	}
}

// TestEditor_send_NilExpanderPassesThrough keeps the component usable without a
// chat page behind it (the pre-existing editor tests rely on this).
func TestEditor_send_NilExpanderPassesThrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ed, _ := newEditorForTest(ctx, &editorFakeAgent{})
	ed.textarea.SetValue("/commit stays literal")

	msg := cmdProducesMsg(ed.send())
	send, ok := msg.(SendMsg)
	if !ok {
		t.Fatalf("send() produced %T, want SendMsg", msg)
	}
	if send.Text != "/commit stays literal" {
		t.Errorf("SendMsg.Text = %q, want the input untouched", send.Text)
	}
}

func TestEditor_StageInvocation(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		staged   string
		want     string
	}{
		{
			name:   "into an empty editor",
			staged: "/skill:reviewer ",
			want:   "/skill:reviewer ",
		},
		{
			name:     "mid-line staging starts a new line",
			existing: "look at this:",
			staged:   "/skill:reviewer ",
			want:     "look at this:\n/skill:reviewer ",
		},
		{
			name:     "at column 0 no extra newline is added",
			existing: "look at this:\n",
			staged:   "/skill:reviewer ",
			want:     "look at this:\n/skill:reviewer ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			ed, _ := newEditorForTest(ctx, &editorFakeAgent{})
			ed.textarea.SetValue(tt.existing)

			model, cmd := ed.Update(dialog.StageInvocationMsg{Text: tt.staged})
			if cmd != nil {
				t.Errorf("staging returned a cmd: %#v", cmdProducesMsg(cmd))
			}
			got := model.(*editorCmp).textarea.Value()
			if got != tt.want {
				t.Errorf("textarea = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestEditor_StageInvocationAccumulates is the composition case: two staged
// invocations plus the user's own prose become one message.
func TestEditor_StageInvocationAccumulates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ed, _ := newEditorForTest(ctx, &editorFakeAgent{})
	ed.expand = testExpander()

	ed.Update(dialog.StageInvocationMsg{Text: "/skill:reviewer the diff"})
	ed.textarea.InsertString("\nmind the tests")
	ed.Update(dialog.StageInvocationMsg{Text: "/commit"})

	if got := ed.textarea.Value(); got != "/skill:reviewer the diff\nmind the tests\n/commit" {
		t.Fatalf("staged editor content = %q", got)
	}

	msg := cmdProducesMsg(ed.send())
	send, ok := msg.(SendMsg)
	if !ok {
		t.Fatalf("send() produced %T, want SendMsg", msg)
	}
	want := "<skill_content name=\"reviewer\">\nReview the diff.\n\nARGUMENTS: the diff\n</skill_content>\nmind the tests\nCommit the staged changes."
	if send.Text != want {
		t.Errorf("SendMsg.Text =\n%q\nwant\n%q", send.Text, want)
	}
}

// TestEditor_ExternalEditorContentIsExpanded covers the ctrl+e path, which used
// to emit SendMsg directly and would have bypassed expansion entirely.
func TestEditor_ExternalEditorContentIsExpanded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ed, _ := newEditorForTest(ctx, &editorFakeAgent{})
	ed.expand = testExpander()

	_, cmd := ed.Update(editorContentMsg{Text: "/commit"})
	msg := cmdProducesMsg(cmd)
	send, ok := msg.(SendMsg)
	if !ok {
		t.Fatalf("external editor content produced %T, want SendMsg", msg)
	}
	if send.Text != "Commit the staged changes." {
		t.Errorf("SendMsg.Text = %q, want the expanded command", send.Text)
	}
}

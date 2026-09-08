package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
)

func TestConsumesInput(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.Msg
		want bool
	}{
		{"key press", tea.KeyPressMsg{Code: 'a', Text: "a"}, true},
		{"paste", tea.PasteMsg{}, true},
		{"paste start", tea.PasteStartMsg{}, true},
		{"paste end", tea.PasteEndMsg{}, true},
		{"window size", tea.WindowSizeMsg{Width: 80, Height: 24}, false},
		{"unrelated message", dialog.StageInvocationMsg{Text: "/commit "}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := consumesInput(tt.msg); got != tt.want {
				t.Errorf("consumesInput(%T) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

// TestPasteReachesArgumentDialog: bracketed paste is not a key press, so it used
// to fall through the argument dialog and land in the chat editor behind it —
// the pasted text appeared somewhere the user could not see while the modal held
// focus. It must reach the focused field instead.
func TestPasteReachesArgumentDialog(t *testing.T) {
	a := appModel{
		showMultiArgumentsDialog: true,
		multiArgumentsDialog: dialog.NewMultiArgumentsDialogCmp(dialog.ShowMultiArgumentsDialogMsg{
			CommandID: "project:scope",
			Content:   "Look at $TARGET inside $SCOPE.",
			ArgNames:  []string{"TARGET", "SCOPE"},
			Mode:      dialog.ArgsModePositional,
		}),
	}

	// Paste into the first field, tab, then paste into the second.
	model, _ := a.Update(tea.PasteMsg{Content: "HEAD~3"})
	a = model.(appModel)
	model, _ = a.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	a = model.(appModel)
	model, _ = a.Update(tea.PasteMsg{Content: "src/internal tools"})
	a = model.(appModel)

	// Submitting reports what the fields actually hold.
	_, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("submitting the dialog returned no cmd")
	}
	closeMsg, ok := cmd().(dialog.CloseMultiArgumentsDialogMsg)
	if !ok {
		t.Fatalf("got %T, want CloseMultiArgumentsDialogMsg", cmd())
	}
	if !closeMsg.Submit {
		t.Fatal("Submit = false")
	}
	if len(closeMsg.Values) != 2 || closeMsg.Values[0] != "HEAD~3" || closeMsg.Values[1] != "src/internal tools" {
		t.Fatalf("Values = %#v, want the pasted text in both fields", closeMsg.Values)
	}
}

// The complementary property — paste falling through to the editor when no
// modal is open — is not covered here: reaching that path in appModel.Update
// requires the full page/topbar/status graph, and the intercept is a single
// `if a.showMultiArgumentsDialog` guard inside the case.

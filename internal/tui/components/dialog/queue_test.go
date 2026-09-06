package dialog

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

func newQueueDialogForTest(t *testing.T, a *app.App) *queueDialogCmp {
	t.Helper()
	d := NewQueueDialogCmp(a).(*queueDialogCmp)
	d.SetSize(120, 40)
	d.SetSession("s1")
	return d
}

func TestQueueDialog_ListsQueuedMessagesInOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := app.NewForTest(ctx, nil)
	a.EnqueueForTest("s1", app.QueuedMessage{Text: "first message"})
	a.EnqueueForTest("s1", app.QueuedMessage{Text: "second message"})

	d := newQueueDialogForTest(t, a)
	view := ansi.Strip(d.View().Content)

	if !strings.Contains(view, "Queued messages (2)") {
		t.Errorf("header missing count:\n%s", view)
	}
	first := strings.Index(view, "first message")
	second := strings.Index(view, "second message")
	if first < 0 || second < 0 {
		t.Fatalf("previews missing:\n%s", view)
	}
	if first > second {
		t.Errorf("previews out of delivery order:\n%s", view)
	}
}

// A multi-line or escape-laden paste must stay a single bounded preview row.
func TestQueueDialog_PreviewIsSingleBoundedLine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := app.NewForTest(ctx, nil)
	a.EnqueueForTest("s1", app.QueuedMessage{
		Text: "line one\nline two\x1b[31m red \x1b[0m" + strings.Repeat("x", 500),
	})

	line := queueEntryLine(0, a.QueuedMessages("s1")[0], 60, styles.BaseStyle(), theme.CurrentTheme())

	if got := strings.Count(line, "\n"); got != 0 {
		t.Errorf("preview spans %d extra lines: %q", got, line)
	}
	if w := ansi.StringWidth(line); w > 60 {
		t.Errorf("preview width = %d, want <= 60", w)
	}
	if strings.Contains(ansi.Strip(line), "\x1b") {
		t.Errorf("escape sequences survived: %q", line)
	}
}

func TestQueueDialog_EmptyState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := newQueueDialogForTest(t, app.NewForTest(ctx, nil))
	view := ansi.Strip(d.View().Content)

	if !strings.Contains(view, "Queued messages (0)") || !strings.Contains(view, "Nothing queued") {
		t.Errorf("empty state not rendered:\n%s", view)
	}
}

func TestQueueDialog_CloseKeys(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := newQueueDialogForTest(t, app.NewForTest(ctx, nil))

	for _, k := range []string{"esc", "ctrl+g", "q"} {
		_, cmd := d.Update(keyPress(k))
		if cmd == nil {
			t.Fatalf("%s produced no cmd", k)
		}
		if _, ok := cmd().(CloseQueueDialogMsg); !ok {
			t.Errorf("%s did not close the dialog, got %T", k, cmd())
		}
	}
}

// ctrl+x is advertised in the dialog footer, and the dialog swallows key
// presses — so it must discard the queue itself.
func TestQueueDialog_DiscardKeyEmptiesQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := app.NewForTest(ctx, nil)
	a.EnqueueForTest("s1", app.QueuedMessage{Text: "doomed"})

	d := newQueueDialogForTest(t, a)
	_, cmd := d.Update(keyPress("ctrl+x"))

	if n := a.QueueLen("s1"); n != 0 {
		t.Errorf("QueueLen after ctrl+x = %d, want 0", n)
	}
	if cmd == nil {
		t.Fatal("ctrl+x produced no cmd")
	}
	if _, ok := cmd().(CloseQueueDialogMsg); !ok {
		t.Errorf("ctrl+x did not close the dialog, got %T", cmd())
	}
}

func keyPress(k string) tea.KeyPressMsg {
	switch k {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "q":
		return tea.KeyPressMsg{Code: 'q', Text: "q"}
	case "ctrl+g":
		return tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}
	case "ctrl+x":
		return tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}
	}
	panic("unhandled key " + k)
}

package chat

import (
	"context"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
)

// newMessagesForTest builds a messagesCmp with one rendered message so View
// takes the normal (non-empty) branch, sized like the real container.
func newMessagesForTest(t *testing.T, a *app.App, width int) *messagesCmp {
	t.Helper()
	vp := viewport.New()
	m := &messagesCmp{
		app:           a,
		cachedContent: make(map[string]cacheItem),
		taskMessages:  make(map[string][]message.Message),
		viewport:      vp,
		spinner:       spinner.New(),
		attachments:   viewport.New(),
		session:       session.Session{ID: "test-session"},
		messages: []message.Message{{
			ID:        "m1",
			SessionID: "test-session",
			Role:      message.User,
		}},
	}
	m.width = width
	m.height = 12
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(m.height - 2)
	return m
}

// The chat view must never render more rows than it was given: the container
// applies MaxHeight, so an extra row silently clips the bottom line. That is
// how the queue banner made the help bar vanish for the rest of the session.
func TestMessages_ViewHeightFitsRegardlessOfQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ag := &editorFakeAgent{busy: true}
	a := app.NewForTest(ctx, ag)

	m := newMessagesForTest(t, a, 80)

	if h := lipgloss.Height(m.View().Content); h != m.height {
		t.Fatalf("empty queue: view height = %d, want %d", h, m.height)
	}

	a.EnqueueForTest("test-session", app.QueuedMessage{Text: "queued one"})
	if h := lipgloss.Height(m.View().Content); h != m.height {
		t.Fatalf("non-empty queue: view height = %d, want %d", h, m.height)
	}
}

// The help bar is the affordance that teaches enter / \ / / / ! — it must be
// visible whenever nothing is queued, and come back after a drain.
func TestMessages_HelpVisibleWhenQueueEmpty(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ag := &editorFakeAgent{}
	a := app.NewForTest(ctx, ag)

	// 100 columns fits the full help line; narrower terminals truncate its tail.
	m := newMessagesForTest(t, a, 100)

	view := m.View().Content
	if !strings.Contains(view, "for shell") {
		t.Errorf("help bar missing while queue is empty:\n%s", view)
	}

	a.EnqueueForTest("test-session", app.QueuedMessage{Text: "queued one"})
	view = m.View().Content
	if !strings.Contains(view, "queued") {
		t.Errorf("queue banner missing while a message is queued:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+g") || !strings.Contains(view, "ctrl+x") {
		t.Errorf("queue banner must advertise ctrl+g / ctrl+x:\n%s", view)
	}

	a.DiscardQueue("test-session")
	view = m.View().Content
	if !strings.Contains(view, "for shell") {
		t.Errorf("help bar did not come back after the queue drained:\n%s", view)
	}
}

// A queued message with newlines must not grow the banner into a second row.
func TestMessages_FooterIsSingleLine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ag := &editorFakeAgent{busy: true}
	a := app.NewForTest(ctx, ag)

	m := newMessagesForTest(t, a, 80)
	for i := 0; i < 40; i++ {
		a.EnqueueForTest("test-session", app.QueuedMessage{Text: "line\nline\nline"})
	}

	if h := lipgloss.Height(m.footer()); h != 1 {
		t.Fatalf("footer height = %d, want 1", h)
	}
}

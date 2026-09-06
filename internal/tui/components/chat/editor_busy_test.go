package chat

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/opencode-ai/opencode/internal/app"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"

	"github.com/opencode-ai/opencode/internal/config"
)

// ---- fake agent for editor tests -------------------------------------------

type editorFakeAgent struct {
	busy    bool
	runErr  error
	runChan chan agentpkg.AgentEvent // if nil, returns a closed channel
}

func (a *editorFakeAgent) IsSessionBusy(_ string) bool { return a.busy }

func (a *editorFakeAgent) Run(_ context.Context, _ string, _ string, _ int, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	if a.runErr != nil {
		return nil, a.runErr
	}
	if a.runChan != nil {
		return a.runChan, nil
	}
	ch := make(chan agentpkg.AgentEvent)
	close(ch)
	return ch, nil
}

// Satisfy the rest of the interface:
func (a *editorFakeAgent) Subscribe(_ context.Context) <-chan pubsub.Event[agentpkg.AgentEvent] {
	ch := make(chan pubsub.Event[agentpkg.AgentEvent])
	close(ch)
	return ch
}
func (a *editorFakeAgent) AgentID() config.AgentName               { return "" }
func (a *editorFakeAgent) Model() models.Model                     { return models.Model{} }
func (a *editorFakeAgent) Tools() []tools.BaseTool                 { return nil }
func (a *editorFakeAgent) ResolvedTools() ([]tools.BaseTool, bool) { return nil, false }
func (a *editorFakeAgent) RunWith(_ context.Context, _ string, _ string, _ int, _ agentpkg.RunOptions, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	return nil, nil
}
func (a *editorFakeAgent) Cancel(_ string)              {}
func (a *editorFakeAgent) IsBusy() bool                 { return a.busy }
func (a *editorFakeAgent) TryLockSession(_ string) bool { return true }
func (a *editorFakeAgent) UnlockSession(_ string)       {}
func (a *editorFakeAgent) Update(_ config.AgentName, _ models.ModelID) (models.Model, error) {
	return models.Model{}, nil
}
func (a *editorFakeAgent) Summarize(_ context.Context, _ string) error               { return nil }
func (a *editorFakeAgent) SummarizeSync(_ context.Context, _ string) error           { return nil }
func (a *editorFakeAgent) GenerateRecap(_ context.Context, _ string) (string, error) { return "", nil }

// ---- helpers ----------------------------------------------------------------

// newEditorForTest constructs a minimal editorCmp backed by a stub App.
func newEditorForTest(ctx context.Context, ag *editorFakeAgent) (*editorCmp, *app.App) {
	a := app.NewForTest(ctx, ag)
	ed := &editorCmp{
		app:      a,
		session:  session.Session{ID: "test-session"},
		textarea: CreateTextArea(nil),
		mode:     modeNormal,
	}
	return ed, a
}

// cmdProducesMsg runs cmd (if non-nil) and returns the produced tea.Msg.
func cmdProducesMsg(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// ---- tests ------------------------------------------------------------------

// TestEditor_send_BusyEnqueuesNoWarnCmd asserts that send() while the session
// is busy enqueues the message, resets the textarea, and returns nil (no
// warning cmd).
func TestEditor_send_BusyEnqueuesNoWarnCmd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ag := &editorFakeAgent{busy: true}
	ed, a := newEditorForTest(ctx, ag)

	ed.textarea.SetValue("hello world")
	cmd := ed.send()
	// Must be nil — no warning toast for queued submissions.
	if cmd != nil {
		msg := cmdProducesMsg(cmd)
		t.Errorf("send() while busy returned non-nil cmd producing %T: %+v", msg, msg)
	}
	// Text must have moved into the queue.
	if n := a.QueueLen("test-session"); n != 1 {
		t.Errorf("QueueLen = %d, want 1", n)
	}
	// Textarea must be reset.
	if v := ed.textarea.Value(); v != "" {
		t.Errorf("textarea not reset, still has %q", v)
	}
	// Stop drain worker started by EnqueueMessage.
	a.ShutdownQueues()
}

// TestEditor_send_EmptyWhileBusyIsNoOp asserts that an empty submit while
// busy is silently discarded (no enqueue, no cmd).
func TestEditor_send_EmptyWhileBusyIsNoOp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ag := &editorFakeAgent{busy: true}
	ed, a := newEditorForTest(ctx, ag)

	ed.textarea.SetValue("")
	cmd := ed.send()

	if cmd != nil {
		t.Errorf("empty send() should return nil, got %T", cmd)
	}
	if n := a.QueueLen("test-session"); n != 0 {
		t.Errorf("empty submit should not enqueue, QueueLen = %d", n)
	}
}

// TestEditor_send_IdleDirectDispatch asserts that when the queue is empty AND
// the session is idle, send() dispatches directly (returns a SendMsg cmd)
// without calling EnqueueMessage.
func TestEditor_send_IdleDirectDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ag := &editorFakeAgent{busy: false}
	ed, a := newEditorForTest(ctx, ag)

	ed.textarea.SetValue("direct message")
	cmd := ed.send()

	// Must not enqueue.
	if n := a.QueueLen("test-session"); n != 0 {
		t.Errorf("idle send enqueued unexpectedly, QueueLen = %d", n)
	}
	// Must produce a SendMsg.
	if cmd == nil {
		t.Fatal("idle send returned nil cmd, expected SendMsg")
	}
	msg := cmdProducesMsg(cmd)
	sendMsg, ok := msg.(SendMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want SendMsg", msg)
	}
	if sendMsg.Text != "direct message" {
		t.Errorf("SendMsg.Text = %q, want %q", sendMsg.Text, "direct message")
	}
}

// TestEditor_send_QueueNonEmptySessionIdleEnqueues asserts FIFO routing:
// when the queue is non-empty but the session is momentarily idle, send()
// enqueues rather than dispatching directly.
func TestEditor_send_QueueNonEmptySessionIdleEnqueues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ag := &editorFakeAgent{busy: false} // observably idle
	ed, a := newEditorForTest(ctx, ag)

	// Pre-load the queue with an existing message (simulates a message
	// already enqueued while busy).
	a.EnqueueMessage("test-session", app.QueuedMessage{Text: "existing"})
	a.ShutdownQueues() // stop the worker so it doesn't race

	// Reset the queue state but keep the queued message.
	// We need to prevent the drain worker from interfering.
	// Reinitialize to have the message in queue with no active worker.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	a2 := app.NewForTest(ctx2, ag)
	a2.EnqueueMessage("test-session", app.QueuedMessage{Text: "existing"})
	a2.ShutdownQueues() // kill the worker immediately

	ed2 := &editorCmp{
		app:      a2,
		session:  session.Session{ID: "test-session"},
		textarea: CreateTextArea(nil),
		mode:     modeNormal,
	}

	// Queue has 1 message; session is idle. send() must enqueue, not dispatch.
	ed2.textarea.SetValue("new message")
	cmd := ed2.send()

	if cmd != nil {
		msg := cmdProducesMsg(cmd)
		if _, ok := msg.(SendMsg); ok {
			t.Error("send() dispatched directly despite non-empty queue (FIFO violation)")
		}
	}
	if n := a2.QueueLen("test-session"); n < 2 {
		t.Errorf("expected ≥2 queued messages (existing + new), got %d", n)
	}

	a2.ShutdownQueues()
	_ = ed
}

// TestEditor_send_IdlePathErrorSurfaces asserts that when the session is idle
// and agent.Run returns a non-ErrSessionBusy error, the send() function
// still returns a SendMsg cmd (which the chat page's sendMessage will dispatch;
// sendMessage itself surfaces the error). This test validates the editor's role
// which is only to route to direct dispatch — error surfacing happens upstream.
func TestEditor_send_IdlePathErrorSurfaces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ag := &editorFakeAgent{busy: false, runErr: errors.New("api error")}
	ed, _ := newEditorForTest(ctx, ag)

	ed.textarea.SetValue("direct with error")
	cmd := ed.send()

	// The editor must return a SendMsg cmd — error surfacing is chat page's job.
	if cmd == nil {
		t.Fatal("idle send with run error returned nil cmd")
	}
	msg := cmdProducesMsg(cmd)
	if _, ok := msg.(SendMsg); !ok {
		t.Errorf("idle path produced %T, want SendMsg", msg)
	}
}

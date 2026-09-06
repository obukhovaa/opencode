package page

import (
	"context"
	"sync"
	"testing"

	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/util"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
)

// ---- fake agent -------------------------------------------------------------

// busyFakeAgent returns runErr from every Run call. Only Run and IsSessionBusy
// are exercised; the remaining methods exist to satisfy agent.Service.
type busyFakeAgent struct {
	runErr error
}

func (a *busyFakeAgent) IsSessionBusy(_ string) bool { return false }

func (a *busyFakeAgent) Run(_ context.Context, _ string, _ string, _ int, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	if a.runErr != nil {
		return nil, a.runErr
	}
	ch := make(chan agentpkg.AgentEvent)
	close(ch)
	return ch, nil
}

func (a *busyFakeAgent) Subscribe(_ context.Context) <-chan pubsub.Event[agentpkg.AgentEvent] {
	ch := make(chan pubsub.Event[agentpkg.AgentEvent])
	close(ch)
	return ch
}
func (a *busyFakeAgent) AgentID() config.AgentName               { return "" }
func (a *busyFakeAgent) Model() models.Model                     { return models.Model{} }
func (a *busyFakeAgent) Tools() []tools.BaseTool                 { return nil }
func (a *busyFakeAgent) ResolvedTools() ([]tools.BaseTool, bool) { return nil, false }
func (a *busyFakeAgent) RunWith(_ context.Context, _ string, _ string, _ int, _ agentpkg.RunOptions, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	return nil, nil
}
func (a *busyFakeAgent) Cancel(_ string)              {}
func (a *busyFakeAgent) IsBusy() bool                 { return false }
func (a *busyFakeAgent) TryLockSession(_ string) bool { return true }
func (a *busyFakeAgent) UnlockSession(_ string)       {}
func (a *busyFakeAgent) Update(_ config.AgentName, _ models.ModelID) (models.Model, error) {
	return models.Model{}, nil
}
func (a *busyFakeAgent) Summarize(_ context.Context, _ string) error               { return nil }
func (a *busyFakeAgent) SummarizeSync(_ context.Context, _ string) error           { return nil }
func (a *busyFakeAgent) GenerateRecap(_ context.Context, _ string) (string, error) { return "", nil }

// ---- tests ------------------------------------------------------------------

// TestChatPage_sendMessage_BusyRaceEnqueues covers the direct-dispatch race:
// the editor routes to the queue only when it observes queue-empty AND
// not-busy, but another actor (drain worker, cron, flow step, bridge dispatch)
// can claim the session slot between that check and agent.Run. The resulting
// ErrSessionBusy must NOT be surfaced as an error toast (it is retryable) and
// must NOT be dropped — the textarea has already been reset, so dropping it
// silently loses the user's submission.
func TestChatPage_sendMessage_BusyRaceEnqueues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ag := &busyFakeAgent{runErr: agentpkg.ErrSessionBusy}
	a := app.NewForTest(ctx, ag)

	// Capture drain events synchronously: EnqueueMessage notifies with the
	// post-append length before returning, so this is race-free — unlike
	// polling QueueLen, which the drain worker mutates concurrently.
	var mu sync.Mutex
	var events []app.DrainEvent
	a.SetDrainNotifier(func(e app.DrainEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})

	p := &chatPage{app: a, session: session.Session{ID: "s1"}}

	cmd := p.sendMessage("redirect me", nil)
	a.ShutdownQueues()

	// No error toast for a retryable busy race.
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, isErr := msg.(util.InfoMsg); isErr {
				t.Errorf("sendMessage surfaced an info/error toast on ErrSessionBusy: %+v", msg)
			}
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatal("no DrainEvent emitted — the message was dropped instead of enqueued")
	}
	if events[0].SessionID != "s1" || events[0].QueueLen != 1 {
		t.Errorf("first DrainEvent = %+v, want {SessionID: s1, QueueLen: 1}", events[0])
	}
}

// TestChatPage_sendMessage_NonBusyErrorStillSurfaces guards the other half of
// the branch: a real failure must still reach the user rather than being
// swallowed into the queue.
func TestChatPage_sendMessage_NonBusyErrorStillSurfaces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ag := &busyFakeAgent{runErr: context.DeadlineExceeded}
	a := app.NewForTest(ctx, ag)
	p := &chatPage{app: a, session: session.Session{ID: "s1"}}

	cmd := p.sendMessage("hello", nil)
	if cmd == nil {
		t.Fatal("non-busy error produced no cmd — the failure was swallowed")
	}
	if n := a.QueueLen("s1"); n != 0 {
		t.Errorf("QueueLen = %d, want 0 (non-busy errors must not enqueue)", n)
	}
}

package service

import (
	"context"
	"testing"
	"time"

	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/message"
)

// deliveryStubAgent plays a run whose agent compacts after the turn: it
// sends the terminal event, then waits for the dispatcher's delivery signal
// before "compacting", recording what the chat had received by then. The
// channel stays open until the compaction is over, as the real agent's does.
type deliveryStubAgent struct {
	agentpkg.Service

	ad             *stubAdapter
	sendsAtCompact chan []string
}

func (a *deliveryStubAgent) Run(ctx context.Context, _, _ string, _ int, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	delivered := agentpkg.TurnDelivery(ctx)
	ch := make(chan agentpkg.AgentEvent, 1)
	go func() {
		defer close(ch)
		ch <- agentpkg.AgentEvent{
			Type:    agentpkg.AgentEventTypeResponse,
			Message: message.Message{Parts: []message.ContentPart{message.TextContent{Text: "the task is done"}}},
		}
		if delivered == nil {
			a.sendsAtCompact <- nil
			return
		}
		select {
		case <-delivered:
		case <-time.After(5 * time.Second):
		}
		var texts []string
		for _, s := range a.ad.Sends() {
			texts = append(texts, s.Text)
		}
		a.sendsAtCompact <- texts
	}()
	return ch, nil
}

// The turn's reply reaches the chat before a compaction the agent requested
// starts: the dispatcher marks the run's context for delivery and signals it
// only after handleTerminalEvent has posted the reply.
func TestHandleInbound_ReplyDeliveredBeforePostTurnCompaction(t *testing.T) {
	ag := &deliveryStubAgent{sendsAtCompact: make(chan []string, 1)}
	svc, ad := newDispatchTestSvc(t, ag)
	ag.ad = ad

	d := newBareDispatch(svc, "S1")
	d.handleInbound(context.Background(), testInbound("finish the task"))

	var got []string
	select {
	case got = <-ag.sendsAtCompact:
	case <-time.After(5 * time.Second):
		t.Fatal("the stub never reached its compaction")
	}
	if got == nil {
		t.Fatal("the run's context carries no delivery signal")
	}
	found := false
	for _, text := range got {
		if text == "the task is done" {
			found = true
		}
	}
	if !found {
		t.Errorf("chat sends when the compaction started = %q, want the turn's reply among them", got)
	}
}

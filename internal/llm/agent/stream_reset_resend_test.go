package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

// midStreamResetProvider streams a partial text delta and then ends in
// failWith for each request numbered in resets (1-based); every other request
// is the wrapped provider's normal response.
type midStreamResetProvider struct {
	*compactionProvider
	resets   map[int]error
	requests int
	// onReset runs each time a request is served a reset.
	onReset func()
}

func (p *midStreamResetProvider) StreamResponse(ctx context.Context, msgs []message.Message, ts []tools.BaseTool) <-chan provider.ProviderEvent {
	p.requests++
	err, reset := p.resets[p.requests]
	if !reset {
		p.fail = nil
		return p.compactionProvider.StreamResponse(ctx, msgs, ts)
	}
	p.fail = err
	if p.onReset != nil {
		p.onReset()
	}
	inner := p.compactionProvider.StreamResponse(ctx, msgs, ts)
	out := make(chan provider.ProviderEvent, 2)
	out <- provider.ProviderEvent{Type: provider.EventContentDelta, Content: "partial answer"}
	for ev := range inner {
		out <- ev
	}
	close(out)
	return out
}

func withNoResendDelay(t *testing.T) {
	t.Helper()
	prev := streamResetResendDelay
	streamResetResendDelay = 0
	t.Cleanup(func() { streamResetResendDelay = prev })
}

func newMidStreamResetAgent(t *testing.T, sessionID string, resets map[int]error) (*agent, *midStreamResetProvider, *[]string) {
	t.Helper()
	withFreshTaskRegistry(t)
	withNoResendDelay(t)
	a, main, _, log := newCompactingAgent(t, 100, nil)
	p := &midStreamResetProvider{compactionProvider: main, resets: resets}
	a.provider = p
	seedHistory(t, a, sessionID, textMsg(message.User, "earlier task"), textMsg(message.Assistant, "earlier answer"))
	seedReport(t, a, sessionID, 100)
	return a, p, log
}

func assistantTexts(t *testing.T, a *agent, sessionID string) []string {
	t.Helper()
	msgs, err := a.messages.List(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	var out []string
	for _, m := range msgs {
		if m.Role == message.Assistant {
			out = append(out, m.Content().String())
		}
	}
	return out
}

// A peer reset after partial output: the partial message is dropped and the
// same history goes out once more.
func TestStreamResetResend_ResendsTheTurnOnce(t *testing.T) {
	withAutoCompact(t, true)
	const sess = "s-reset-ok"
	a, p, log := newMidStreamResetAgent(t, sess, map[int]error{1: errRSTStream})

	res := a.processGeneration(context.Background(), sess, "next task", 0, nil, RunOptions{})

	if res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	if got := strings.Join(*log, ","); got != "main,main" {
		t.Fatalf("call order = %s, want main,main", got)
	}
	first, second := p.compactionProvider.requests[0], p.compactionProvider.requests[1]
	if len(first) != len(second) {
		t.Fatalf("the resend carried %d messages, the first call %d", len(second), len(first))
	}
	if last := second[len(second)-1]; last.Role != message.User || firstText(last) != "next task" {
		t.Errorf("the resend must end with the turn's user message, got %+v", last)
	}
	got := assistantTexts(t, a, sess)
	if len(got) != 2 || got[0] != "earlier answer" || strings.Contains(got[1], "partial answer") {
		t.Errorf("assistant messages = %q, want the partial one dropped", got)
	}
}

// The resend is bounded: a second reset fails the run with that error.
func TestStreamResetResend_OnlyOnce(t *testing.T) {
	withAutoCompact(t, true)
	const sess = "s-reset-twice"
	a, _, log := newMidStreamResetAgent(t, sess, map[int]error{1: errRSTStream, 2: errRSTStream})

	res := a.processGeneration(context.Background(), sess, "next task", 0, nil, RunOptions{})

	if res.Error == nil || !errors.Is(res.Error, errRSTStream) {
		t.Fatalf("error = %v, want the stream reset", res.Error)
	}
	if got := strings.Join(*log, ","); got != "main,main" {
		t.Fatalf("call order = %s, want main,main", got)
	}
}

// Errors other than a peer reset are not resent.
func TestStreamResetResend_OtherErrorsAreNotResent(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"internal server error", errInternal},
		{"429 after retries", err429Retried},
		{"locally generated stream error", errors.New("stream error: stream ID 3; INTERNAL_ERROR")},
		{"protocol error from peer", errors.New("stream error: stream ID 3; PROTOCOL_ERROR; received from peer")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withAutoCompact(t, true)
			const sess = "s-reset-other"
			a, _, log := newMidStreamResetAgent(t, sess, map[int]error{1: tt.err})

			res := a.processGeneration(context.Background(), sess, "next task", 0, nil, RunOptions{})

			if res.Error == nil || !errors.Is(res.Error, tt.err) {
				t.Fatalf("error = %v, want %v", res.Error, tt.err)
			}
			if got := strings.Join(*log, ","); got != "main" {
				t.Fatalf("call order = %s, want main", got)
			}
		})
	}
}

// Near the window a reset goes to the overflow recovery; when its retry is
// reset too, the turn is not sent a third time.
func TestStreamResetResend_NotAfterOverflowRecovery(t *testing.T) {
	withAutoCompact(t, true)
	withNoResendDelay(t)
	const sess = "s-reset-overflow"
	a, _, log := newOverflowAgent(t, sess, func([]message.Message) error { return errRSTStream })
	seedReport(t, a, sess, 920)

	res := a.processGeneration(context.Background(), sess, "next task", 0, nil, RunOptions{})

	if res.Error == nil || !errors.Is(res.Error, errRSTStream) {
		t.Fatalf("error = %v, want the stream reset", res.Error)
	}
	if got := strings.Join(*log, ","); got != "main,summarizer,main" {
		t.Fatalf("call order = %s, want main,summarizer,main", got)
	}
}

// A run cancelled while waiting to resend ends as cancelled, with no resend.
func TestStreamResetResend_CancelledDuringDelay(t *testing.T) {
	withAutoCompact(t, true)
	const sess = "s-reset-cancel"
	a, p, log := newMidStreamResetAgent(t, sess, map[int]error{1: errRSTStream})
	streamResetResendDelay = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.onReset = func() { time.AfterFunc(50*time.Millisecond, cancel) }

	res := a.processGeneration(ctx, sess, "next task", 0, nil, RunOptions{})

	if res.Error == nil || !errors.Is(res.Error, ErrRequestCancelled) {
		t.Fatalf("error = %v, want the run cancelled", res.Error)
	}
	if got := strings.Join(*log, ","); got != "main" {
		t.Fatalf("call order = %s, want main", got)
	}
}

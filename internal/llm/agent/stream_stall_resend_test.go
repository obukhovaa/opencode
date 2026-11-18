package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/inbox"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

func TestStreamStallResend_BoundedAcrossRecoveries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failures   map[int]error
		sends      int
		nearWindow bool
	}{
		{"stalls", map[int]error{1: provider.ErrStreamStalled, 2: provider.ErrStreamStalled, 3: provider.ErrStreamStalled}, 3, true},
		{"peer reset between stalls", map[int]error{1: provider.ErrStreamStalled, 2: errRSTStream, 3: provider.ErrStreamStalled, 4: provider.ErrStreamStalled}, 4, false},
		{"overflow recovery between stalls", map[int]error{1: provider.ErrStreamStalled, 2: errPromptTooLong, 3: provider.ErrStreamStalled, 4: provider.ErrStreamStalled}, 4, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withAutoCompact(t, true)
			const sess = "stall-cap"
			a, p, _ := newMidStreamResetAgent(t, sess, tc.failures)
			// Near-window heuristics must not turn exhausted stalls into another retry.
			if tc.nearWindow {
				seedReport(t, a, sess, 920)
			}
			res := a.processGeneration(context.Background(), sess, "next task", 0, nil, RunOptions{})
			if !errors.Is(res.Error, provider.ErrStreamStalled) {
				t.Fatalf("error = %v", res.Error)
			}
			if p.requests != tc.sends {
				t.Fatalf("sends = %d, want %d", p.requests, tc.sends)
			}
			if !strings.Contains(res.Error.Error(), "after 2 resends") {
				t.Fatalf("unclear exhaustion: %v", res.Error)
			}
		})
	}
}

func TestStreamStallResend_DrainsOnlyAfterStreamEnds(t *testing.T) {
	withAutoCompact(t, false)
	const sess = "stall-inbox"
	acked := useSessionInbox(t, sess)
	a, p, _ := newMidStreamResetAgent(t, sess, map[int]error{1: provider.ErrStreamStalled, 2: provider.ErrStreamStalled})
	p.onReset = func() {
		id := string(rune('a' + p.requests - 1))
		inbox.Default().Push(sess, nil, inbox.Item{ID: id, Text: "queued " + id})
		if len(acked()) != p.requests-1 {
			t.Fatal("input acknowledged while model call is still in flight")
		}
	}
	res := a.processGeneration(context.Background(), sess, "next task", 0, nil, RunOptions{})
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	for i, text := range []string{"queued a", "queued b"} {
		req := p.compactionProvider.requests[i+1]
		if got := tail(req, 1); !reflect.DeepEqual(got, []string{"user:" + text}) {
			t.Fatalf("resend tail %v", got)
		}
		for _, m := range req {
			if strings.Contains(m.Content().String(), "partial answer") {
				t.Fatal("partial output replayed")
			}
		}
	}
	if !reflect.DeepEqual(acked(), []string{"a", "b"}) {
		t.Fatalf("acks %v", acked())
	}
}

func TestInbox_FailedTurnRetainsInputForNextRun(t *testing.T) {
	withAutoCompact(t, false)
	const sess = "stall-next-run"
	acked := useSessionInbox(t, sess)
	a, p, _ := newMidStreamResetAgent(t, sess, map[int]error{1: provider.ErrStreamStalled, 2: provider.ErrStreamStalled, 3: provider.ErrStreamStalled})
	p.onReset = func() {
		if p.requests == 3 {
			inbox.Default().Push(sess, nil, inbox.Item{ID: "late", Text: "try something else"})
		}
	}
	res := a.processGeneration(context.Background(), sess, "next task", 0, nil, RunOptions{})
	if !errors.Is(res.Error, provider.ErrStreamStalled) || len(acked()) != 0 || inbox.Default().Len(sess) != 1 {
		t.Fatalf("error=%v acks=%v pending=%d", res.Error, acked(), inbox.Default().Len(sess))
	}
	res = a.processGeneration(context.Background(), sess, "retry", 0, nil, RunOptions{})
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if got := tail(p.compactionProvider.requests[3], 1); !reflect.DeepEqual(got, []string{"user:try something else"}) {
		t.Fatalf("first retry request: %v", got)
	}
	if !reflect.DeepEqual(acked(), []string{"late"}) {
		t.Fatalf("acks %v", acked())
	}
}

func TestInbox_ExistingSessionDeliversBeforeFirstCall(t *testing.T) {
	withAutoCompact(t, false)
	const sess = "inbox-before-run"
	acked := useSessionInbox(t, sess)
	a, p, _ := newMidStreamResetAgent(t, sess, nil)
	inbox.Default().Push(sess, nil, inbox.Item{ID: "pending", Text: "pending input"})
	events, runErr := a.Run(context.Background(), sess, "next task", 0)
	if runErr != nil {
		t.Fatal(runErr)
	}
	var res AgentEvent
	for ev := range events {
		res = ev
	}
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if got := tail(p.compactionProvider.requests[0], 1); !reflect.DeepEqual(got, []string{"user:pending input"}) {
		t.Fatalf("first request: %v", got)
	}
	if !reflect.DeepEqual(acked(), []string{"pending"}) {
		t.Fatalf("acks %v", acked())
	}
	// The delivered item is persisted, so a later run does not depend on the inbox.
	msgs, err := a.messages.List(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range msgs {
		if m.Role == message.User && m.Content().String() == "pending input" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("persisted input count %d", n)
	}
}

// Partial tool arguments are never executed. Only the final failed attempt
// remains in storage, paired with an error result for a later session reload.
type stalledToolProvider struct {
	*recordingProvider
}

func (p *stalledToolProvider) StreamResponse(ctx context.Context, msgs []message.Message, ts []tools.BaseTool) <-chan provider.ProviderEvent {
	p.mu.Lock()
	p.requests = append(p.requests, append([]message.Message(nil), msgs...))
	n := len(p.requests)
	p.mu.Unlock()
	out := make(chan provider.ProviderEvent, 2)
	out <- provider.ProviderEvent{Type: provider.EventToolUseStart, ToolCall: &message.ToolCall{ID: fmt.Sprintf("partial-%d", n), Name: "count", Input: `{"incomplete":`}}
	out <- provider.ProviderEvent{Type: provider.EventError, Error: provider.ErrStreamStalled}
	close(out)
	return out
}

func TestStreamStallResend_PairsFinalPartialTools(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, false)
	p := &stalledToolProvider{recordingProvider: &recordingProvider{scriptedProvider: &scriptedProvider{}}}
	ran := 0
	a := newInboxLoopAgent(t, p, func() { ran++ })
	const sess = "stalled-tools"
	res := a.processGeneration(context.Background(), sess, "start", 0, nil, RunOptions{})
	if !errors.Is(res.Error, provider.ErrStreamStalled) {
		t.Fatal(res.Error)
	}
	if ran != 0 {
		t.Fatalf("executed %d unfinished tool calls", ran)
	}
	if len(p.requests) != 3 {
		t.Fatalf("requests=%d", len(p.requests))
	}
	for _, req := range p.requests {
		for _, m := range req {
			if len(m.ToolCalls()) > 0 {
				t.Fatal("partial tool sent to model")
			}
		}
	}
	msgs, err := a.messages.List(context.Background(), sess)
	if err != nil {
		t.Fatal(err)
	}
	calls, results := 0, 0
	for _, m := range msgs {
		for _, tc := range m.ToolCalls() {
			calls++
			if tc.ID != "partial-3" {
				t.Fatalf("discarded tool remains: %s", tc.ID)
			}
		}
		for _, tr := range m.ToolResults() {
			results++
			if tr.ToolCallID != "partial-3" || !tr.IsError {
				t.Fatalf("bad result: %+v", tr)
			}
		}
	}
	if calls != 1 || results != 1 {
		t.Fatalf("stored calls/results = %d/%d", calls, results)
	}
}

func TestInbox_AutoResumeKeepsCompletionAcrossOverflow(t *testing.T) {
	withAutoCompact(t, true)
	const sess = "inbox-resume-overflow"
	useSessionInbox(t, sess)
	a, _, _ := newOverflowAgent(t, sess, func(msgs []message.Message) error {
		if headedBySummary(msgs) {
			return nil
		}
		return errPromptTooLong
	})
	seedHistory(t, a, sess, completionPair("background", "completed work")...)
	inbox.Default().Push(sess, nil, inbox.Item{ID: "pending", Text: "also consider this"})
	res := a.processGeneration(context.Background(), sess, "", 0, nil, RunOptions{})
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	p := a.provider.(*overflowingProvider)
	if len(p.requests) != 2 {
		t.Fatalf("requests=%d", len(p.requests))
	}
	req := p.requests[1]
	if !hasToolResult(req, "completed work") {
		t.Fatal("completion lost across recovery compaction")
	}
	if got := tail(req, 1); !reflect.DeepEqual(got, []string{"user:also consider this"}) {
		t.Fatalf("queued input lost: %v", got)
	}
}

func TestStreamStallResend_SuccessfulToolRoundGetsNewBudget(t *testing.T) {
	withAutoCompact(t, false)
	withFreshTaskRegistry(t)
	log := &[]string{}
	main := &compactionProvider{name: "main", window: 1000, log: log}
	p := &midStreamResetProvider{compactionProvider: main, resets: map[int]error{1: provider.ErrStreamStalled, 2: provider.ErrStreamStalled, 4: provider.ErrStreamStalled, 5: provider.ErrStreamStalled}}
	main.respond = func(_ []message.Message) *provider.ProviderResponse {
		if p.requests == 3 {
			return countToolTurn("done-tool")
		}
		return endTurn()
	}
	ran := 0
	a := newInboxLoopAgent(t, p, func() { ran++ })
	res := a.processGeneration(context.Background(), "stall-new-round", "start", 0, nil, RunOptions{})
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if p.requests != 6 || ran != 1 {
		t.Fatalf("requests=%d tools=%d, want 6/1", p.requests, ran)
	}
	for _, req := range main.requests[3:] {
		calls, results := 0, 0
		for _, m := range req {
			calls += len(m.ToolCalls())
			results += len(m.ToolResults())
		}
		if calls != 1 || results != 1 {
			t.Fatalf("resend lost completed tool pair: %d/%d", calls, results)
		}
	}
}

func TestStreamStallResend_WrapUpExhaustionFails(t *testing.T) {
	withAutoCompact(t, false)
	const sess = "stall-wrap-up"
	acked := useSessionInbox(t, sess)
	a, p, _ := newMidStreamResetAgent(t, sess, map[int]error{2: provider.ErrStreamStalled, 3: provider.ErrStreamStalled, 4: provider.ErrStreamStalled})
	p.respond = func(_ []message.Message) *provider.ProviderResponse {
		inbox.Default().Push(sess, nil, inbox.Item{ID: "continue", Text: "continue"})
		return endTurn()
	}
	p.onReset = func() {
		if p.requests == 2 {
			inbox.Default().Push(sess, nil, inbox.Item{ID: "steer", Text: "change course"})
		}
	}
	res := a.processGeneration(context.Background(), sess, "start", 1, nil, RunOptions{})
	if !errors.Is(res.Error, provider.ErrStreamStalled) || p.requests != 4 {
		t.Fatalf("error=%v requests=%d", res.Error, p.requests)
	}
	if got := tail(p.compactionProvider.requests[2], 1); !reflect.DeepEqual(got, []string{"user:change course"}) {
		t.Fatalf("stall resend missed input: %v", got)
	}
	if !reflect.DeepEqual(acked(), []string{"continue", "steer"}) {
		t.Fatalf("acks %v", acked())
	}
}

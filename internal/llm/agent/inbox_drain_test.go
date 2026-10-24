package agent

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/inbox"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// recordingProvider is a scriptedProvider that also keeps the history
// each request was sent with.
type recordingProvider struct {
	*scriptedProvider
	mu       sync.Mutex
	requests [][]message.Message
}

func (p *recordingProvider) StreamResponse(ctx context.Context, msgs []message.Message, ts []tools.BaseTool) <-chan provider.ProviderEvent {
	p.mu.Lock()
	p.requests = append(p.requests, append([]message.Message(nil), msgs...))
	p.mu.Unlock()
	return p.scriptedProvider.StreamResponse(ctx, msgs, ts)
}

func (p *recordingProvider) request(n int) []message.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[n-1]
}

// newInboxLoopAgent is newLoopAgent with an extra plain tool ("count")
// whose run hook lets a test push input while a tool call is in flight.
func newInboxLoopAgent(t *testing.T, p provider.Provider, onTool func()) *agent {
	t.Helper()
	if config.Get() == nil {
		if _, err := config.Load(t.TempDir(), false); err != nil {
			t.Fatalf("config.Load: %v", err)
		}
	}
	if onTool == nil {
		onTool = func() {}
	}
	toolsCh := make(chan tools.BaseTool, 2)
	toolsCh <- tools.NewStructOutputTool(map[string]any{
		"type":       "object",
		"properties": map[string]any{"status": map[string]any{"type": "string"}},
		"required":   []any{"status"},
	})
	toolsCh <- &countingTool{run: onTool}
	close(toolsCh)
	return &agent{
		Broker:   pubsub.NewBroker[AgentEvent](),
		sessions: &memSessions{},
		messages: newMemMessages(),
		agentID:  config.AgentName("coder"),
		toolsCh:  toolsCh,
		provider: p,
	}
}

func countToolTurn(id string) *provider.ProviderResponse {
	return &provider.ProviderResponse{
		ToolCalls:    []message.ToolCall{{ID: id, Name: "count", Input: `{}`, Finished: true}},
		FinishReason: message.FinishReasonToolUse,
	}
}

func structOutputTurnWith(id, status string) *provider.ProviderResponse {
	return &provider.ProviderResponse{
		ToolCalls: []message.ToolCall{{
			ID: id, Name: tools.StructOutputToolName,
			Input: `{"status":"` + status + `"}`, Finished: true,
		}},
		FinishReason: message.FinishReasonToolUse,
	}
}

// useSessionInbox registers a delivery hook for session on the shared
// inbox and returns a function reporting every id it acknowledged.
func useSessionInbox(t *testing.T, session string) func() []string {
	t.Helper()
	var mu sync.Mutex
	var acked []string
	inbox.Default().Push(session, func(_ string, items []inbox.Item) {
		mu.Lock()
		defer mu.Unlock()
		for _, it := range items {
			acked = append(acked, it.ID)
		}
	})
	t.Cleanup(func() { inbox.Default().Clear(session) })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), acked...)
	}
}

// tail renders the tail of a history as role:text, for assertions.
func tail(msgs []message.Message, n int) []string {
	if len(msgs) < n {
		n = len(msgs)
	}
	out := make([]string, 0, n)
	for _, m := range msgs[len(msgs)-n:] {
		out = append(out, string(m.Role)+":"+m.Content().String())
	}
	return out
}

// Two messages that arrive during a tool call reach the next request as
// consecutive user messages right after the tool result, and their ids
// are acknowledged in order.
func TestInbox_DrainedAfterToolResults(t *testing.T) {
	withFreshTaskRegistry(t)
	const sess = "sess-inbox-drain"
	acked := useSessionInbox(t, sess)

	p := &recordingProvider{scriptedProvider: &scriptedProvider{respond: func(call int, _ bool) *provider.ProviderResponse {
		if call == 1 {
			return countToolTurn("call-count-1")
		}
		return endTurn()
	}}}
	a := newInboxLoopAgent(t, p, func() {
		inbox.Default().Push(sess, nil,
			inbox.Item{ID: "a", Text: "Make my discounts steeper"},
			inbox.Item{ID: "b", Text: "except for Premium"})
	})

	res := a.processGeneration(context.Background(), sess, "start", 0, nil, RunOptions{NonInteractive: true})
	if res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	if got := p.callCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 (a tool-boundary drain adds no call)", got)
	}
	req := p.request(2)
	if req[len(req)-3].Role != message.Tool {
		t.Fatalf("message before the drained input is %s, want the tool result", req[len(req)-3].Role)
	}
	want := []string{"user:Make my discounts steeper", "user:except for Premium"}
	if got := tail(req, 2); !reflect.DeepEqual(got, want) {
		t.Fatalf("request 2 tail = %v, want %v", got, want)
	}
	if got := acked(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("acked = %v, want [a b]", got)
	}
}

// Input waiting when the model ends its turn with text keeps the run
// going for one more cycle.
func TestInbox_HoldsNaturalEndTurn(t *testing.T) {
	withFreshTaskRegistry(t)
	const sess = "sess-inbox-endturn"
	useSessionInbox(t, sess)

	p := &recordingProvider{scriptedProvider: &scriptedProvider{respond: func(int, bool) *provider.ProviderResponse {
		return endTurn()
	}}}
	p.onCall = func(call int) {
		if call == 1 {
			inbox.Default().Push(sess, nil, inbox.Item{ID: "a", Text: "one more thing"})
		}
	}
	a := newInboxLoopAgent(t, p, nil)

	res := a.processGeneration(context.Background(), sess, "start", 0, nil, RunOptions{NonInteractive: true})
	if res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	if got := p.callCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
	// The scripted provider streams no content deltas, so the stored
	// assistant message has no text; its role is what matters here.
	req := p.request(2)
	if req[len(req)-2].Role != message.Assistant {
		t.Fatalf("message before the drained input is %s, want the ended turn", req[len(req)-2].Role)
	}
	if got := tail(req, 1); got[0] != "user:one more thing" {
		t.Fatalf("request 2 tail = %v", got)
	}
}

// Input waiting after an accepted struct_output reopens the run. When the
// model then ends with text, the earlier struct_output is the output.
func TestInbox_ReopensAfterStructOutput_KeepsEarlierOutput(t *testing.T) {
	withFreshTaskRegistry(t)
	const sess = "sess-inbox-struct-keep"
	useSessionInbox(t, sess)

	p := &recordingProvider{scriptedProvider: &scriptedProvider{respond: func(call int, _ bool) *provider.ProviderResponse {
		if call == 1 {
			return structOutputTurnWith("call-struct-1", "first")
		}
		return endTurn()
	}}}
	p.onCall = func(call int) {
		if call == 1 {
			inbox.Default().Push(sess, nil, inbox.Item{ID: "a", Text: "wait, one change"})
		}
	}
	a := newInboxLoopAgent(t, p, nil)

	res := a.processGeneration(context.Background(), sess, "start", 0, nil, RunOptions{NonInteractive: true})
	if res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	if got := p.callCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
	if got := tail(p.request(2), 1); got[0] != "user:wait, one change" {
		t.Fatalf("request 2 tail = %v", got)
	}
	if res.StructOutput == nil || !strings.Contains(res.StructOutput.Content, "first") {
		t.Fatalf("StructOutput = %+v, want the first output", res.StructOutput)
	}
}

// After a reopen, a plain tool call does not finish the run early; a new
// struct_output does, and replaces the earlier one.
func TestInbox_ReopenedRunFinishesOnNewStructOutput(t *testing.T) {
	withFreshTaskRegistry(t)
	const sess = "sess-inbox-struct-new"
	useSessionInbox(t, sess)

	p := &recordingProvider{scriptedProvider: &scriptedProvider{respond: func(call int, _ bool) *provider.ProviderResponse {
		switch call {
		case 1:
			return structOutputTurnWith("call-struct-1", "first")
		case 2:
			return countToolTurn("call-count-2")
		case 3:
			return structOutputTurnWith("call-struct-3", "second")
		}
		return endTurn()
	}}}
	p.onCall = func(call int) {
		if call == 1 {
			inbox.Default().Push(sess, nil, inbox.Item{ID: "a", Text: "steeper please"})
		}
	}
	a := newInboxLoopAgent(t, p, nil)

	res := a.processGeneration(context.Background(), sess, "start", 0, nil, RunOptions{NonInteractive: true})
	if res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	if got := p.callCount(); got != 3 {
		t.Fatalf("provider calls = %d, want 3 (finish on the new struct_output, no wrap-up)", got)
	}
	if res.StructOutput == nil || !strings.Contains(res.StructOutput.Content, "second") {
		t.Fatalf("StructOutput = %+v, want the second output", res.StructOutput)
	}
}

// A reviewer who keeps sending cannot keep a run alive past max_turns: the
// hold cycles count, and the forced wrap-up turn does not drain, so input
// that reaches it stays queued and unacknowledged.
func TestInbox_CannotExtendPastMaxTurns(t *testing.T) {
	withFreshTaskRegistry(t)
	const sess = "sess-inbox-maxturns"
	acked := useSessionInbox(t, sess)
	const maxTurns = 3

	p := &recordingProvider{scriptedProvider: &scriptedProvider{respond: func(_ int, forced bool) *provider.ProviderResponse {
		if forced {
			return structOutputTurnWith("call-struct-final", "wrapped")
		}
		return endTurn()
	}}}
	var pushed []string
	p.onCall = func(call int) {
		id := string(rune('a' + call - 1))
		pushed = append(pushed, id)
		inbox.Default().Push(sess, nil, inbox.Item{ID: id, Text: "msg " + id})
	}
	a := newInboxLoopAgent(t, p, nil)

	res := a.processGeneration(context.Background(), sess, "start", maxTurns, nil, RunOptions{NonInteractive: true})
	if res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	if !res.TurnsExhausted {
		t.Fatal("TurnsExhausted = false, want the run to end on the turn budget")
	}
	if got := p.callCount(); got != maxTurns+1 {
		t.Fatalf("provider calls = %d, want %d (max_turns plus the wrap-up)", got, maxTurns+1)
	}
	last := pushed[len(pushed)-1]
	for _, m := range p.request(maxTurns + 1) {
		if m.Role == message.User && m.Content().String() == "msg "+last {
			t.Fatalf("wrap-up turn drained %q", last)
		}
	}
	if got := acked(); len(got) != maxTurns || contains(got, last) {
		t.Fatalf("acked = %v, want the first %d ids and not %q", got, maxTurns, last)
	}
	if n := inbox.Default().Len(sess); n != 1 {
		t.Fatalf("inbox holds %d items after the run, want the one that reached the wrap-up", n)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

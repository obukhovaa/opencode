package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/question"
)

// storeMessageSvc is a message.Service backed by an in-memory slice: Get
// and ListLatest read it, SubscribeParts comes from stubMessageSvc.
type storeMessageSvc struct {
	stubMessageSvc

	mu   sync.Mutex
	msgs []message.Message
	gets int
}

func (s *storeMessageSvc) add(m message.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, m)
}

func (s *storeMessageSvc) Get(_ context.Context, id string) (message.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	for _, m := range s.msgs {
		if m.ID == id {
			return m, nil
		}
	}
	return message.Message{}, errors.New("not found")
}

func (s *storeMessageSvc) ListLatest(_ context.Context, sessionID string, limit int64) ([]message.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []message.Message
	for _, m := range s.msgs {
		if m.SessionID == sessionID {
			out = append(out, m)
		}
	}
	if int64(len(out)) > limit {
		out = out[int64(len(out))-limit:]
	}
	return out, nil
}

func (s *storeMessageSvc) Gets() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets
}

// newIntermediateTestSvc wires the progress fixture with a message store
// and a dispatcher for S1 whose run is in flight (text guard set).
func newIntermediateTestSvc(t *testing.T, cfg *bridge.Config) (*Service, *sessionDispatch, *storeMessageSvc, *editorStubAdapter) {
	t.Helper()
	svc, ed, _ := newProgressTestSvc(t, cfg)
	msgs := &storeMessageSvc{}
	svc.app = &app.App{Messages: msgs}
	d := newBareDispatch(svc, "S1")
	d.textGuard.Store(newRunTextGuard())
	return svc, d, msgs, ed
}

func toolUseMessage(id, text string, calls ...message.ToolCall) message.Message {
	parts := []message.ContentPart{}
	if text != "" {
		parts = append(parts, message.TextContent{Text: text})
	}
	for _, c := range calls {
		parts = append(parts, c)
	}
	parts = append(parts, message.Finish{Reason: message.FinishReasonToolUse})
	return message.Message{ID: id, Role: message.Assistant, SessionID: "S1", Parts: parts}
}

func finishedCall(id, name string) message.ToolCall {
	return message.ToolCall{ID: id, Name: name, Input: "{}", Finished: true}
}

func callPart(sessionID, messageID string, call message.ToolCall) pubsub.Event[message.PartEvent] {
	return pubsub.Event[message.PartEvent]{
		Type:    pubsub.CreatedEvent,
		Payload: message.PartEvent{SessionID: sessionID, MessageID: messageID, Part: call},
	}
}

func sendTexts(sends []bridge.Outbound) []string {
	out := make([]string, 0, len(sends))
	for _, s := range sends {
		out = append(out, s.Text)
	}
	return out
}

func countTextPosts(sends []bridge.Outbound, prefix string) int {
	n := 0
	for _, s := range sends {
		if strings.HasPrefix(s.Text, prefix) {
			n++
		}
	}
	return n
}

func TestIntermediateText_PostsHeaderAndText(t *testing.T) {
	tests := []struct {
		name       string
		calls      []message.ToolCall
		wantHeader string
	}{
		{
			name:       "single call",
			calls:      []message.ToolCall{finishedCall("toolu_01aaaaaa", "bash")},
			wantHeader: "⌛ bash",
		},
		{
			name: "calls in order",
			calls: []message.ToolCall{
				finishedCall("toolu_01aaaaaa", "bash"),
				finishedCall("toolu_02bbbbbb", "question"),
			},
			wantHeader: "⌛ bash, question",
		},
		{
			name: "duplicate names kept",
			calls: []message.ToolCall{
				finishedCall("toolu_01aaaaaa", "bash"),
				finishedCall("toolu_02bbbbbb", "bash"),
			},
			wantHeader: "⌛ bash, bash",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, d, msgs, ed := newIntermediateTestSvc(t, &bridge.Config{ToolUpdatesEnabled: false})
			msgs.add(toolUseMessage("M1", "Let me check the logs.", tt.calls...))

			for _, c := range tt.calls {
				d.handlePartEvent(callPart("S1", "M1", c))
			}

			sends := ed.Sends()
			if len(sends) != 1 {
				t.Fatalf("sends = %q; want exactly one", sendTexts(sends))
			}
			if want := tt.wantHeader + "\nLet me check the logs."; sends[0].Text != want {
				t.Errorf("text = %q; want %q", sends[0].Text, want)
			}
			if got := msgs.Gets(); got != 1 {
				t.Errorf("store reads = %d; want 1 (later calls of the message are deduped)", got)
			}
		})
	}
}

func TestIntermediateText_NoTextPostsNothing(t *testing.T) {
	_, d, msgs, ed := newIntermediateTestSvc(t, &bridge.Config{ToolUpdatesEnabled: false})
	call := finishedCall("toolu_01aaaaaa", "bash")
	msgs.add(toolUseMessage("M1", "", call))

	d.handlePartEvent(callPart("S1", "M1", call))

	if sends := ed.Sends(); len(sends) != 0 {
		t.Errorf("sends = %q; want none", sendTexts(sends))
	}
	if d.textGuard.Load().has("M1") {
		t.Error("a message without text must not be claimed")
	}
}

func TestIntermediateText_FinalReplyPostedOnceWithoutHeader(t *testing.T) {
	_, d, _, ed := newIntermediateTestSvc(t, &bridge.Config{ToolUpdatesEnabled: true})
	final := message.Message{
		ID: "M2", Role: message.Assistant, SessionID: "S1",
		Parts: []message.ContentPart{
			message.TextContent{Text: "All done."},
			message.Finish{Reason: message.FinishReasonEndTurn},
		},
	}

	d.handleTerminalEvent(context.Background(), agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeResponse, Message: final})
	d.handleTerminalEvent(context.Background(), agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeResponse, Message: final})

	sends := ed.Sends()
	if len(sends) != 1 || sends[0].Text != "All done." {
		t.Errorf("sends = %q; want exactly [\"All done.\"]", sendTexts(sends))
	}
}

func TestIntermediateText_DedupWithTerminal(t *testing.T) {
	call := finishedCall("toolu_01aaaaaa", "bash")
	msg := toolUseMessage("M1", "Out of turns, here is where I got.", call)

	t.Run("intermediate first", func(t *testing.T) {
		_, d, msgs, ed := newIntermediateTestSvc(t, &bridge.Config{})
		msgs.add(msg)
		d.handlePartEvent(callPart("S1", "M1", call))
		d.handleTerminalEvent(context.Background(), agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeResponse, Message: msg})

		sends := ed.Sends()
		if len(sends) != 1 || !strings.HasPrefix(sends[0].Text, "⌛ bash\n") {
			t.Errorf("sends = %q; want one header post", sendTexts(sends))
		}
	})
	t.Run("terminal first", func(t *testing.T) {
		_, d, msgs, ed := newIntermediateTestSvc(t, &bridge.Config{})
		msgs.add(msg)
		d.handleTerminalEvent(context.Background(), agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeResponse, Message: msg})
		d.handlePartEvent(callPart("S1", "M1", call))

		sends := ed.Sends()
		if len(sends) != 1 || sends[0].Text != "Out of turns, here is where I got." {
			t.Errorf("sends = %q; want the terminal post only", sendTexts(sends))
		}
		if got := msgs.Gets(); got != 0 {
			t.Errorf("store reads = %d; want 0 once the message is claimed", got)
		}
	})
}

func TestIntermediateText_PrecedesFullCallCard(t *testing.T) {
	_, d, msgs, ed := newIntermediateTestSvc(t, &bridge.Config{ToolUpdatesEnabled: true, ToolUpdateVerbosity: "full"})
	call := message.ToolCall{ID: "toolu_01aaaaaa", Name: "bash", Input: `{"command":"ls"}`, Finished: true}
	msgs.add(toolUseMessage("M1", "Listing files.", call))

	d.handlePartEvent(callPart("S1", "M1", call))
	waitFor(t, "text and call card", func() bool { return len(ed.Sends()) == 2 })

	sends := ed.Sends()
	if sends[0].Text != "⌛ bash\nListing files." {
		t.Errorf("first send = %q; want the intermediate text", sends[0].Text)
	}
	if sends[1].Render == nil || !strings.HasPrefix(sends[1].Text, "🔧 bash") {
		t.Errorf("second send = %q; want the 🔧 call card", sends[1].Text)
	}
}

func TestIntermediateText_PostedAtEveryVerbosity(t *testing.T) {
	tests := []struct {
		name string
		cfg  *bridge.Config
	}{
		{"compact", &bridge.Config{ToolUpdatesEnabled: true}},
		{"full", &bridge.Config{ToolUpdatesEnabled: true, ToolUpdateVerbosity: "full"}},
		{"tool updates off", &bridge.Config{ToolUpdatesEnabled: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, d, msgs, ed := newIntermediateTestSvc(t, tt.cfg)
			call := finishedCall("toolu_01aaaaaa", "read")
			msgs.add(toolUseMessage("M1", "Reading config.", call))

			d.handlePartEvent(callPart("S1", "M1", call))

			if n := countTextPosts(ed.Sends(), "⌛ read\nReading config."); n != 1 {
				t.Errorf("intermediate posts = %d; want 1 (sends %q)", n, sendTexts(ed.Sends()))
			}
		})
	}
}

func TestIntermediateText_SkippedParts(t *testing.T) {
	call := finishedCall("toolu_01aaaaaa", "bash")
	tests := []struct {
		name string
		ev   pubsub.Event[message.PartEvent]
	}{
		{"subagent session", callPart("S2", "M1", call)},
		{"synthetic", func() pubsub.Event[message.PartEvent] {
			ev := callPart("S1", "M1", call)
			ev.Payload.Synthetic = true
			return ev
		}()},
		{"streaming stop without input", callPart("S1", "M1", message.ToolCall{ID: call.ID, Name: "bash", Finished: true})},
		{"streaming start", callPart("S1", "M1", message.ToolCall{ID: call.ID, Name: "bash"})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, d, msgs, ed := newIntermediateTestSvc(t, &bridge.Config{})
			msgs.add(toolUseMessage("M1", "Some text.", call))

			d.handlePartEvent(tt.ev)

			if sends := ed.Sends(); len(sends) != 0 {
				t.Errorf("sends = %q; want none", sendTexts(sends))
			}
			if got := msgs.Gets(); got != 0 {
				t.Errorf("store reads = %d; want 0", got)
			}
		})
	}
}

func TestIntermediateText_NoRunInFlight(t *testing.T) {
	_, d, msgs, ed := newIntermediateTestSvc(t, &bridge.Config{})
	d.textGuard.Store(nil)
	call := finishedCall("toolu_01aaaaaa", "bash")
	msgs.add(toolUseMessage("M1", "Some text.", call))

	d.handlePartEvent(callPart("S1", "M1", call))

	if sends := ed.Sends(); len(sends) != 0 {
		t.Errorf("sends = %q; want none", sendTexts(sends))
	}
	if got := msgs.Gets(); got != 0 {
		t.Errorf("store reads = %d; want 0", got)
	}
}

func TestIntermediateText_FlushedBeforeQuestion(t *testing.T) {
	svc, d, msgs, ed := newIntermediateTestSvc(t, &bridge.Config{})
	svc.dispatchMu.Lock()
	svc.dispatchers["S1"] = d
	svc.dispatchMu.Unlock()
	call := finishedCall("toolu_01aaaaaa", "question")
	msgs.add(toolUseMessage("M1", "Before I go on, one question.", call))

	router := &QuestionRouter{svc: svc, pending: map[string]*pendingQuestion{}}
	router.handleNewRequest(context.Background(), question.Request{
		ID:        "q1",
		SessionID: "S1",
		Questions: []question.Prompt{{Question: "ship?"}},
	})
	// The parts path arrives later and must not post the text again.
	d.handlePartEvent(callPart("S1", "M1", call))

	sends := ed.Sends()
	if len(sends) != 2 {
		t.Fatalf("sends = %q; want text then question", sendTexts(sends))
	}
	if sends[0].Text != "⌛ question\nBefore I go on, one question." {
		t.Errorf("first send = %q; want the pre-question text", sends[0].Text)
	}
	if !strings.Contains(sends[1].Text, "ship?") {
		t.Errorf("second send = %q; want the question prompt", sends[1].Text)
	}
}

func TestFlushIntermediateText_WaitsForInFlightPost(t *testing.T) {
	svc, d, msgs, _ := newIntermediateTestSvc(t, &bridge.Config{})
	svc.dispatchMu.Lock()
	svc.dispatchers["S1"] = d
	svc.dispatchMu.Unlock()
	msgs.add(toolUseMessage("M1", "Some text.", finishedCall("toolu_01aaaaaa", "question")))

	c, won := d.textGuard.Load().claim("M1")
	if !won {
		t.Fatal("claim not won on a fresh guard")
	}
	const hold = 50 * time.Millisecond
	go func() {
		time.Sleep(hold)
		close(c.done)
	}()

	start := time.Now()
	svc.flushIntermediateText(context.Background(), "S1")
	if elapsed := time.Since(start); elapsed < hold || elapsed >= intermediateFlushWait {
		t.Errorf("flush returned after %v; want it to wait for the in-flight post (~%v)", elapsed, hold)
	}
}

func TestFlushIntermediateText_NoDispatcherOrRun(t *testing.T) {
	svc, d, msgs, ed := newIntermediateTestSvc(t, &bridge.Config{})
	msgs.add(toolUseMessage("M1", "Some text.", finishedCall("toolu_01aaaaaa", "question")))

	// No dispatcher registered: nothing is created, nothing is posted.
	svc.flushIntermediateText(context.Background(), "S1")
	svc.dispatchMu.Lock()
	_, created := svc.dispatchers["S1"]
	svc.dispatchMu.Unlock()
	if created {
		t.Error("flush created a dispatcher")
	}

	// Dispatcher without a run in flight.
	d.textGuard.Store(nil)
	svc.dispatchMu.Lock()
	svc.dispatchers["S1"] = d
	svc.dispatchMu.Unlock()
	svc.flushIntermediateText(context.Background(), "S1")

	if sends := ed.Sends(); len(sends) != 0 {
		t.Errorf("sends = %q; want none", sendTexts(sends))
	}
}

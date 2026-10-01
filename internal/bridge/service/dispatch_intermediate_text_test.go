package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
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
	d.textGuard.Store(newRunTextGuard(0))
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
				d.handlePartEvent(callPart("S1", "M1", c), d.textGuard.Load())
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
	first := finishedCall("toolu_01aaaaaa", "bash")
	second := finishedCall("toolu_02bbbbbb", "read")
	msgs.add(toolUseMessage("M1", "", first, second))

	d.handlePartEvent(callPart("S1", "M1", first), d.textGuard.Load())
	d.handlePartEvent(callPart("S1", "M1", second), d.textGuard.Load())

	if sends := ed.Sends(); len(sends) != 0 {
		t.Errorf("sends = %q; want none", sendTexts(sends))
	}
	if got := msgs.Gets(); got != 1 {
		t.Errorf("store reads = %d; want 1 (a text-less message is claimed after the first read)", got)
	}
	c, ok := d.textGuard.Load().lookup("M1")
	if !ok {
		t.Fatal("a text-less tool_use message must be claimed")
	}
	select {
	case <-c.done:
	default:
		t.Error("the claim of a text-less message must be released")
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
		d.handlePartEvent(callPart("S1", "M1", call), d.textGuard.Load())
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
		d.handlePartEvent(callPart("S1", "M1", call), d.textGuard.Load())

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

	d.handlePartEvent(callPart("S1", "M1", call), d.textGuard.Load())
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

			d.handlePartEvent(callPart("S1", "M1", call), d.textGuard.Load())

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

			d.handlePartEvent(tt.ev, d.textGuard.Load())

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

	d.handlePartEvent(callPart("S1", "M1", call), d.textGuard.Load())

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
	d.handlePartEvent(callPart("S1", "M1", call), d.textGuard.Load())

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

// slowAdapter delays every Send by delay unless the send's context ends
// first, in which case nothing is delivered — what a slow or hung chat
// API looks like to a caller that bounds the send.
type slowAdapter struct {
	*stubAdapter
	delay time.Duration
}

func (a *slowAdapter) Send(ctx context.Context, out bridge.Outbound) bridge.SendResult {
	select {
	case <-time.After(a.delay):
		return a.stubAdapter.Send(ctx, out)
	case <-ctx.Done():
		return bridge.SendResult{Err: ctx.Err()}
	}
}

// useSlowSlack replaces the fixture's Slack adapter with a slowAdapter.
func useSlowSlack(svc *Service, delay time.Duration) *slowAdapter {
	slow := &slowAdapter{stubAdapter: newStubAdapter("slack", "default"), delay: delay}
	svc.mu.Lock()
	svc.adapters[adapterKey("slack", "default")] = slow
	svc.mu.Unlock()
	return slow
}

// liveMessageSvc adds a parts broker to storeMessageSvc, so a stub agent
// can store a message and republish its tool calls the way processEvent
// does at EventComplete.
type liveMessageSvc struct {
	storeMessageSvc
	broker *pubsub.Broker[message.PartEvent]
}

func newLiveMessageSvc() *liveMessageSvc {
	return &liveMessageSvc{broker: pubsub.NewBroker[message.PartEvent]()}
}

func (s *liveMessageSvc) SubscribeParts(ctx context.Context) <-chan pubsub.Event[message.PartEvent] {
	return s.broker.Subscribe(ctx)
}

func (s *liveMessageSvc) completeToolUse(msg message.Message) {
	s.add(msg)
	for _, c := range msg.ToolCalls() {
		s.broker.Publish(pubsub.UpdatedEvent, message.PartEvent{
			SessionID: msg.SessionID,
			MessageID: msg.ID,
			Part:      c,
			Time:      time.Now().UnixMilli(),
		})
	}
}

// scriptedAgent is an agent.Service whose Run hands each attempt (1-based)
// to script.
type scriptedAgent struct {
	agentpkg.Service

	attempts atomic.Int32
	script   func(attempt int) (<-chan agentpkg.AgentEvent, error)
}

func (a *scriptedAgent) Run(_ context.Context, _, _ string, _ int, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	return a.script(int(a.attempts.Add(1)))
}

func finalReply(id, text string) agentpkg.AgentEvent {
	return agentpkg.AgentEvent{
		Type: agentpkg.AgentEventTypeResponse,
		Message: message.Message{
			ID: id, Role: message.Assistant, SessionID: "S1",
			Parts: []message.ContentPart{
				message.TextContent{Text: text},
				message.Finish{Reason: message.FinishReasonEndTurn},
			},
		},
	}
}

// TestHandleInbound_RelaysIntermediateText drives a whole bridge run: the
// agent stores a tool_use message with text and republishes its call, and
// the text must reach chat under its header. otherActorFirst adds a busy
// first attempt during which another actor's run on the session completes
// a tool_use message of its own; the parts subscription is already open,
// so that part is buffered, but it is not this run's and is not relayed.
func TestHandleInbound_RelaysIntermediateText(t *testing.T) {
	tests := []struct {
		name            string
		otherActorFirst bool
	}{
		{"own run", false},
		{"another actor held the session first", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, ed, _ := newProgressTestSvc(t, &bridge.Config{})
			msgs := newLiveMessageSvc()
			ag := &scriptedAgent{script: func(attempt int) (<-chan agentpkg.AgentEvent, error) {
				if tt.otherActorFirst && attempt == 1 {
					msgs.completeToolUse(toolUseMessage("M0", "Another run's text.", finishedCall("toolu_00zzzzzz", "bash")))
					return nil, agentpkg.ErrSessionBusy
				}
				msgs.completeToolUse(toolUseMessage("M1", "Checking the logs.", finishedCall("toolu_01aaaaaa", "bash")))
				ch := make(chan agentpkg.AgentEvent, 1)
				ch <- finalReply("M2", "Done.")
				close(ch)
				return ch, nil
			}}
			svc.app = &app.App{
				Messages:         msgs,
				PrimaryAgents:    map[config.AgentName]agentpkg.Service{config.AgentCoder: ag},
				PrimaryAgentKeys: []config.AgentName{config.AgentCoder},
			}
			d := newBareDispatch(svc, "S1")
			ctx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			wg.Add(1)
			go func() { defer wg.Done(); d.runParts(ctx) }()
			defer func() { cancel(); wg.Wait() }()

			d.handleInbound(context.Background(), testInbound("look at the logs"))

			waitFor(t, "the intermediate text", func() bool {
				return countTextPosts(ed.Sends(), "⌛ bash\nChecking the logs.") == 1
			})
			// M0's part was forwarded ahead of M1's, so it has been handled.
			if n := countTextPosts(ed.Sends(), "⌛ bash\nAnother run's text."); n != 0 {
				t.Errorf("another actor's text posted %d times; want 0 (sends %q)", n, sendTexts(ed.Sends()))
			}
			if n := countTextPosts(ed.Sends(), "Done."); n != 1 {
				t.Errorf("final reply posted %d times; want 1 (sends %q)", n, sendTexts(ed.Sends()))
			}
		})
	}
}

// TestHandleInbound_RunEndingOnToolUsePostsTextOnce drives a whole bridge
// run that ends on its own tool_use message (its turn limit, say). The
// parts path relays the message under its header, then the terminal event
// carries the same message: the terminal path must find the run's guard
// and not post the text again without a header.
func TestHandleInbound_RunEndingOnToolUsePostsTextOnce(t *testing.T) {
	const text = "Out of turns, here is where I got."
	svc, ed, _ := newProgressTestSvc(t, &bridge.Config{})
	msgs := newLiveMessageSvc()
	m1 := toolUseMessage("M1", text, finishedCall("toolu_01aaaaaa", "bash"))
	ag := &scriptedAgent{script: func(int) (<-chan agentpkg.AgentEvent, error) {
		msgs.completeToolUse(m1)
		ch := make(chan agentpkg.AgentEvent)
		go func() {
			defer close(ch)
			// Hold the terminal event until the intermediate post is out,
			// so the terminal path is the one that must skip the message.
			deadline := time.Now().Add(3 * time.Second)
			for countTextPosts(ed.Sends(), "⌛ bash\n"+text) == 0 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			ch <- agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeResponse, Message: m1}
		}()
		return ch, nil
	}}
	svc.app = &app.App{
		Messages:         msgs,
		PrimaryAgents:    map[config.AgentName]agentpkg.Service{config.AgentCoder: ag},
		PrimaryAgentKeys: []config.AgentName{config.AgentCoder},
	}
	d := newBareDispatch(svc, "S1")
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); d.runParts(ctx) }()
	defer func() { cancel(); wg.Wait() }()

	d.handleInbound(context.Background(), testInbound("look at the logs"))

	var posts []string
	for _, s := range ed.Sends() {
		if strings.Contains(s.Text, text) {
			posts = append(posts, s.Text)
		}
	}
	if len(posts) != 1 || posts[0] != "⌛ bash\n"+text {
		t.Errorf("posts of the text = %q; want exactly [%q]", posts, "⌛ bash\n"+text)
	}
}

// TestIntermediateText_LatePartKeepsItsRunsGuard: d.parts is shared by
// the session's runs and runParts can lag, so a part of run N can be
// handled after run N ended. It must be checked against run N's guard:
// against run N+1's it would post run N's message a second time, against
// none (no run in flight) its message would be lost.
func TestIntermediateText_LatePartKeepsItsRunsGuard(t *testing.T) {
	tests := []struct {
		name string
		next *runTextGuard
	}{
		{"next run started", newRunTextGuard(0)},
		{"no run in flight", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, d, msgs, ed := newIntermediateTestSvc(t, &bridge.Config{})
			first := finishedCall("toolu_01aaaaaa", "bash")
			second := finishedCall("toolu_02bbbbbb", "read")
			msgs.add(toolUseMessage("M1", "Checking two things.", first, second))
			last := finishedCall("toolu_03cccccc", "grep")
			msgs.add(toolUseMessage("M3", "One more look.", last))
			runN := d.textGuard.Load()

			ctx, cancel := context.WithCancel(context.Background())
			sub := make(chan pubsub.Event[message.PartEvent], 4)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); d.drainParts(ctx, sub, runN) }()
			go func() { defer wg.Done(); d.runParts(ctx) }()
			defer func() { cancel(); wg.Wait() }()

			sub <- callPart("S1", "M1", first)
			waitFor(t, "run N's first text", func() bool { return len(ed.Sends()) == 1 })

			// Run N ends; its last parts are handled only now.
			d.textGuard.Store(tt.next)
			sub <- callPart("S1", "M1", second)
			sub <- callPart("S1", "M3", last)
			waitFor(t, "run N's last text", func() bool {
				return countTextPosts(ed.Sends(), "⌛ grep\nOne more look.") == 1
			})

			if n := countTextPosts(ed.Sends(), "⌛ bash, read\n"); n != 1 {
				t.Errorf("M1 posted %d times; want 1 (sends %q)", n, sendTexts(ed.Sends()))
			}
		})
	}
}

// TestFlushIntermediateText_SlowSendNotCancelled: the question router
// stops waiting after intermediateFlushWait, but the text it claimed is
// still delivered, once. Cancelling the send at the wait bound would lose
// the text: the claim stays, so the parts path skips the message.
func TestFlushIntermediateText_SlowSendNotCancelled(t *testing.T) {
	oldWait := intermediateFlushWait
	intermediateFlushWait = 20 * time.Millisecond
	defer func() { intermediateFlushWait = oldWait }()

	svc, d, msgs, _ := newIntermediateTestSvc(t, &bridge.Config{})
	const delay = 300 * time.Millisecond
	slow := useSlowSlack(svc, delay)
	svc.dispatchMu.Lock()
	svc.dispatchers["S1"] = d
	svc.dispatchMu.Unlock()
	call := finishedCall("toolu_01aaaaaa", "question")
	msgs.add(toolUseMessage("M1", "Before I go on, one question.", call))

	start := time.Now()
	svc.flushIntermediateText(context.Background(), "S1")
	if elapsed := time.Since(start); elapsed >= delay {
		t.Errorf("flush returned after %v; want it bounded by the wait (%v), not the send (%v)",
			elapsed, intermediateFlushWait, delay)
	}

	c, ok := d.textGuard.Load().lookup("M1")
	if !ok {
		t.Fatal("the flush must claim the message")
	}
	select {
	case <-c.done:
	case <-time.After(3 * time.Second):
		t.Fatal("the flush's send never finished")
	}
	// The parts path arrives later and must not post the text again.
	d.handlePartEvent(callPart("S1", "M1", call), d.textGuard.Load())

	if n := countTextPosts(slow.Sends(), "⌛ question\nBefore I go on, one question."); n != 1 {
		t.Errorf("pre-question text delivered %d times; want 1 (sends %q)", n, sendTexts(slow.Sends()))
	}
}

// TestIntermediateText_PartsPathSendIsBounded: the parts-path post is
// synchronous on runParts, so a chat API that never answers must not
// hold it past intermediateSendTimeout.
func TestIntermediateText_PartsPathSendIsBounded(t *testing.T) {
	oldTimeout := intermediateSendTimeout
	intermediateSendTimeout = 20 * time.Millisecond
	defer func() { intermediateSendTimeout = oldTimeout }()

	svc, d, msgs, _ := newIntermediateTestSvc(t, &bridge.Config{})
	useSlowSlack(svc, time.Hour)
	call := finishedCall("toolu_01aaaaaa", "bash")
	msgs.add(toolUseMessage("M1", "Checking the logs.", call))

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.handlePartEvent(callPart("S1", "M1", call), d.textGuard.Load())
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the parts path stayed blocked on a hung chat API")
	}
}

// TestIntermediateText_FinalReplyWaitsForInFlightText: the final reply
// waits for an intermediate post of its run that is still in flight, so it
// does not land above it, but for at most intermediateFlushWait.
func TestIntermediateText_FinalReplyWaitsForInFlightText(t *testing.T) {
	oldWait := intermediateFlushWait
	intermediateFlushWait = 200 * time.Millisecond
	defer func() { intermediateFlushWait = oldWait }()

	const hold = 50 * time.Millisecond
	tests := []struct {
		name    string
		release bool
		minWait time.Duration
	}{
		{"released", true, hold},
		{"never released", false, 200 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, d, _, ed := newIntermediateTestSvc(t, &bridge.Config{})
			c, _ := d.textGuard.Load().claim("M1")
			if tt.release {
				time.AfterFunc(hold, func() { close(c.done) })
			}

			start := time.Now()
			d.handleTerminalEvent(context.Background(), finalReply("M2", "All done."))
			elapsed := time.Since(start)

			if elapsed < tt.minWait {
				t.Errorf("final reply posted after %v; want it to wait at least %v", elapsed, tt.minWait)
			}
			if elapsed >= 3*time.Second {
				t.Errorf("final reply posted after %v; want the wait bounded", elapsed)
			}
			if sends := ed.Sends(); len(sends) != 1 || sends[0].Text != "All done." {
				t.Errorf("sends = %q; want exactly [\"All done.\"]", sendTexts(sends))
			}
		})
	}
}

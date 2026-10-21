package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/task"
)

// compactSessionCall is a model response that calls compact_session.
func compactSessionCall(id, keep string) *provider.ProviderResponse {
	return &provider.ProviderResponse{
		ToolCalls: []message.ToolCall{{
			ID: id, Name: tools.CompactSessionToolName,
			Input: `{"keep":"` + keep + `"}`, Finished: true,
		}},
		FinishReason: message.FinishReasonToolUse,
	}
}

// newSelfCompactingAgent wires newCompactingAgent (window 1000, reported
// size 500, under the auto threshold) with compact_session at minTokens and
// scripts the main model: responses[i] answers call i+1, then "task done".
func newSelfCompactingAgent(t *testing.T, minTokens int64, responses ...*provider.ProviderResponse) (*agent, *compactionProvider, *compactionProvider, *[]string) {
	t.Helper()
	a, main, summarizer, log := newCompactingAgent(t, 500, []tools.BaseTool{tools.NewCompactSessionTool(minTokens)})
	a.selfCompact = config.SelfCompact{Enabled: true, MinTokens: minTokens}
	var calls atomic.Int32
	main.respond = func([]message.Message) *provider.ProviderResponse {
		n := int(calls.Add(1))
		if n <= len(responses) {
			return responses[n-1]
		}
		return &provider.ProviderResponse{Content: "task done", FinishReason: message.FinishReasonEndTurn}
	}
	return a, main, summarizer, log
}

func requestCount(p *compactionProvider) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func summarizerPrompt(t *testing.T, p *compactionProvider, i int) string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) <= i || len(p.requests[i]) == 0 {
		t.Fatalf("summarizer request %d missing (%d requests)", i, len(p.requests))
	}
	return firstText(p.requests[i][0])
}

func toolResultContent(msgs []message.Message, name string) []string {
	var out []string
	for _, m := range msgs {
		for _, r := range m.ToolResults() {
			if r.Name == name {
				out = append(out, r.Content)
			}
		}
	}
	return out
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The tool only stores the request; the summarizer runs once the turn's
// terminal event is out (and, with WithTurnDelivery, confirmed delivered),
// before the events channel closes, and its prompt carries the keep notes.
func TestSelfCompaction_RunsAfterTheTurnNotInsideIt(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	a, _, summarizer, log := newSelfCompactingAgent(t, 150, compactSessionCall("c1", "KEEP: open item 42"))
	const sess = "sess-self-after-turn"

	ctx, delivered := WithTurnDelivery(context.Background())
	events, err := a.RunWith(ctx, sess, "do the task", 0, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ev := <-events
	if ev.Error != nil {
		t.Fatalf("turn error: %v", ev.Error)
	}
	if ev.Type != AgentEventTypeResponse {
		t.Fatalf("terminal event type = %q, want the turn's response", ev.Type)
	}
	msgs, _ := a.messages.List(context.Background(), sess)
	if got := toolResultContent(msgs, tools.CompactSessionToolName); len(got) != 1 || !strings.HasPrefix(got[0], "requested: 500 tokens") {
		t.Fatalf("compact_session results = %q, want one stored request at 500 tokens", got)
	}

	// The turn is over but the reply is not confirmed delivered: nothing
	// has been compacted, and the session is still held.
	time.Sleep(50 * time.Millisecond)
	if n := requestCount(summarizer); n != 0 {
		t.Fatalf("summarizer called %d time(s) inside the turn or before delivery", n)
	}
	if !a.IsSessionBusy(sess) {
		t.Error("session released before the post-turn compaction ran")
	}

	delivered()
	for range events {
	}
	if n := requestCount(summarizer); n != 1 {
		t.Fatalf("summarizer called %d time(s), want 1", n)
	}
	if got := *log; len(got) != 3 || got[0] != "main" || got[1] != "main" || got[2] != "summarizer" {
		t.Errorf("call order = %v, want [main main summarizer]", got)
	}
	prompt := summarizerPrompt(t, summarizer, 0)
	if !strings.Contains(prompt, "Carry these forward verbatim") || !strings.Contains(prompt, "KEEP: open item 42") {
		t.Errorf("summarizer prompt does not carry the keep notes:\n%s", prompt)
	}
	sessRow, _ := a.sessions.Get(context.Background(), sess)
	if sessRow.SummaryMessageID == "" {
		t.Fatal("no summary recorded")
	}
	history, _, _ := a.historyAfterCompaction(context.Background(), sess)
	if pending, _ := pendingSelfCompactionNotes(history); pending {
		t.Error("the request is still pending after its compaction")
	}
	waitUntil(t, "the session slot to be released", func() bool { return !a.IsSessionBusy(sess) })
}

// Below the floor the tool reports "skipped", nothing is stored, and
// neither this turn's end nor the next turn compacts.
func TestSelfCompaction_BelowFloorStoresNothing(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	a, _, summarizer, _ := newSelfCompactingAgent(t, 600, compactSessionCall("c1", "notes"))
	const sess = "sess-self-floor"

	events, err := a.RunWith(context.Background(), sess, "do the task", 0, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	msgs, _ := a.messages.List(context.Background(), sess)
	if got := toolResultContent(msgs, tools.CompactSessionToolName); len(got) != 1 || got[0] != "skipped: 500 tokens < floor 600" {
		t.Fatalf("compact_session results = %q, want one skipped call", got)
	}
	if pending, _ := pendingSelfCompactionNotes(msgs); pending {
		t.Fatal("a skipped call stored a request")
	}

	waitUntil(t, "the session slot to be released", func() bool { return !a.IsSessionBusy(sess) })
	res := a.processGeneration(context.Background(), sess, "next task", 0, nil, RunOptions{})
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if n := requestCount(summarizer); n != 0 {
		t.Errorf("summarizer called %d time(s), want 0", n)
	}
}

// A request whose post-turn compaction never ran (the process stopped) is
// in the persisted log; a fresh agent on the same stores compacts before the
// next turn's first model call, with autoCompact off, and the notes reach
// the summarizer.
func TestSelfCompaction_PendingRequestSurvivesReloadAndRunsBeforeNextTurn(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, false)
	first, _, _, _ := newSelfCompactingAgent(t, 150)
	const sess = "sess-self-reload"
	req := `{"compaction_requested":true,"tokens":500,"keep":"RESUME-NOTE"}`
	seedHistory(t, first, sess,
		textMsg(message.User, "do the task"),
		message.Message{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "c1", Name: tools.CompactSessionToolName, Input: `{"keep":"RESUME-NOTE"}`, Finished: true},
		}},
		message.Message{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "c1", Name: tools.CompactSessionToolName, Content: "requested", Metadata: req},
		}},
		textMsg(message.Assistant, "task done"),
	)
	seedReport(t, first, sess, 500)

	// The reload: a new agent instance over the same session and message stores.
	a, main, summarizer, log := newSelfCompactingAgent(t, 150)
	a.sessions, a.messages = first.sessions, first.messages

	res := a.processGeneration(context.Background(), sess, "next task", 0, nil, RunOptions{})
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if got := *log; len(got) < 2 || got[0] != "summarizer" || got[1] != "main" {
		t.Fatalf("call order = %v, want the summarizer before the first model call", got)
	}
	if got := firstText(main.requests[0][0]); got != testSummaryText {
		t.Errorf("first model request starts with %q, want the summary", got)
	}
	if prompt := summarizerPrompt(t, summarizer, 0); !strings.Contains(prompt, "verbatim:\n\nRESUME-NOTE") {
		t.Errorf("summarizer prompt lacks the stored keep note:\n%s", prompt)
	}
	if n := requestCount(summarizer); n != 1 {
		t.Errorf("summarizer called %d time(s), want 1", n)
	}
	// The new user message lands after the summary, so it survives reloads.
	history, _, _ := a.historyAfterCompaction(context.Background(), sess)
	if len(history) < 2 || firstText(history[0]) != testSummaryText || !strings.HasPrefix(firstText(history[1]), "next task") {
		t.Errorf("history after the turn = %v, want the summary then the new task", texts(history))
	}
}

// One busy session compacts once: two requests in one turn are served by a
// single compaction, nothing else can run on the session while it does, and
// the next turn finds nothing pending.
func TestSelfCompaction_BusySessionNeverCompactsTwice(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	a, _, summarizer, _ := newSelfCompactingAgent(t, 150,
		compactSessionCall("c1", "NOTE-A"),
		compactSessionCall("c2", "NOTE-B"),
	)
	const sess = "sess-self-once"

	ctx, delivered := WithTurnDelivery(context.Background())
	events, err := a.RunWith(ctx, sess, "do the task", 0, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	<-events

	// Held at the delivery barrier, before compacting: the session is busy
	// for every other entry point.
	if _, err := a.Run(context.Background(), sess, "another message", 0); !errors.Is(err, ErrSessionBusy) {
		t.Errorf("Run during the post-turn compaction = %v, want ErrSessionBusy", err)
	}
	if err := a.SummarizeSync(context.Background(), sess); !errors.Is(err, ErrSessionBusy) {
		t.Errorf("SummarizeSync during the post-turn compaction = %v, want ErrSessionBusy", err)
	}
	if err := a.Summarize(context.Background(), sess); !errors.Is(err, ErrSessionBusy) {
		t.Errorf("Summarize during the post-turn compaction = %v, want ErrSessionBusy", err)
	}

	delivered()
	for range events {
	}
	if n := requestCount(summarizer); n != 1 {
		t.Fatalf("summarizer called %d time(s) for two requests in one turn, want 1", n)
	}
	prompt := summarizerPrompt(t, summarizer, 0)
	if !strings.HasSuffix(prompt, "verbatim:\n\nNOTE-A\n\nNOTE-B") {
		t.Errorf("summarizer prompt lacks a request's notes:\n%s", prompt)
	}

	waitUntil(t, "the session slot to be released", func() bool { return !a.IsSessionBusy(sess) })
	next, err := a.Run(context.Background(), sess, "next task", 0)
	if err != nil {
		t.Fatal(err)
	}
	for range next {
	}
	if n := requestCount(summarizer); n != 1 {
		t.Errorf("summarizer called %d time(s) after the next turn, want still 1", n)
	}
}

// A turn that ends in an error leaves the request pending; the next turn
// serves it before its first model call.
func TestSelfCompaction_FailedTurnLeavesTheRequestForTheNextTurn(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, false)
	a, main, summarizer, log := newSelfCompactingAgent(t, 150)
	// The first call stores the request; every later call of the turn fails.
	main.respond = func([]message.Message) *provider.ProviderResponse {
		main.fail = errors.New("upstream reset")
		return compactSessionCall("c1", "NOTE")
	}
	const sess = "sess-self-failed"

	events, err := a.RunWith(context.Background(), sess, "do the task", 0, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ev := <-events
	for range events {
	}
	if ev.Error == nil {
		t.Fatal("the turn did not end in an error")
	}
	if n := requestCount(summarizer); n != 0 {
		t.Fatalf("summarizer ran after a failed turn (%d calls)", n)
	}
	msgs, _ := a.messages.List(context.Background(), sess)
	if pending, _ := pendingSelfCompactionNotes(msgs); !pending {
		t.Fatal("the request was not stored")
	}

	waitUntil(t, "the session slot to be released", func() bool { return !a.IsSessionBusy(sess) })
	main.fail = nil
	main.respond = func([]message.Message) *provider.ProviderResponse {
		return &provider.ProviderResponse{Content: "retried", FinishReason: message.FinishReasonEndTurn}
	}
	*log = nil
	res := a.processGeneration(context.Background(), sess, "retry", 0, nil, RunOptions{})
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if got := *log; len(got) != 2 || got[0] != "summarizer" || got[1] != "main" {
		t.Errorf("call order = %v, want the pending compaction before the first model call", got)
	}
	if prompt := summarizerPrompt(t, summarizer, 0); !strings.HasSuffix(prompt, "verbatim:\n\nNOTE") {
		t.Errorf("summarizer prompt lacks the keep note:\n%s", prompt)
	}
}

func TestPendingSelfCompactionNotes(t *testing.T) {
	result := func(name, metadata string, isErr bool) message.Message {
		return message.Message{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{Name: name, Metadata: metadata, IsError: isErr},
		}}
	}
	req := func(keep string) string {
		return `{"compaction_requested":true,"tokens":1,"keep":"` + keep + `"}`
	}
	msgs := []message.Message{
		textMsg(message.User, "hi"),
		result(tools.CompactSessionToolName, "", false),       // skipped
		result(tools.CompactSessionToolName, req("A"), true),  // error
		result("bash", req("X"), false),                       // another tool
		result(tools.CompactSessionToolName, req("A"), false), // stored
		result(tools.CompactSessionToolName, req(""), false),  // stored, no notes
		result(tools.CompactSessionToolName, req("A"), false), // duplicate notes
		result(tools.CompactSessionToolName, req("B"), false), // stored
	}
	pending, notes := pendingSelfCompactionNotes(msgs)
	if !pending || strings.Join(notes, "|") != "A|B" {
		t.Errorf("pending %v notes %q, want true [A B]", pending, notes)
	}
	if pending, _ := pendingSelfCompactionNotes(msgs[:4]); pending {
		t.Error("skipped, failed and foreign results read as pending")
	}

	prompt := textMsg(message.User, "SUMMARIZE")
	if got := withCarryForward(prompt, nil); firstText(got) != "SUMMARIZE" {
		t.Errorf("prompt without notes changed: %q", firstText(got))
	}
	got := firstText(withCarryForward(prompt, notes))
	if !strings.HasPrefix(got, "SUMMARIZE\n\n") || !strings.HasSuffix(got, "verbatim:\n\nA\n\nB") {
		t.Errorf("prompt with notes = %q", got)
	}
}

// completionDeps is the task.Deps a background completion goes through: it
// writes the synthetic pair into the agent's store, reads the process-wide
// session ledger, and records each auto-resume with whether the session was
// still held when it fired.
type completionDeps struct {
	messages message.Service
	resumed  chan string
	heldAt   atomic.Int32
}

func (d *completionDeps) WritePair(ctx context.Context, sessionID string, p task.SyntheticPair) error {
	_, _, err := d.messages.CreatePair(ctx, sessionID,
		message.CreateMessageParams{Role: message.Assistant, Synthetic: true, Parts: []message.ContentPart{
			message.ToolCall{ID: p.AssistantToolCallID, Name: p.AssistantToolName, Input: p.AssistantInput, Type: "tool_use", Finished: true},
		}},
		message.CreateMessageParams{Role: message.Tool, Synthetic: true, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: p.ToolToolCallID, Name: p.ToolName, Content: p.ToolContent},
		}},
	)
	return err
}
func (d *completionDeps) IsSessionBusy(sessionID string) bool { return SessionBusy(sessionID) }
func (d *completionDeps) ResumeSession(sessionID, requester string, _ map[string]any) {
	if SessionBusy(sessionID) {
		d.heldAt.Add(1)
	}
	d.resumed <- requester
}

// A background task finishing while the post-turn compaction holds the
// session (during the delivery wait, or during the summarizer call) skips
// its auto-resume and lands before the summary. Its pair is moved after the
// summary, so later turns still see it, and the session is resumed for it
// once the run lets the session go.
func TestSelfCompaction_CompletionDuringCompactionIsKeptAndResumed(t *testing.T) {
	reg := withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	a, _, summarizer, _ := newSelfCompactingAgent(t, 150, compactSessionCall("c1", "NOTE"))
	const sess = "sess-self-late-completion"
	deps := &completionDeps{messages: a.messages, resumed: make(chan string, 4)}
	t.Cleanup(task.SetDeps(deps))
	t.Cleanup(func() { task.TakeSkippedResume(sess) })

	complete := func(content string) {
		t.Helper()
		tk := &task.Task{ID: task.NewTaskID(task.KindBash), SessionID: sess, Kind: task.KindBash, OriginatingToolName: "bash", Requester: "alice@"}
		if err := reg.Register(tk); err != nil {
			t.Fatal(err)
		}
		if err := task.EnqueueTaskCompletion(context.Background(), task.CompletionInput{
			SessionID: sess, OriginatingToolName: "bash", TaskID: tk.ID, Kind: task.KindBash,
			Status: task.StatusCompleted, Content: content, SuppressIfNotified: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	summarizer.respond = func([]message.Message) *provider.ProviderResponse {
		complete("BG-DURING-SUMMARY")
		return &provider.ProviderResponse{Content: testSummaryText, FinishReason: message.FinishReasonEndTurn}
	}

	ctx, delivered := WithTurnDelivery(context.Background())
	events, err := a.RunWith(ctx, sess, "do the task", 0, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	<-events
	complete("BG-DURING-DELIVERY")
	delivered()
	for range events {
	}

	select {
	case requester := <-deps.resumed:
		if requester != "alice@" {
			t.Errorf("resume requester = %q, want alice@", requester)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session was never resumed for the completions")
	}
	if n := deps.heldAt.Load(); n != 0 {
		t.Errorf("%d resume(s) fired while the session was still held", n)
	}
	select {
	case <-deps.resumed:
		t.Error("resumed more than once")
	case <-time.After(50 * time.Millisecond):
	}
	if n := requestCount(summarizer); n != 1 {
		t.Fatalf("summarizer called %d time(s), want 1", n)
	}

	history, _, _ := a.historyAfterCompaction(context.Background(), sess)
	if len(history) == 0 || firstText(history[0]) != testSummaryText {
		t.Fatalf("history = %v, want it to start at the summary", texts(history))
	}
	for _, c := range []string{"BG-DURING-DELIVERY", "BG-DURING-SUMMARY"} {
		if !hasToolResult(history, c) {
			t.Errorf("completion %q is hidden behind the summary; history = %v", c, roles(history))
		}
	}
	// Each pair is a tool call followed by its result, and nothing is
	// duplicated in the full log.
	if got := roles(history[1:]); len(got) != 4 || got[0] != message.Assistant || got[1] != message.Tool || got[2] != message.Assistant || got[3] != message.Tool {
		t.Errorf("roles after the summary = %v, want two call/result pairs", got)
	}
	all, _ := a.messages.List(context.Background(), sess)
	if n := len(toolResultContent(all, "bash")); n != 2 {
		t.Errorf("full log holds %d completion results, want 2 (no duplicates)", n)
	}
}

// Cover both sides of the handoff without scheduling-dependent sleeps. The
// completion during StreamResponse is written before the assistant response
// row, so a last-response sequence watermark would also lose it.
func TestSelfCompaction_CompletionHandoff(t *testing.T) {
	for _, when := range []string{"already observed", "during final call", "after terminal response"} {
		t.Run(when, func(t *testing.T) {
			reg := withFreshTaskRegistry(t)
			withAutoCompact(t, true)
			a, main, _, _ := newSelfCompactingAgent(t, 150)
			const sess = "sess-self-handoff-completion"
			deps := &completionDeps{messages: a.messages, resumed: make(chan string, 4)}
			t.Cleanup(task.SetDeps(deps))
			t.Cleanup(func() { task.TakeSkippedResume(sess) })
			if !acquireSessionSlot(sess, context.CancelFunc(func() {})) {
				t.Fatal("session unexpectedly busy")
			}
			t.Cleanup(func() { releaseSessionSlot(sess) })
			complete := func() {
				tk := &task.Task{ID: task.NewTaskID(task.KindBash), SessionID: sess, Kind: task.KindBash, OriginatingToolName: "bash", Requester: "alice@"}
				if err := reg.Register(tk); err != nil {
					t.Fatal(err)
				}
				if err := task.EnqueueTaskCompletion(context.Background(), task.CompletionInput{
					SessionID: sess, OriginatingToolName: "bash", TaskID: tk.ID, Kind: task.KindBash,
					Status: task.StatusCompleted, Content: "BG-HANDOFF", SuppressIfNotified: true,
				}); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			main.respond = func([]message.Message) *provider.ProviderResponse {
				calls++
				if calls == 1 {
					return compactSessionCall("c1", "NOTE")
				}
				if when == "during final call" {
					complete()
				}
				return &provider.ProviderResponse{Content: "task done", FinishReason: message.FinishReasonEndTurn}
			}
			if when == "already observed" {
				complete()
			}
			result := a.processGeneration(context.Background(), sess, "do the task", 0, nil, RunOptions{})
			if result.Error != nil {
				t.Fatal(result.Error)
			}
			if when == "after terminal response" {
				complete()
			}
			after := a.compactAfterTurn(context.Background(), context.Background(), sess, result)
			releaseSessionSlot(sess)
			if after != nil {
				after()
			}
			history, _, err := a.historyAfterCompaction(context.Background(), sess)
			if err != nil {
				t.Fatal(err)
			}
			wantUnobserved := when != "already observed"
			if got := hasToolResult(history, "BG-HANDOFF"); got != wantUnobserved {
				t.Errorf("completion retained after summary = %v, want %v", got, wantUnobserved)
			}
			wantResumes := 0
			if wantUnobserved {
				wantResumes = 1
			}
			if got := len(deps.resumed); got != wantResumes {
				t.Errorf("resume count = %d, want %d", got, wantResumes)
			}
		})
	}
}

// A caller that cancels its ctx as soon as it has the terminal event does
// not abort the post-turn compaction.
func TestSelfCompaction_CallerCancelAfterTheTurnDoesNotAbortIt(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	a, _, summarizer, _ := newSelfCompactingAgent(t, 150, compactSessionCall("c1", "NOTE"))
	const sess = "sess-self-caller-cancel"

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	ctx, delivered := WithTurnDelivery(callerCtx)
	events, err := a.RunWith(ctx, sess, "do the task", 0, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ev := <-events; ev.Error != nil {
		t.Fatal(ev.Error)
	}
	cancelCaller()
	delivered()
	for range events {
	}
	if n := requestCount(summarizer); n != 1 {
		t.Fatalf("summarizer called %d time(s) after the caller cancelled, want 1", n)
	}
	history, _, _ := a.historyAfterCompaction(context.Background(), sess)
	if pending, _ := pendingSelfCompactionNotes(history); pending {
		t.Error("the request is still pending")
	}
	if n := a.compactionBackoff.failureCount(sess); n != 0 {
		t.Errorf("backoff failures = %d, want 0", n)
	}
}

// The agent's own Cancel stops a post-turn compaction; the request stays
// pending and nothing is recorded in the backoff.
func TestSelfCompaction_AgentCancelStopsThePostTurnCompaction(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	a, _, summarizer, _ := newSelfCompactingAgent(t, 150, compactSessionCall("c1", "NOTE"))
	const sess = "sess-self-agent-cancel"

	ctx, delivered := WithTurnDelivery(context.Background())
	defer delivered()
	events, err := a.RunWith(ctx, sess, "do the task", 0, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	<-events
	a.Cancel(sess)
	drained := make(chan struct{})
	go func() {
		for range events {
		}
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("Cancel did not end the post-turn compaction")
	}
	if n := requestCount(summarizer); n != 0 {
		t.Errorf("summarizer called %d time(s) after Cancel, want 0", n)
	}
	msgs, _ := a.messages.List(context.Background(), sess)
	if pending, _ := pendingSelfCompactionNotes(msgs); !pending {
		t.Error("the request did not stay pending")
	}
	if n := a.compactionBackoff.failureCount(sess); n != 0 {
		t.Errorf("backoff failures = %d, want 0", n)
	}
}

// A flow step's run does not hold the session past its turn: the flow
// runner reuses the session as soon as it has the terminal event. The
// request is served before the session's next turn.
func TestSelfCompaction_FlowStepDefersToTheNextTurn(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, false)
	a, _, summarizer, log := newSelfCompactingAgent(t, 150, compactSessionCall("c1", "NOTE"))
	const sess = "sess-self-flow-step"
	stepCtx := context.WithValue(context.Background(), tools.StepScopedContextKey, context.Background())

	events, err := a.RunWith(stepCtx, sess, "do the step", 0, RunOptions{NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	if n := requestCount(summarizer); n != 0 {
		t.Fatalf("summarizer called %d time(s) after a flow step's turn, want 0", n)
	}
	msgs, _ := a.messages.List(context.Background(), sess)
	if pending, _ := pendingSelfCompactionNotes(msgs); !pending {
		t.Fatal("the request is not pending")
	}

	waitUntil(t, "the session slot to be released", func() bool { return !a.IsSessionBusy(sess) })
	*log = nil
	next, err := a.RunWith(stepCtx, sess, "the next step", 0, RunOptions{NonInteractive: true})
	if err != nil {
		t.Fatalf("the next run on the session: %v", err)
	}
	for range next {
	}
	if got := *log; len(got) < 2 || got[0] != "summarizer" || got[1] != "main" {
		t.Errorf("call order = %v, want the pending compaction before the first model call", got)
	}
}

// An auto-compaction cancelled through its ctx is not a summarizer failure.
func TestAutoCompact_CancelledAttemptIsNotABackoffFailure(t *testing.T) {
	withAutoCompact(t, true)
	a, _, summarizer, _ := newCompactingAgent(t, 500, nil)
	const sess = "sess-autocompact-cancel"
	seedHistory(t, a, sess, textMsg(message.User, "hello"), textMsg(message.Assistant, "hi"))
	summarizer.fail = errors.New("aborted")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.autoCompact(cancelled, sess, compactionTriggerSelfAfterTurn, 500); err == nil {
		t.Fatal("autoCompact succeeded with a failing summarizer")
	}
	if n := a.compactionBackoff.failureCount(sess); n != 0 {
		t.Errorf("backoff failures after a cancelled attempt = %d, want 0", n)
	}
	if err := a.autoCompact(context.Background(), sess, compactionTriggerSelfAfterTurn, 500); err == nil {
		t.Fatal("autoCompact succeeded with a failing summarizer")
	}
	if n := a.compactionBackoff.failureCount(sess); n != 1 {
		t.Errorf("backoff failures after a failed attempt = %d, want 1", n)
	}
}

// A self-compaction's summarizer trace is named `<agent>/self-compaction`,
// apart from the agent's turn traces.
func TestSelfCompaction_TraceNamedAsSelfCompaction(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	setLangfuseClient(t, true)
	a, main, summarizer, _ := newSelfCompactingAgent(t, 150, compactSessionCall("c1", "NOTE"))
	const sess = "sess-self-trace"

	events, err := a.RunWith(context.Background(), sess, "do the task", 0, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	summarizer.mu.Lock()
	summarizerNames := slices.Clone(summarizer.traceNames)
	summarizer.mu.Unlock()
	if len(summarizerNames) != 1 || summarizerNames[0] != "coder/self-compaction" {
		t.Errorf("summarizer trace names = %q, want [coder/self-compaction]", summarizerNames)
	}
	main.mu.Lock()
	turnName := main.traceNames[0]
	main.mu.Unlock()
	if turnName != "coder" {
		t.Errorf("turn trace name = %q, want coder", turnName)
	}

	// In a flow the suffix follows agent/flow/step and survives truncation.
	flowCtx := context.WithValue(context.Background(), tools.FlowIDContextKey, strings.Repeat("f", 300))
	flowCtx = context.WithValue(flowCtx, tools.FlowStepIDContextKey, "step")
	name := spanTraceName(a.createLangfuseTraceNamed(flowCtx, session.Session{ID: sess}, selfCompactionTraceSuffix, nil))
	if !strings.HasPrefix(name, "coder/fff") || !strings.HasSuffix(name, "/self-compaction") || len(name) > maxTraceNameLen {
		t.Errorf("flow trace name = %q (%d bytes), want coder/<flow>... ending /self-compaction within %d", name, len(name), maxTraceNameLen)
	}
}

func TestCompactionTraceInput(t *testing.T) {
	for trigger, want := range map[string]string{
		compactionTriggerSelfAfterTurn: selfCompactionTraceSuffix,
		compactionTriggerSelfPreTurn:   selfCompactionTraceSuffix,
		compactionTriggerPreTurn:       "",
		compactionTriggerInLoop:        "",
	} {
		if got := compactionTraceNameSuffix(trigger); got != want {
			t.Errorf("compactionTraceNameSuffix(%q) = %q, want %q", trigger, got, want)
		}
	}
	for trigger, want := range map[string]string{
		compactionTriggerSelfAfterTurn: "self-compaction requested by agent over 7 messages",
		compactionTriggerSelfPreTurn:   "self-compaction requested by agent over 7 messages",
		compactionTriggerPreTurn:       "auto-compaction over 7 messages",
		compactionTriggerInLoop:        "auto-compaction over 7 messages",
	} {
		if got := compactionTraceInput(trigger, 7); got != want {
			t.Errorf("compactionTraceInput(%q) = %q, want %q", trigger, got, want)
		}
	}
}

// selfCompactRegistry enables compact_session alone, or nothing (denyTool:
// the agent's tools exclude it).
type selfCompactRegistry struct {
	agentregistry.Registry
	denyTool bool
}

func (r selfCompactRegistry) IsToolEnabled(_, name string) bool {
	return !r.denyTool && name == tools.CompactSessionToolName
}
func (r selfCompactRegistry) IsToolExplicitlyEnabled(string, string) bool { return false }
func (r selfCompactRegistry) HasTools(string) bool                        { return true }

func TestCompactSessionToolRegistration(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	on := &config.SelfCompact{Enabled: true, MinTokens: 10}
	cases := []struct {
		name string
		info agentregistry.AgentInfo
		reg  selfCompactRegistry
		want bool
	}{
		{"enabled primary agent", agentregistry.AgentInfo{ID: "daemon", Mode: config.AgentModeAgent, SelfCompact: on}, selfCompactRegistry{}, true},
		{"unset", agentregistry.AgentInfo{ID: "daemon", Mode: config.AgentModeAgent}, selfCompactRegistry{}, false},
		{"disabled", agentregistry.AgentInfo{ID: "daemon", Mode: config.AgentModeAgent, SelfCompact: &config.SelfCompact{MinTokens: 10}}, selfCompactRegistry{}, false},
		{"subagent", agentregistry.AgentInfo{ID: "helper", Mode: config.AgentModeSubagent, SelfCompact: on}, selfCompactRegistry{}, false},
		{"tools exclude it", agentregistry.AgentInfo{ID: "daemon", Mode: config.AgentModeAgent, SelfCompact: on}, selfCompactRegistry{denyTool: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			found := false
			for tool := range NewToolSet(&tc.info, tc.reg, nil, nil, nil, nil, nil, nil, &lateHeartbeatFactory{}, "") {
				if tool.Info().Name == tools.CompactSessionToolName {
					found = true
				}
			}
			if found != tc.want {
				t.Errorf("compact_session registered = %v, want %v", found, tc.want)
			}
		})
	}
}

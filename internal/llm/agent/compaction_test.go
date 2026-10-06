package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/task"
)

// compactionProvider records every request it is sent into a log shared with
// the other provider of the test, so the order of summarizer and model calls
// is observable. count decides what CountTokens reports for a request.
type compactionProvider struct {
	name    string
	window  int64
	count   func(msgs []message.Message) int64
	respond func(msgs []message.Message) *provider.ProviderResponse
	system  string
	// fail, when set, makes every request end in a stream error.
	fail error

	mu       sync.Mutex
	log      *[]string
	requests [][]message.Message
	forced   []string // provider.ForcedTool on each request's ctx
	budgets  []int64  // task budget remaining on each request's ctx, -1 when unset
}

func (p *compactionProvider) StreamResponse(ctx context.Context, msgs []message.Message, _ []tools.BaseTool) <-chan provider.ProviderEvent {
	budget, ok := provider.TaskBudgetRemaining(ctx)
	if !ok {
		budget = -1
	}
	p.mu.Lock()
	*p.log = append(*p.log, p.name)
	p.requests = append(p.requests, append([]message.Message(nil), msgs...))
	p.forced = append(p.forced, provider.ForcedTool(ctx))
	p.budgets = append(p.budgets, budget)
	p.mu.Unlock()
	if p.fail != nil {
		ch := make(chan provider.ProviderEvent, 1)
		ch <- provider.ProviderEvent{Type: provider.EventError, Error: p.fail}
		close(ch)
		return ch
	}
	resp := &provider.ProviderResponse{Content: "done", FinishReason: message.FinishReasonEndTurn}
	if p.respond != nil {
		resp = p.respond(msgs)
	}
	ch := make(chan provider.ProviderEvent, 1)
	ch <- provider.ProviderEvent{Type: provider.EventComplete, Response: resp}
	close(ch)
	return ch
}

func (p *compactionProvider) SendMessages(context.Context, []message.Message, []tools.BaseTool) (*provider.ProviderResponse, error) {
	return nil, errors.New("compactionProvider: unexpected SendMessages call")
}

func (p *compactionProvider) Model() models.Model {
	return models.Model{ID: models.ModelID(p.name), ContextWindow: p.window}
}

func (p *compactionProvider) CountTokens(_ context.Context, threshold float64, msgs []message.Message, _ []tools.BaseTool) (int64, bool) {
	var n int64
	if p.count != nil {
		n = p.count(msgs)
	}
	return n, p.window > 0 && n >= int64(float64(p.window)*threshold)
}

func (p *compactionProvider) AdjustMaxTokens(estimated int64) int64 { return estimated }

func (p *compactionProvider) SystemMessage() string { return p.system }

func textMsg(role message.MessageRole, text string) message.Message {
	return message.Message{Role: role, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

func firstText(m message.Message) string {
	for _, p := range m.Parts {
		if t, ok := p.(message.TextContent); ok {
			return t.Text
		}
	}
	return ""
}

func withAutoCompact(t *testing.T, on bool) {
	t.Helper()
	loadConfigIn(t, t.TempDir())
	config.Get().AutoCompact = on
}

const testSummaryText = "SUMMARY OF EARLIER WORK"

// newCompactingAgent wires a main provider with a 1000-token window that
// reports overTokens for any history not headed by the summary (50 for one
// that is), and a summarizer that answers with testSummaryText.
func newCompactingAgent(t *testing.T, overTokens int64, ts []tools.BaseTool) (*agent, *compactionProvider, *compactionProvider, *[]string) {
	t.Helper()
	log := &[]string{}
	main := &compactionProvider{name: "main", window: 1000, log: log}
	main.count = func(msgs []message.Message) int64 {
		if len(msgs) > 0 && firstText(msgs[0]) == testSummaryText {
			return 50
		}
		return overTokens
	}
	summarizer := &compactionProvider{name: "summarizer", log: log,
		respond: func([]message.Message) *provider.ProviderResponse {
			return &provider.ProviderResponse{Content: testSummaryText, FinishReason: message.FinishReasonEndTurn}
		}}
	a := newLoopAgentWithTools(t, main, ts)
	a.summarizeProvider = summarizer
	return a, main, summarizer, log
}

func seedHistory(t *testing.T, a *agent, sessionID string, msgs ...message.Message) {
	t.Helper()
	for _, m := range msgs {
		if _, err := a.messages.Create(context.Background(), sessionID, message.CreateMessageParams{Role: m.Role, Parts: m.Parts, Synthetic: m.Synthetic}); err != nil {
			t.Fatal(err)
		}
	}
}

// completionPair is what task.EnqueueTaskCompletion writes for a finished
// background task: a synthetic tool call and its result.
func completionPair(callID, content string) []message.Message {
	return []message.Message{
		{Role: message.Assistant, Synthetic: true, Parts: []message.ContentPart{
			message.ToolCall{ID: callID, Name: "bash", Input: "{}", Finished: true},
		}},
		{Role: message.Tool, Synthetic: true, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: callID, Content: content},
		}},
	}
}

func hasToolResult(msgs []message.Message, content string) bool {
	for _, m := range msgs {
		for _, p := range m.Parts {
			if r, ok := p.(message.ToolResult); ok && r.Content == content {
				return true
			}
		}
	}
	return false
}

// assertSummaryThenPair checks a request is [summary, tool call, tool result].
func assertSummaryThenPair(t *testing.T, req []message.Message, callID, content string) {
	t.Helper()
	if len(req) != 3 || firstText(req[0]) != testSummaryText || req[1].Role != message.Assistant || req[2].Role != message.Tool {
		t.Fatalf("request = %v (roles %v), want [summary, assistant tool call, tool result]", texts(req), roles(req))
	}
	if calls := req[1].ToolCalls(); len(calls) != 1 || calls[0].ID != callID {
		t.Errorf("assistant message tool calls = %+v, want the completion's call %q", calls, callID)
	}
	if !hasToolResult(req[2:], content) {
		t.Errorf("tool message does not carry the completion result %q", content)
	}
}

// An auto-resume turn (no content) is driven by the synthetic completion pair
// at the end of the history. Compacting before it must not drop that pair:
// the summary lands after it, so the reload alone would leave the model only
// the summary to react to.
func TestProcessGeneration_PreTurnCompactionKeepsResumeTail(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	a, main, _, log := newCompactingAgent(t, 960, nil)

	const sess = "sess-resume"
	seedHistory(t, a, sess, textMsg(message.User, "start the build"), textMsg(message.Assistant, "started in the background"))
	seedHistory(t, a, sess, completionPair("bg-1", "BUILD OK")...)

	res := a.processGeneration(context.Background(), sess, "", 0, nil, RunOptions{})
	if res.Error != nil {
		t.Fatalf("processGeneration error: %v", res.Error)
	}
	if len(*log) < 2 || (*log)[0] != "summarizer" || (*log)[1] != "main" {
		t.Fatalf("call order = %v, want the summarizer before the first model call", *log)
	}
	assertSummaryThenPair(t, main.requests[0], "bg-1", "BUILD OK")
}

// completingRegistry finishes the session's background task the moment the
// non-interactive drain starts waiting on it, writing the completion pair
// first — what EnqueueTaskCompletion does — so the re-entry is deterministic.
type completingRegistry struct {
	task.Registry
	once     sync.Once
	complete func()
}

func (r *completingRegistry) WaitForActiveTasks(ctx context.Context, sessionID string, opts task.WaitOptions) error {
	r.once.Do(r.complete)
	return r.Registry.WaitForActiveTasks(ctx, sessionID, opts)
}

// A non-interactive re-entry resets cycles to 0, so its first call used to
// skip the in-loop check like a turn's first call does, with no pre-turn gate
// in front of it. A drained completion that pushes the history over must be
// compacted before the model sees it, and the completion must survive.
func TestProcessGeneration_ReentryAfterDrainIsGated(t *testing.T) {
	withAutoCompact(t, true)
	const sess = "sess-reentry"
	log := &[]string{}
	main := &compactionProvider{name: "main", window: 1000, log: log}
	main.count = func(msgs []message.Message) int64 {
		switch {
		case len(msgs) > 0 && firstText(msgs[0]) == testSummaryText:
			return 50
		case hasToolResult(msgs, "BIG OUTPUT"):
			return 960
		default:
			return 100
		}
	}
	summarizer := &compactionProvider{name: "summarizer", log: log,
		respond: func([]message.Message) *provider.ProviderResponse {
			return &provider.ProviderResponse{Content: testSummaryText, FinishReason: message.FinishReasonEndTurn}
		}}
	a := newLoopAgentWithTools(t, main, nil)
	a.summarizeProvider = summarizer

	dir := t.TempDir()
	inner := task.NewRegistry(func() string { return dir })
	taskID := task.NewTaskID(task.KindTask)
	if err := inner.Register(&task.Task{ID: taskID, SessionID: sess, Kind: task.KindTask}); err != nil {
		t.Fatalf("register task: %v", err)
	}
	reg := &completingRegistry{Registry: inner, complete: func() {
		seedHistory(t, a, sess, completionPair("bg-1", "BIG OUTPUT")...)
		inner.MarkFinished(taskID, task.StateCompleted, nil)
	}}
	task.ResetGlobalRegistry()
	task.SetGlobalRegistry(reg)
	t.Cleanup(task.ResetGlobalRegistry)

	res := a.processGeneration(context.Background(), sess, "run the job", 0, nil, RunOptions{NonInteractive: true})
	if res.Error != nil {
		t.Fatalf("processGeneration error: %v", res.Error)
	}
	if want := []string{"main", "summarizer", "main"}; !slices.Equal(*log, want) {
		t.Fatalf("call order = %v, want %v — the re-entry's first call must be checked", *log, want)
	}
	assertSummaryThenPair(t, main.requests[1], "bg-1", "BIG OUTPUT")
}

// The flow runner's struct_output rescue run forces tool_choice on the turn's
// ctx. The summarizer is sent no tools, so the forced choice must not reach it
// (Anthropic rejects a forced tool that is not in the request).
func TestProcessGeneration_CompactionDoesNotForceSummarizerTool(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	a, main, summarizer, _ := newCompactingAgent(t, 960, nil)

	const sess = "sess-forced"
	seedHistory(t, a, sess, textMsg(message.User, "q"), textMsg(message.Assistant, "a"))

	ctx := provider.WithForcedTool(context.Background(), tools.StructOutputToolName)
	res := a.processGeneration(ctx, sess, "next question", 0, nil, RunOptions{})
	if res.Error != nil {
		t.Fatalf("processGeneration error: %v", res.Error)
	}
	if len(summarizer.forced) != 1 || summarizer.forced[0] != "" {
		t.Fatalf("summarizer forced tools = %q, want one request with none", summarizer.forced)
	}
	if len(main.forced) == 0 || main.forced[0] != tools.StructOutputToolName {
		t.Errorf("main forced tools = %q, want the turn's own call still forced", main.forced)
	}
}

// listHookMessages runs onList once, after the first List call returns.
type listHookMessages struct {
	*memMessages
	once   sync.Once
	onList func()
}

func (m *listHookMessages) List(ctx context.Context, sessionID string) ([]message.Message, error) {
	msgs, err := m.memMessages.List(ctx, sessionID)
	m.once.Do(m.onList)
	return msgs, err
}

// The TUI starts an async Summarize after a turn that ended near the window,
// and it does not hold the session slot. A turn that starts meanwhile must
// wait for it and count what it leaves, not compact the stale history a
// second time.
func TestProcessGeneration_WaitsForInFlightSummarize(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	prevPoll := summarizeWaitPoll
	summarizeWaitPoll = 5 * time.Millisecond
	t.Cleanup(func() { summarizeWaitPoll = prevPoll })

	a, main, summarizer, _ := newCompactingAgent(t, 960, nil)
	const sess = "sess-tui"
	seedHistory(t, a, sess, textMsg(message.User, "q"), textMsg(message.Assistant, "a"))

	key := sess + "-summarize"
	a.activeRequests.Store(key, context.CancelFunc(func() {}))
	done := make(chan struct{})
	// The in-flight Summarize finishes only after the turn has listed the
	// pre-summary history, as it would in the TUI.
	a.messages = &listHookMessages{memMessages: a.messages.(*memMessages), onList: func() {
		go func() {
			defer close(done)
			time.Sleep(20 * time.Millisecond)
			created, err := a.messages.Create(context.Background(), sess, message.CreateMessageParams{
				Role:  message.Assistant,
				Parts: []message.ContentPart{message.TextContent{Text: testSummaryText}},
			})
			if err != nil {
				t.Error(err)
			}
			_, _ = a.sessions.Save(context.Background(), session.Session{ID: sess, SummaryMessageID: created.ID})
			a.activeRequests.Delete(key)
		}()
	}}

	res := a.processGeneration(context.Background(), sess, "next question", 0, nil, RunOptions{})
	<-done
	if res.Error != nil {
		t.Fatalf("processGeneration error: %v", res.Error)
	}
	if len(summarizer.requests) != 0 {
		t.Fatalf("summarizer called %d time(s) by the turn, want 0 — it must wait for the in-flight one", len(summarizer.requests))
	}
	if got := firstText(main.requests[0][0]); got != testSummaryText {
		t.Errorf("first model request starts with %q, want the in-flight summary", got)
	}
}

// A compaction before the turn starts must not shrink the turn's task budget:
// the turn has spent nothing yet, as on a turn that does not compact.
func TestProcessGeneration_PreTurnCompactionKeepsFullTaskBudget(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	config.Get().Agents[config.AgentName("coder")] = config.Agent{TaskBudget: 50_000}
	a, main, _, _ := newCompactingAgent(t, 960, nil)

	const sess = "sess-budget"
	_, _ = a.sessions.Save(context.Background(), session.Session{ID: sess, TotalCompletionTokens: 30_000})
	seedHistory(t, a, sess, textMsg(message.User, "q"), textMsg(message.Assistant, "a"))

	res := a.processGeneration(context.Background(), sess, "next question", 0, nil, RunOptions{})
	if res.Error != nil {
		t.Fatalf("processGeneration error: %v", res.Error)
	}
	if len(main.budgets) == 0 || main.budgets[0] != -1 {
		t.Fatalf("first model request task budget remaining = %v, want unset", main.budgets)
	}
}

// After a compaction the deferred-tools delta sits before the summary, out of
// the model's view. The next turn must announce the tools again, once.
func TestProcessGeneration_ReannouncesDeferredToolsAfterCompaction(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	a, main, _, _ := newCompactingAgent(t, 960, []tools.BaseTool{tools.WrapDeferred(noopTool{}, &atomic.Int64{})})
	ctx := context.Background()
	const sess = "sess-deferred"

	for _, content := range []string{"first", "second compacts", "third"} {
		if res := a.processGeneration(ctx, sess, content, 0, nil, RunOptions{}); res.Error != nil {
			t.Fatalf("processGeneration(%q) error: %v", content, res.Error)
		}
	}

	got, summarySeq := persistedDeltas(t, a, sess)
	if summarySeq == 0 || len(got) != 2 || got[1] <= summarySeq {
		t.Fatalf("delta seqs = %v, summary seq %d; want one delta before the summary and one after", got, summarySeq)
	}
	for i, req := range main.requests[1:] {
		if n := len(deltaSeqs(req)); n != 1 {
			t.Errorf("request after the compaction #%d carries %d deltas, want 1", i+1, n)
		}
	}
}

// A compaction between two model calls of a turn hides the delta behind the
// summary as well. A flow step or a task subagent is usually that one turn,
// so the turn's next request must announce the tools again, once, and a later
// turn must not add another.
func TestProcessGeneration_InLoopCompactionReannouncesDeferredTools(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	log := &[]string{}
	main := &compactionProvider{name: "main", window: 1000, log: log}
	main.count = func(msgs []message.Message) int64 {
		switch {
		case len(msgs) > 0 && firstText(msgs[0]) == testSummaryText:
			return 50
		case hasToolResult(msgs, "ok"):
			return 960
		default:
			return 100
		}
	}
	calls := 0
	main.respond = func([]message.Message) *provider.ProviderResponse {
		calls++
		if calls == 1 {
			return &provider.ProviderResponse{
				ToolCalls:    []message.ToolCall{{ID: "call-1", Name: "noop", Input: "{}", Finished: true}},
				FinishReason: message.FinishReasonToolUse,
			}
		}
		return &provider.ProviderResponse{Content: "done", FinishReason: message.FinishReasonEndTurn}
	}
	summarizer := &compactionProvider{name: "summarizer", log: log,
		respond: func([]message.Message) *provider.ProviderResponse {
			return &provider.ProviderResponse{Content: testSummaryText, FinishReason: message.FinishReasonEndTurn}
		}}
	deferred := tools.WrapDeferred(newMock("mcp_jira_get_issue", false), &atomic.Int64{})
	a := newLoopAgentWithTools(t, main, []tools.BaseTool{noopTool{}, deferred})
	a.summarizeProvider = summarizer
	ctx := context.Background()
	const sess = "sess-deferred-loop"

	for _, content := range []string{"do the task", "next"} {
		if res := a.processGeneration(ctx, sess, content, 0, nil, RunOptions{}); res.Error != nil {
			t.Fatalf("processGeneration(%q) error: %v", content, res.Error)
		}
	}
	if want := []string{"main", "summarizer", "main", "main"}; !slices.Equal(*log, want) {
		t.Fatalf("call order = %v, want %v — one compaction between the turn's two calls", *log, want)
	}
	for i, req := range main.requests {
		if n := len(deltaSeqs(req)); n != 1 {
			t.Errorf("model request #%d carries %d deltas, want 1", i+1, n)
		}
	}
	got, summarySeq := persistedDeltas(t, a, sess)
	if summarySeq == 0 || len(got) != 2 || got[1] <= summarySeq {
		t.Fatalf("delta seqs = %v, summary seq %d; want one delta before the summary and one after", got, summarySeq)
	}
}

// deltaSeqs returns the seqs of the deferred-tools delta messages in msgs.
func deltaSeqs(msgs []message.Message) (seqs []int64) {
	for _, m := range msgs {
		if m.Role == message.User && strings.Contains(firstText(m), deferredDeltaMarker) {
			seqs = append(seqs, m.Seq)
		}
	}
	return seqs
}

// persistedDeltas returns the seqs of the session's persisted delta messages
// and the seq of its current summary message (0 when there is none).
func persistedDeltas(t *testing.T, a *agent, sessionID string) ([]int64, int64) {
	t.Helper()
	persisted, err := a.messages.List(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	sessRow, _ := a.sessions.Get(context.Background(), sessionID)
	var summarySeq int64
	for _, m := range persisted {
		if m.ID == sessRow.SummaryMessageID {
			summarySeq = m.Seq
		}
	}
	return deltaSeqs(persisted), summarySeq
}

func TestProcessGeneration_CompactsBeforeFirstCall(t *testing.T) {
	const summaryText = "SUMMARY OF EARLIER WORK"
	cases := []struct {
		name          string
		historyTokens int64
		wantCompact   bool
	}{
		{"over the threshold compacts before the first call", 960, true},
		{"under the threshold does not compact", 100, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withFreshTaskRegistry(t)
			withAutoCompact(t, true)

			var log []string
			main := &compactionProvider{name: "main", window: 1000, log: &log}
			main.count = func(msgs []message.Message) int64 {
				if len(msgs) > 0 && firstText(msgs[0]) == summaryText {
					return 50
				}
				return tc.historyTokens
			}
			summarizer := &compactionProvider{name: "summarizer", log: &log,
				respond: func([]message.Message) *provider.ProviderResponse {
					return &provider.ProviderResponse{Content: summaryText, FinishReason: message.FinishReasonEndTurn}
				}}
			a := newLoopAgentWithTools(t, main, nil)
			a.summarizeProvider = summarizer

			const sess = "sess-pre-turn"
			ctx := context.Background()
			for _, m := range []message.Message{
				textMsg(message.User, "first question"),
				textMsg(message.Assistant, "first answer"),
			} {
				if _, err := a.messages.Create(ctx, sess, message.CreateMessageParams{Role: m.Role, Parts: m.Parts}); err != nil {
					t.Fatal(err)
				}
			}

			res := a.processGeneration(ctx, sess, "next question", 0, nil, RunOptions{})
			if res.Error != nil {
				t.Fatalf("processGeneration error: %v", res.Error)
			}

			if !tc.wantCompact {
				if len(summarizer.requests) != 0 {
					t.Fatalf("summarizer called %d time(s), want 0; call log %v", len(summarizer.requests), log)
				}
				return
			}
			if len(log) < 2 || log[0] != "summarizer" || log[1] != "main" {
				t.Fatalf("call order = %v, want the summarizer before the first model call", log)
			}
			first := main.requests[0]
			if got := firstText(first[0]); got != summaryText {
				t.Errorf("first model request starts with %q, want the summary", got)
			}
			if got := firstText(first[len(first)-1]); !strings.HasPrefix(got, "next question") {
				t.Errorf("first model request ends with %q, want the new user message", got)
			}

			sessRow, _ := a.sessions.Get(ctx, sess)
			persisted, _ := a.messages.List(ctx, sess)
			var summarySeq, userSeq int64
			for _, m := range persisted {
				if m.ID == sessRow.SummaryMessageID {
					summarySeq = m.Seq
				}
				if strings.HasPrefix(firstText(m), "next question") {
					userSeq = m.Seq
				}
			}
			if summarySeq == 0 || userSeq == 0 {
				t.Fatalf("summary seq %d / user seq %d not found in %d persisted messages", summarySeq, userSeq, len(persisted))
			}
			if userSeq <= summarySeq {
				t.Errorf("user message seq %d <= summary seq %d; the user turn must land after the summary", userSeq, summarySeq)
			}
		})
	}
}

func TestCountContextTokens(t *testing.T) {
	withAssistant := []message.Message{
		textMsg(message.User, "q"),
		textMsg(message.Assistant, "a"),
		textMsg(message.User, strings.Repeat("x", 400)),
	}
	// One text part: 400 chars + 100 bytes of per-part overhead, 4 B/token.
	const tailTokens = int64((400 + 100) / message.BytesPerTokenEta)

	cases := []struct {
		name      string
		window    int64
		estimate  int64
		sess      session.Session
		usage     *provider.TokenUsage // recorded through TrackUsage when set
		msgs      []message.Message
		wantCount int64
		wantHit   bool
	}{
		{
			name:      "undercounting estimate is floored by reported usage plus tail",
			window:    1000,
			estimate:  700,
			sess:      session.Session{PromptTokens: 600, CompletionTokens: 300},
			msgs:      withAssistant,
			wantCount: 900 + tailTokens,
			wantHit:   true,
		},
		{
			name:      "no assistant message ignores reported usage",
			window:    1000,
			estimate:  100,
			sess:      session.Session{PromptTokens: 600, CompletionTokens: 300},
			msgs:      []message.Message{textMsg(message.User, "q")},
			wantCount: 100,
			wantHit:   false,
		},
		{
			name:      "zero usage after compaction falls back to the estimate",
			window:    1000,
			estimate:  400,
			sess:      session.Session{PromptTokens: 0, CompletionTokens: 20},
			msgs:      withAssistant,
			wantCount: 400,
			wantHit:   false,
		},
		{
			name:      "zero context window never hits",
			window:    0,
			estimate:  700,
			sess:      session.Session{PromptTokens: 600, CompletionTokens: 300},
			msgs:      withAssistant,
			wantCount: 900 + tailTokens,
			wantHit:   false,
		},
		{
			// A Gemini call with a 500-token prompt, 480 of it cached, and 20
			// output tokens: 52% of the window. The cached part is reported
			// once, as CacheReadTokens, so the floor does not double it to 100%.
			name:      "gemini usage with a cached prompt is not double counted",
			window:    1000,
			estimate:  400,
			usage:     &provider.TokenUsage{InputTokens: 20, CacheReadTokens: 480, OutputTokens: 20},
			msgs:      withAssistant[:2],
			wantCount: 520,
			wantHit:   false,
		},
		{
			// An upstream that sums two attempts of one call reports about
			// twice the real prompt (a doubled cache read). Taken as the
			// floor it would fire compaction at half the real size.
			name:      "report at twice the estimate is ignored",
			window:    1_000_000,
			estimate:  690_000,
			usage:     &provider.TokenUsage{InputTokens: 30_000, CacheReadTokens: 1_383_037, OutputTokens: 1_000},
			msgs:      withAssistant[:2],
			wantCount: 690_000,
			wantHit:   false,
		},
		{
			name:      "report over the window is ignored",
			window:    1000,
			estimate:  900,
			sess:      session.Session{PromptTokens: 900, CompletionTokens: 300},
			msgs:      withAssistant[:2],
			wantCount: 900,
			wantHit:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var log []string
			est := tc.estimate
			p := &compactionProvider{name: "main", window: tc.window, log: &log,
				count: func([]message.Message) int64 { return est }}
			sessions := &memSessions{}
			tc.sess.ID = "s"
			_, _ = sessions.Save(context.Background(), tc.sess)
			a := &agent{provider: p, sessions: sessions}
			if tc.usage != nil {
				if err := a.TrackUsage(context.Background(), "s", p.Model(), *tc.usage); err != nil {
					t.Fatal(err)
				}
			}

			got, hit := a.countContextTokens(context.Background(), "s", 0.95, tc.msgs, nil)
			if got != tc.wantCount || hit != tc.wantHit {
				t.Errorf("countContextTokens = (%d, %v), want (%d, %v)", got, hit, tc.wantCount, tc.wantHit)
			}
		})
	}
}

func TestResolveCompactionThreshold(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	const cfgAgent = config.AgentName("compaction-threshold-cfg")
	config.Get().Agents[cfgAgent] = config.Agent{CompactionThreshold: 0.3}

	cases := []struct {
		name     string
		agentID  config.AgentName
		agentVal float64
		opts     RunOptions
		want     float64
	}{
		{"default", "compaction-threshold-none", 0, RunOptions{}, AutoCompactionThreshold},
		{"agent value", "compaction-threshold-none", 0.4, RunOptions{}, 0.4},
		{"flow step wins over agent", "compaction-threshold-none", 0.4, RunOptions{CompactionThreshold: 0.7}, 0.7},
		{"invalid agent value zeroed by validation inherits default", "compaction-threshold-none", 0, RunOptions{}, AutoCompactionThreshold},
		{"config value when the agent was built without one", cfgAgent, 0, RunOptions{}, 0.3},
		{"flow step above one is clamped", "compaction-threshold-none", 0.4, RunOptions{CompactionThreshold: 1.5}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &agent{agentID: tc.agentID, compactionThreshold: tc.agentVal}
			if got := a.resolveCompactionThreshold(tc.opts); got != tc.want {
				t.Errorf("resolveCompactionThreshold = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSummarizerInput(t *testing.T) {
	prompt := textMsg(message.User, "summarize")
	big := strings.Repeat("y", 3900) // (3900 + 100) / 4 = 1000 tokens per message

	toolUse := message.Message{Role: message.Assistant, Parts: []message.ContentPart{
		message.TextContent{Text: big},
		message.ToolCall{ID: "call-1", Name: "bash", Input: "{}", Finished: true},
	}}
	toolResult := message.Message{Role: message.Tool, Parts: []message.ContentPart{
		message.ToolResult{ToolCallID: "call-1", Content: big},
	}}

	build := func(t *testing.T, window int64, history []message.Message, summaryAt int) (*agent, string) {
		t.Helper()
		var log []string
		a := &agent{
			messages:          newMemMessages(),
			sessions:          &memSessions{},
			summarizeProvider: &compactionProvider{name: "summarizer", window: window, log: &log},
		}
		const sess = "sess-sum"
		ctx := context.Background()
		var summaryID string
		for i, m := range history {
			created, err := a.messages.Create(ctx, sess, message.CreateMessageParams{Role: m.Role, Parts: m.Parts, Synthetic: m.Synthetic})
			if err != nil {
				t.Fatal(err)
			}
			if i == summaryAt {
				summaryID = created.ID
			}
		}
		if summaryID != "" {
			_, _ = a.sessions.Save(ctx, session.Session{ID: sess, SummaryMessageID: summaryID})
		}
		return a, sess
	}

	t.Run("starts at the previous summary", func(t *testing.T) {
		a, sess := build(t, 0, []message.Message{
			textMsg(message.User, "old question"),
			textMsg(message.Assistant, "old answer"),
			textMsg(message.User, "PREVIOUS SUMMARY"),
			textMsg(message.User, "new question"),
			textMsg(message.Assistant, "new answer"),
		}, 2)
		got, err := a.summarizerInput(context.Background(), sess, prompt)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 4 || firstText(got[0]) != "PREVIOUS SUMMARY" || firstText(got[3]) != "summarize" {
			t.Fatalf("input = %v, want [summary, new question, new answer, prompt]", texts(got))
		}
	})

	t.Run("fits the window untouched", func(t *testing.T) {
		a, sess := build(t, 100_000, []message.Message{
			textMsg(message.User, big), textMsg(message.Assistant, big),
		}, -1)
		got, err := a.summarizerInput(context.Background(), sess, prompt)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3 (nothing trimmed)", len(got))
		}
	})

	t.Run("trims the oldest and keeps the summary head", func(t *testing.T) {
		// Window 3000 → budget 2700: summary + note + two big messages fit, three do not.
		a, sess := build(t, 3000, []message.Message{
			textMsg(message.User, "PREVIOUS SUMMARY"),
			textMsg(message.User, big),
			textMsg(message.Assistant, big),
			textMsg(message.User, big),
			textMsg(message.Assistant, "latest"),
		}, 0)
		got, err := a.summarizerInput(context.Background(), sess, prompt)
		if err != nil {
			t.Fatal(err)
		}
		if firstText(got[0]) != "PREVIOUS SUMMARY" {
			t.Errorf("head = %q, want the previous summary", firstText(got[0]))
		}
		if !strings.Contains(firstText(got[1]), "earlier messages were omitted") {
			t.Errorf("second message = %q, want the omission note", firstText(got[1]))
		}
		if firstText(got[len(got)-2]) != "latest" || firstText(got[len(got)-1]) != "summarize" {
			t.Errorf("tail = %v, want [..., latest, summarize]", texts(got))
		}
		if est := message.EstimateTokens(got, nil, message.BytesPerTokenEta); est >= 2700 {
			t.Errorf("trimmed estimate %d still reaches the 2700-token budget", est)
		}
	})

	t.Run("reported usage calibrates an undercounting estimate", func(t *testing.T) {
		// Window 10000 → budget 9000. The local estimate (~3050) fits, but the
		// provider reported 9500 for the same history: the real input would
		// not, so the trim has to run in real-token terms.
		a, sess := build(t, 10_000, []message.Message{
			textMsg(message.User, big),
			textMsg(message.Assistant, big),
			textMsg(message.User, big),
			textMsg(message.Assistant, "latest"),
		}, -1)
		_, _ = a.sessions.Save(context.Background(), session.Session{ID: sess, PromptTokens: 9000, CompletionTokens: 500})
		got, err := a.summarizerInput(context.Background(), sess, prompt)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(firstText(got[0]), "earlier messages were omitted") {
			t.Fatalf("input = %v, want the oldest messages dropped", texts(got))
		}
		if firstText(got[len(got)-2]) != "latest" {
			t.Errorf("tail = %v, want [..., latest, summarize]", texts(got))
		}
	})

	t.Run("keeps the turn's prompt through the cut", func(t *testing.T) {
		toolUse2 := message.Message{Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: big},
			message.ToolCall{ID: "call-2", Name: "bash", Input: "{}", Finished: true},
		}}
		toolResult2 := message.Message{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call-2", Content: big},
		}}
		// The schema envelope is a later user message, but synthetic: it is
		// not the prompt.
		envelope := message.Message{Role: message.User, Synthetic: true, Parts: []message.ContentPart{
			message.TextContent{Text: "SCHEMA ENVELOPE"},
		}}
		turn := []message.Message{
			textMsg(message.User, "TASK PROMPT"),
			toolUse, toolResult,
			envelope,
			toolUse2, toolResult2,
			textMsg(message.Assistant, "latest"),
		}
		cases := []struct {
			name    string
			history []message.Message
			head    []string
		}{
			{"no previous summary", turn, nil},
			{"after the previous summary", append([]message.Message{textMsg(message.User, "PREVIOUS SUMMARY")}, turn...), []string{"PREVIOUS SUMMARY"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				summaryAt := -1
				if len(tc.head) > 0 {
					summaryAt = 0
				}
				// Window 3000 → budget 2700: the first tool pair has to go,
				// and the prompt, oldest of the turn, with it unless kept.
				a, sess := build(t, 3000, tc.history, summaryAt)
				got, err := a.summarizerInput(context.Background(), sess, prompt)
				if err != nil {
					t.Fatal(err)
				}
				h := len(tc.head)
				if len(got) < h+3 || (h > 0 && firstText(got[0]) != tc.head[0]) {
					t.Fatalf("input = %v, want it to start with %v", texts(got), tc.head)
				}
				if firstText(got[h]) != "TASK PROMPT" {
					t.Errorf("input = %v, want the turn's prompt kept after the head", texts(got))
				}
				// The note right after the prompt means the cut passed it.
				if !strings.Contains(firstText(got[h+1]), "earlier messages were omitted") {
					t.Errorf("input = %v, want the omission note after the prompt", texts(got))
				}
				for i, m := range got {
					if m.Role == message.Tool && (i == 0 || got[i-1].Role != message.Assistant) {
						t.Fatalf("tool result at %d without its tool call; input roles %v", i, roles(got))
					}
				}
				if firstText(got[len(got)-2]) != "latest" || firstText(got[len(got)-1]) != "summarize" {
					t.Errorf("tail = %v, want [..., latest, summarize]", texts(got))
				}
				if est := message.EstimateTokens(got, nil, message.BytesPerTokenEta); est >= 2700 {
					t.Errorf("trimmed estimate %d still reaches the 2700-token budget", est)
				}
			})
		}
	})

	t.Run("never leaves an orphaned tool result at the cut", func(t *testing.T) {
		// Window 2000 → budget 1800. Dropping the first message leaves the
		// tool_use + tool_result pair (~2000) plus the tail, still over; the
		// next drop takes the tool_use, so its tool_result must go too. The
		// first message is the turn's prompt, but it alone is over half the
		// budget, so it is not kept.
		a, sess := build(t, 2000, []message.Message{
			textMsg(message.User, big),
			toolUse,
			toolResult,
			textMsg(message.Assistant, "latest"),
		}, -1)
		got, err := a.summarizerInput(context.Background(), sess, prompt)
		if err != nil {
			t.Fatal(err)
		}
		for i, m := range got {
			if m.Role != message.Tool {
				continue
			}
			if i == 0 || got[i-1].Role != message.Assistant {
				t.Fatalf("tool result at %d without its tool call; input roles %v", i, roles(got))
			}
		}
		if len(got) != 3 || firstText(got[1]) != "latest" {
			t.Errorf("input = %v, want [note, latest, summarize]", texts(got))
		}
	})

	t.Run("empty session", func(t *testing.T) {
		a, sess := build(t, 1000, nil, -1)
		if _, err := a.summarizerInput(context.Background(), sess, prompt); !errors.Is(err, errNoMessagesToSummarize) {
			t.Fatalf("err = %v, want errNoMessagesToSummarize", err)
		}
	})
}

func TestTrimSummarizerInput_KeepsPairsTogether(t *testing.T) {
	big := strings.Repeat("z", 3900)
	msgs := []message.Message{
		textMsg(message.User, big),
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: big}}},
		{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "c", Content: big}}},
		{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "d", Content: big}}},
		textMsg(message.User, "tail"),
	}
	kept, dropped, _ := trimSummarizerInput(msgs, false, -1, 0, 2500)
	if dropped != 4 {
		t.Fatalf("dropped = %d, want 4 (the assistant and both its tool results go together)", dropped)
	}
	if len(kept) != 2 || firstText(kept[1]) != "tail" {
		t.Fatalf("kept = %v, want [note, tail]", texts(kept))
	}
}

// A kept prompt must not cost the recent history or the budget: when keeping
// it leaves nothing after the cut, or the input still over budget, the trim
// falls back to treating it like any other message. EstimateTokens counts
// (bytes + 100 per part) / 4, so the sizes below are 1300, 1000, 1000, 30,
// 1400 and 1500 tokens.
func TestTrimSummarizerInput_KeptPromptDoesNotCrowdOutHistory(t *testing.T) {
	sized := func(role message.MessageRole, tokens int) message.Message {
		text := strings.Repeat("p", tokens*4-100)
		if role == message.Tool {
			return message.Message{Role: role, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "c", Content: text}}}
		}
		return textMsg(role, text)
	}
	toolUse := message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "c", Name: "bash", Input: `{"cmd":"ls -la"}`}}}

	cases := []struct {
		name      string
		msgs      []message.Message
		wantRoles []message.MessageRole
	}{
		{
			name:      "keeping the prompt would orphan the last tool pair",
			msgs:      []message.Message{sized(message.User, 1300), sized(message.Assistant, 1000), sized(message.Tool, 1000), toolUse, sized(message.Tool, 1400)},
			wantRoles: []message.MessageRole{message.User, message.Assistant, message.Tool},
		},
		{
			name:      "keeping the prompt would leave the input over budget",
			msgs:      []message.Message{sized(message.User, 1300), sized(message.Assistant, 1000), sized(message.Tool, 1000), sized(message.Assistant, 1500)},
			wantRoles: []message.MessageRole{message.User, message.Assistant},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const fixed, budget = 27, 2700
			kept, dropped, estimated := trimSummarizerInput(tc.msgs, false, 0, fixed, budget)
			if dropped == 0 {
				t.Fatalf("nothing dropped, want a trim")
			}
			if estimated >= budget {
				t.Errorf("estimated = %d, want under the budget %d", estimated, budget)
			}
			if got := roles(kept); !slices.Equal(got, tc.wantRoles) {
				t.Fatalf("kept roles = %v (%v), want %v: the note, then the recent history", got, texts(kept), tc.wantRoles)
			}
			if !strings.Contains(firstText(kept[0]), "omitted") {
				t.Errorf("kept[0] = %q, want the omission note (the prompt is not kept)", firstText(kept[0]))
			}
		})
	}
}

func TestLikelyContextOverflow(t *testing.T) {
	cases := []struct {
		name      string
		estimated int64
		window    int64
		want      bool
	}{
		{"zero window", 1_000_000, 0, false},
		{"well below", 500_000, 1_000_000, false},
		{"just below 90%", 899_999, 1_000_000, false},
		{"exactly 90%", 900_000, 1_000_000, true},
		{"near the window", 997_000, 1_000_000, true},
		{"over the window", 1_200_000, 1_000_000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := likelyContextOverflow(tc.estimated, tc.window); got != tc.want {
				t.Errorf("likelyContextOverflow(%d, %d) = %v, want %v", tc.estimated, tc.window, got, tc.want)
			}
		})
	}
}

func texts(msgs []message.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = string(m.Role) + ":" + truncateForTest(firstText(m))
	}
	return out
}

func roles(msgs []message.Message) []message.MessageRole {
	out := make([]message.MessageRole, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role
	}
	return out
}

func truncateForTest(s string) string {
	if len(s) > 20 {
		return s[:20] + "…"
	}
	return s
}

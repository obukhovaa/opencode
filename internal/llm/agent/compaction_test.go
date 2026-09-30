package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
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

	mu       sync.Mutex
	log      *[]string
	requests [][]message.Message
}

func (p *compactionProvider) StreamResponse(_ context.Context, msgs []message.Message, _ []tools.BaseTool) <-chan provider.ProviderEvent {
	p.mu.Lock()
	*p.log = append(*p.log, p.name)
	p.requests = append(p.requests, append([]message.Message(nil), msgs...))
	p.mu.Unlock()
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
		msgs      []message.Message
		wantCount int64
		wantHit   bool
	}{
		{
			name:      "undercounting estimate is floored by reported usage plus tail",
			window:    1000,
			estimate:  100,
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
			estimate:  100,
			sess:      session.Session{PromptTokens: 600, CompletionTokens: 300},
			msgs:      withAssistant,
			wantCount: 900 + tailTokens,
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
			created, err := a.messages.Create(ctx, sess, message.CreateMessageParams{Role: m.Role, Parts: m.Parts})
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

	t.Run("never leaves an orphaned tool result at the cut", func(t *testing.T) {
		// Window 2000 → budget 1800. Dropping the first message leaves the
		// tool_use + tool_result pair (~2000) plus the tail, still over; the
		// next drop takes the tool_use, so its tool_result must go too.
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
	kept, dropped, _ := trimSummarizerInput(msgs, false, 0, 2500)
	if dropped != 4 {
		t.Fatalf("dropped = %d, want 4 (the assistant and both its tool results go together)", dropped)
	}
	if len(kept) != 2 || firstText(kept[1]) != "tail" {
		t.Fatalf("kept = %v, want [note, tail]", texts(kept))
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

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/config"
	opencodedb "github.com/opencode-ai/opencode/internal/db"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
)

// compactStubAgent implements just the agent.Service methods cmdCompact touches
// (the rest panic via the embedded nil interface, which the command never
// calls). SummarizeSync runs an injected callback to simulate the real
// compaction side effects (token reset + summary message).
type compactStubAgent struct {
	agentpkg.Service
	busy           bool
	model          models.Model
	summarizeCalls int
	summarizeErr   error
	onSummarize    func(sessionID string)
}

func (a *compactStubAgent) IsSessionBusy(string) bool { return a.busy }
func (a *compactStubAgent) Model() models.Model       { return a.model }
func (a *compactStubAgent) SummarizeSync(_ context.Context, sessionID string) error {
	a.summarizeCalls++
	if a.summarizeErr != nil {
		return a.summarizeErr
	}
	if a.onSummarize != nil {
		a.onSummarize(sessionID)
	}
	return nil
}

// stubMessages returns canned messages by ID (for reading the retained summary).
type stubMessages struct {
	message.Service
	byID map[string]message.Message
}

func (m *stubMessages) Get(_ context.Context, id string) (message.Message, error) {
	msg, ok := m.byID[id]
	if !ok {
		return message.Message{}, errors.New("not found")
	}
	return msg, nil
}

func newCompactTestService(t *testing.T, ag *compactStubAgent, msgs message.Service) (*Service, session.Service, *stubAdapter) {
	t.Helper()
	svc, conn := newOrchestratorForTest(t)
	sessSvc := session.NewService(opencodedb.New(conn), "proj")
	svc.app = &app.App{
		Sessions:         sessSvc,
		Messages:         msgs,
		PrimaryAgents:    map[config.AgentName]agentpkg.Service{config.AgentCoder: ag},
		PrimaryAgentKeys: []config.AgentName{config.AgentCoder},
		ActiveAgentIdx:   0,
	}
	ad := newStubAdapter("slack", "default")
	svc.adapters[adapterKey("slack", "default")] = ad
	if _, err := svc.store.UpsertBinding(context.Background(), store.Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "S1",
	}); err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	return svc, sessSvc, ad
}

func TestCmdCompactTwoPhase(t *testing.T) {
	ctx := context.Background()
	msgs := &stubMessages{byID: map[string]message.Message{
		"msg-summary": {Parts: []message.ContentPart{message.TextContent{Text: "Condensed history of the chat."}}},
	}}
	ag := &compactStubAgent{model: models.Model{ContextWindow: 100000}}

	svc, sessSvc, ad := newCompactTestService(t, ag, msgs)

	// Seed a non-trivial "before" context on the bound session.
	sess, _ := sessSvc.Get(ctx, "S1")
	sess.PromptTokens = 8000
	sess.CompletionTokens = 2000
	if _, err := sessSvc.Save(ctx, sess); err != nil {
		t.Fatalf("seed tokens: %v", err)
	}

	// Simulate real compaction: prompt tokens reset, completion = summary size.
	ag.onSummarize = func(sessionID string) {
		s, _ := sessSvc.Get(ctx, sessionID)
		s.PromptTokens = 0
		s.CompletionTokens = 500
		s.SummaryMessageID = "msg-summary"
		_, _ = sessSvc.Save(ctx, s)
	}

	ack := svc.cmdCompact(ctx, bridge.Inbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"},
	})
	if ack == nil || !strings.Contains(ack.Text, "Compaction started") {
		t.Fatalf("ack = %+v, want a 'Compaction started' message", ack)
	}
	if !strings.Contains(ack.Text, "10.0k tokens") {
		t.Errorf("ack should report the current context size (10.0k tokens): %q", ack.Text)
	}

	// The completion reply is posted asynchronously via the adapter.
	done := waitForSend(t, ad, 2*time.Second)
	if !strings.Contains(done, "Compaction complete") {
		t.Fatalf("completion reply = %q, want 'Compaction complete'", done)
	}
	if !strings.Contains(done, "10.0k tokens") || !strings.Contains(done, "500 tokens") {
		t.Errorf("completion reply should show before→after (10.0k → 500): %q", done)
	}
	if !strings.Contains(done, "Condensed history of the chat.") {
		t.Errorf("completion reply should include the retained summary: %q", done)
	}
	if ag.summarizeCalls != 1 {
		t.Errorf("SummarizeSync called %d times, want 1", ag.summarizeCalls)
	}
}

func TestCmdCompactBusyDoesNotCompact(t *testing.T) {
	ctx := context.Background()
	ag := &compactStubAgent{busy: true}
	svc, _, ad := newCompactTestService(t, ag, &stubMessages{})

	reply := svc.cmdCompact(ctx, bridge.Inbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"},
	})
	if reply == nil || !strings.Contains(reply.Text, "mid-run") {
		t.Fatalf("busy reply = %+v, want a 'mid-run' message", reply)
	}
	// Give any (erroneously) spawned goroutine a chance to run.
	time.Sleep(100 * time.Millisecond)
	if ag.summarizeCalls != 0 {
		t.Errorf("SummarizeSync called %d times on a busy session, want 0", ag.summarizeCalls)
	}
	if n := len(ad.Sends()); n != 0 {
		t.Errorf("busy compact sent %d async messages, want 0", n)
	}
}

func TestCmdCompactRegistered(t *testing.T) {
	s := &Service{}
	if s.ChatCommands()["compact"] == nil {
		t.Fatal("/compact is not registered in ChatCommands")
	}
}

// serviceWithModel builds a bare Service whose ActiveAgent reports the given
// context window — enough to exercise compactSummaryLimit / retainedSummary
// without a store or DB.
func serviceWithModel(cw int64, msgs message.Service) *Service {
	return &Service{app: &app.App{
		Messages:         msgs,
		PrimaryAgents:    map[config.AgentName]agentpkg.Service{config.AgentCoder: &compactStubAgent{model: models.Model{ContextWindow: cw}}},
		PrimaryAgentKeys: []config.AgentName{config.AgentCoder},
	}}
}

func TestCompactSummaryLimit(t *testing.T) {
	cases := []struct {
		name string
		cw   int64
		want int
	}{
		{"no context window → floor", 0, 2000},
		{"2% below floor → floor", 50_000, 2000},   // 0.02*50k = 1000
		{"2% equals floor", 100_000, 2000},         // 0.02*100k = 2000
		{"2% above floor scales", 500_000, 10_000}, // 0.02*500k = 10000
		{"large window", 1_000_000, 20_000},        // 0.02*1M = 20000
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := serviceWithModel(c.cw, nil).compactSummaryLimit(); got != c.want {
				t.Errorf("compactSummaryLimit(cw=%d) = %d, want %d", c.cw, got, c.want)
			}
		})
	}
}

func TestRetainedSummaryTruncatesToDynamicLimit(t *testing.T) {
	long := strings.Repeat("x", 30000)
	msgs := &stubMessages{byID: map[string]message.Message{
		"m": {Parts: []message.ContentPart{message.TextContent{Text: long}}},
	}}
	// ContextWindow 500k → limit 10000 runes; truncateRunes appends "…".
	out := serviceWithModel(500_000, msgs).retainedSummary(context.Background(), "m")
	if runes := len([]rune(out)); runes != 10_001 || !strings.HasSuffix(out, "…") {
		t.Errorf("retainedSummary length = %d (ends with … = %v), want 10001 with ellipsis",
			runes, strings.HasSuffix(out, "…"))
	}

	// A summary shorter than the floor is returned untouched.
	shortMsgs := &stubMessages{byID: map[string]message.Message{
		"m": {Parts: []message.ContentPart{message.TextContent{Text: "brief"}}},
	}}
	if out := serviceWithModel(0, shortMsgs).retainedSummary(context.Background(), "m"); out != "brief" {
		t.Errorf("short summary = %q, want %q (no truncation)", out, "brief")
	}
}

// waitForSend polls the stub adapter until it has recorded at least one
// outbound, returning that outbound's text. Fails the test on timeout.
func waitForSend(t *testing.T, ad *stubAdapter, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sends := ad.Sends(); len(sends) > 0 {
			return sends[0].Text
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the async completion reply")
	return ""
}

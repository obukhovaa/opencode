package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/heartbeat"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
)

// heartbeatStubAgent answers every Run with a fixed reply and records the
// prompts it was given. busy makes IsSessionBusy report the session as
// running.
type heartbeatStubAgent struct {
	agentpkg.Service

	mu      sync.Mutex
	reply   string
	runErr  error
	prompts []string
	busy    bool
	// delay holds the run open so a progress card has time to post.
	delay time.Duration
	// noHeartbeatTool hides the heartbeat tool from ResolvedTools.
	noHeartbeatTool bool
}

func (a *heartbeatStubAgent) ResolvedTools() ([]tools.BaseTool, bool) {
	if a.noHeartbeatTool {
		return nil, true
	}
	return []tools.BaseTool{tools.NewHeartbeatTool(nil)}, true
}

func (a *heartbeatStubAgent) Run(_ context.Context, _, content string, _ int, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	a.mu.Lock()
	a.prompts = append(a.prompts, content)
	reply, runErr, delay := a.reply, a.runErr, a.delay
	a.mu.Unlock()
	time.Sleep(delay)
	ch := make(chan agentpkg.AgentEvent, 1)
	if runErr != nil {
		ch <- agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeError, Error: runErr}
	} else {
		ch <- agentpkg.AgentEvent{
			Type:    agentpkg.AgentEventTypeResponse,
			Message: message.Message{Parts: []message.ContentPart{message.TextContent{Text: reply}}},
		}
	}
	close(ch)
	return ch, nil
}

func (a *heartbeatStubAgent) IsSessionBusy(string) bool { return a.busy }

func (a *heartbeatStubAgent) Model() models.Model { return models.Model{ID: "stub-model"} }

func (a *heartbeatStubAgent) runs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.prompts...)
}

// stubSessions reports a fixed message count for every session.
type stubSessions struct {
	session.Service
	messages int64
}

func (s *stubSessions) Get(_ context.Context, id string) (session.Session, error) {
	return session.Session{ID: id, MessageCount: s.messages}, nil
}

type heartbeatHarness struct {
	svc   *Service
	ad    *stubAdapter
	ag    *heartbeatStubAgent
	disp  *sessionDispatch
	peer  bridge.PeerRef
	dir   string
	clock time.Time
}

func newHeartbeatHarness(t *testing.T) *heartbeatHarness {
	t.Helper()
	ag := &heartbeatStubAgent{reply: heartbeat.SilentToken}
	svc, conn := newOrchestratorForTest(t)
	// The in-memory fixture database is only visible through the
	// connection that created it; progress-card flushes query it from
	// another goroutine, so keep the pool on that one connection.
	conn.SetMaxOpenConns(1)
	svc.ctx = context.Background()
	ad := newStubAdapter("slack", "default")
	svc.adapters[adapterKey("slack", "default")] = ad
	if _, err := svc.store.UpsertBinding(context.Background(), store.Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "S1",
	}); err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	svc.app = &app.App{
		Messages:         &stubMessageSvc{},
		Sessions:         &stubSessions{messages: 3},
		PrimaryAgents:    map[config.AgentName]agentpkg.Service{config.AgentCoder: ag},
		PrimaryAgentKeys: []config.AgentName{config.AgentCoder},
	}
	svc.heartbeat = &heartbeatState{reminded: map[string]bool{}, agents: map[string]agentpkg.Service{}}

	h := &heartbeatHarness{
		svc:   svc,
		ad:    ad,
		ag:    ag,
		peer:  bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"},
		dir:   t.TempDir(),
		clock: time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC), // a Thursday
	}
	// Register a bare dispatcher so the scheduler's pushes can be drained
	// by hand, without the background run loop.
	h.disp = newBareDispatch(svc, "S1")
	svc.dispatchers["S1"] = h.disp

	prevNow, prevDir := heartbeatNow, heartbeatWorkDir
	heartbeatNow = func() time.Time { return h.clock }
	heartbeatWorkDir = func() string { return h.dir }
	t.Cleanup(func() { heartbeatNow, heartbeatWorkDir = prevNow, prevDir })
	return h
}

func (h *heartbeatHarness) command(t *testing.T, args string) string {
	t.Helper()
	reply := h.svc.cmdHeartbeat(context.Background(), bridge.Inbound{Peer: h.peer, Command: "heartbeat", CommandArgs: args})
	return reply.Text
}

func (h *heartbeatHarness) writeAgenda(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.dir, heartbeat.DefaultAgendaFile), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (h *heartbeatHarness) row(t *testing.T) store.Heartbeat {
	t.Helper()
	r, err := h.svc.store.GetHeartbeat(context.Background(), "proj", "slack", "default", "D1")
	if err != nil {
		t.Fatalf("GetHeartbeat: %v", err)
	}
	return r
}

// drain runs every queued inbound through handleInbound, as the
// dispatcher's run loop would.
func (h *heartbeatHarness) drain() int {
	n := 0
	for {
		select {
		case in := <-h.disp.inbound:
			h.disp.handleInbound(context.Background(), in)
			n++
		default:
			return n
		}
	}
}

func (h *heartbeatHarness) sentTexts() []string {
	var out []string
	for _, s := range h.ad.Sends() {
		out = append(out, s.Text)
	}
	return out
}

func TestHeartbeatCommandConfiguresAndReports(t *testing.T) {
	h := newHeartbeatHarness(t)

	if got := h.command(t, ""); !strings.Contains(got, "not set up") {
		t.Fatalf("status of a fresh binding: %q", got)
	}
	got := h.command(t, "on every 30m hours 05-21 days weekdays")
	for _, want := range []string{"is on", "Every: 30m", "05:00-21:00 UTC", "weekdays", "Next beat: 2026-10-01 10:00Z", "skipped until the agenda"} {
		if !strings.Contains(got, want) {
			t.Errorf("reply missing %q:\n%s", want, got)
		}
	}
	r := h.row(t)
	if r.State != heartbeat.StateOn || r.Every != 30*time.Minute || !r.WeekdaysOnly {
		t.Fatalf("row not stored: %+v", r)
	}

	if got := h.command(t, "model no-such-model"); !strings.Contains(got, "unknown model") {
		t.Fatalf("unknown model reply: %q", got)
	}
	if h.row(t).Model != "" {
		t.Fatal("an invalid model was stored")
	}

	// Not the exact grammar: left to the agent (nil reply) when it has the
	// tool, refused with the grammar when it does not.
	if reply := h.svc.cmdHeartbeat(context.Background(), bridge.Inbound{Peer: h.peer, Command: "heartbeat", CommandArgs: "every 2m"}); !reply.IsEmpty() {
		t.Fatalf("natural language should go to the agent, got %q", reply.Text)
	}
	h.ag.noHeartbeatTool = true
	if got := h.command(t, "every 2m"); !strings.Contains(got, "between 10m and 24h") || !strings.Contains(got, "usage:") || !strings.Contains(got, `"heartbeat" tool`) {
		t.Fatalf("reply without the tool: %q", got)
	}
	h.ag.noHeartbeatTool = false
	if h.row(t).Every != 30*time.Minute {
		t.Fatal("an unparsed command changed the stored interval")
	}

	h.command(t, "off")
	if r := h.row(t); r.State != heartbeat.StateOff || !r.NextBeatAt.IsZero() {
		t.Fatalf("off: %+v", r)
	}
}

func TestHeartbeatCommandOutsideDaemonMode(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.svc.heartbeat = nil
	if got := h.command(t, "on"); !strings.Contains(got, "only available") {
		t.Fatalf("reply: %q", got)
	}
	if _, err := h.svc.store.GetHeartbeat(context.Background(), "proj", "slack", "default", "D1"); err == nil {
		t.Fatal("a row was stored outside daemon mode")
	}
}

func TestHeartbeatTickSilentAckPostsNothing(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.writeAgenda(t, "- Check open merge requests\n")
	h.command(t, "on")

	h.svc.heartbeatTick(context.Background())
	if n := h.drain(); n != 1 {
		t.Fatalf("queued %d beats, want 1", n)
	}
	prompts := h.ag.runs()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "HEARTBEAT.md") || !strings.Contains(prompts[0], heartbeat.SilentToken) {
		t.Fatalf("heartbeat prompt: %q", prompts)
	}
	if sent := h.sentTexts(); len(sent) != 0 {
		t.Fatalf("a silent beat posted %q", sent)
	}
	r := h.row(t)
	if r.LastStatus != heartbeat.OutcomeSilent || !r.NextBeatAt.Equal(h.clock.Add(time.Hour)) {
		t.Fatalf("after a silent beat: status %q next %s", r.LastStatus, r.NextBeatAt)
	}
	if h.disp.heartbeatQueued.Load() || h.disp.quiet.Load() {
		t.Fatal("dispatcher flags not cleared after the beat")
	}

	// Not due yet: nothing fires.
	h.svc.heartbeatTick(context.Background())
	if n := h.drain(); n != 0 {
		t.Fatalf("a beat fired before it was due (%d)", n)
	}
}

func TestHeartbeatTickReportGetsHeader(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.writeAgenda(t, "- Check CI\n")
	h.ag.reply = "CI on main is red: two tests fail."
	h.command(t, "on")

	h.svc.heartbeatTick(context.Background())
	h.drain()
	sent := h.sentTexts()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "💓 Heartbeat 10:00Z") || !strings.Contains(sent[0], "CI on main is red") {
		t.Fatalf("report: %q", sent)
	}
	if h.row(t).LastStatus != heartbeat.OutcomeOK {
		t.Fatalf("status %q", h.row(t).LastStatus)
	}
}

func TestHeartbeatTickErrorPostsOneLine(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.writeAgenda(t, "- Check CI\n")
	h.ag.runErr = context.DeadlineExceeded
	h.command(t, "on")

	h.svc.heartbeatTick(context.Background())
	h.drain()
	sent := h.sentTexts()
	if len(sent) != 1 || !strings.Contains(sent[0], "failed: context deadline exceeded") {
		t.Fatalf("failure line: %q", sent)
	}
	if r := h.row(t); r.LastStatus != heartbeat.OutcomeError || r.LastError == "" {
		t.Fatalf("row: %+v", r)
	}
}

func TestHeartbeatTickEmptyAgendaSkipsWithoutModelCall(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.command(t, "on")

	h.svc.heartbeatTick(context.Background())
	if n := h.drain(); n != 0 || len(h.ag.runs()) != 0 {
		t.Fatalf("an empty agenda started a run (%d queued)", n)
	}
	r := h.row(t)
	if r.LastStatus != heartbeat.OutcomeSkipped || !strings.Contains(r.LastError, "does not exist") {
		t.Fatalf("row: %+v", r)
	}
	if !r.NextBeatAt.Equal(h.clock.Add(time.Hour)) {
		t.Fatalf("a skipped beat must still advance the schedule: %s", r.NextBeatAt)
	}
}

func TestHeartbeatTickDefersWhileBusyAndCoalesces(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.writeAgenda(t, "- Check CI\n")
	h.command(t, "on")

	h.ag.busy = true
	h.svc.heartbeatTick(context.Background())
	if n := h.drain(); n != 0 {
		t.Fatal("a beat was queued while the session was busy")
	}
	if !h.row(t).NextBeatAt.Equal(h.clock) {
		t.Fatal("a deferred beat must keep its due time")
	}

	// Three hours later, idle: exactly one catch-up beat, then the next slot.
	h.ag.busy = false
	h.clock = h.clock.Add(3*time.Hour + 12*time.Minute)
	h.svc.heartbeatTick(context.Background())
	h.svc.heartbeatTick(context.Background())
	if n := h.drain(); n != 1 {
		t.Fatalf("missed beats should coalesce into one, got %d", n)
	}
	if want := time.Date(2026, 10, 1, 14, 0, 0, 0, time.UTC); !h.row(t).NextBeatAt.Equal(want) {
		t.Fatalf("next beat %s, want %s", h.row(t).NextBeatAt, want)
	}
}

func TestHeartbeatNowDoesNotMoveTheSchedule(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.writeAgenda(t, "- Check CI\n")
	h.command(t, "on every 2h")
	next := h.row(t).NextBeatAt

	if got := h.command(t, "now"); !strings.Contains(got, "Beat now: queued") {
		t.Fatalf("reply: %q", got)
	}
	if h.drain() != 1 {
		t.Fatal("/heartbeat now did not queue a beat")
	}
	if !h.row(t).NextBeatAt.Equal(next) {
		t.Fatal("/heartbeat now moved the schedule")
	}
}

func TestHeartbeatReminderOncePerWeek(t *testing.T) {
	h := newHeartbeatHarness(t)
	ctx := context.Background()

	h.svc.remindNewIdentities(ctx, h.clock)
	sent := h.sentTexts()
	if len(sent) != 1 || !strings.Contains(sent[0], "/heartbeat on") || !strings.Contains(sent[0], "/heartbeat off") {
		t.Fatalf("reminder: %q", sent)
	}

	// A restart the same day: no second reminder.
	h.svc.heartbeat.reminded = map[string]bool{}
	h.svc.remindNewIdentities(ctx, h.clock.Add(6*time.Hour))
	if n := len(h.sentTexts()); n != 1 {
		t.Fatalf("reminded again within a week (%d posts)", n)
	}

	// A week later: reminded again.
	h.svc.heartbeat.reminded = map[string]bool{}
	h.svc.remindNewIdentities(ctx, h.clock.Add(heartbeat.ReminderInterval))
	if n := len(h.sentTexts()); n != 2 {
		t.Fatalf("not reminded after a week (%d posts)", n)
	}

	// Declined: never again.
	h.command(t, "off")
	h.svc.heartbeat.reminded = map[string]bool{}
	h.svc.remindNewIdentities(ctx, h.clock.Add(3*heartbeat.ReminderInterval))
	if n := len(h.sentTexts()); n != 2 {
		t.Fatalf("reminded after /heartbeat off (%d posts)", n)
	}
}

func TestHeartbeatReminderSkipsEmptySessions(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.svc.app.Sessions = &stubSessions{messages: 0}
	h.svc.remindNewIdentities(context.Background(), h.clock)
	if sent := h.sentTexts(); len(sent) != 0 {
		t.Fatalf("reminded a never-used binding: %q", sent)
	}
}

func TestHeartbeatTurnPostsNoProgressCard(t *testing.T) {
	h := newHeartbeatHarness(t)
	ed := newEditorStubAdapter("slack", "default")
	h.svc.adapters[adapterKey("slack", "default")] = ed
	h.svc.cfg.ToolUpdatesEnabled = true
	h.writeAgenda(t, "- Check CI\n")
	h.ag.reply = "done"
	h.ag.delay = 200 * time.Millisecond

	// Control: a human turn at compact verbosity gets a progress card.
	h.disp.handleInbound(context.Background(), testInbound("hello"))
	if len(ed.Posts()) != 1 {
		t.Fatalf("control run posted %d progress cards, want 1", len(ed.Posts()))
	}

	h.command(t, "on")
	h.svc.heartbeatTick(context.Background())
	h.drain()
	if n := len(ed.Posts()); n != 1 {
		t.Fatalf("the heartbeat turn posted a progress card (%d cards in total)", n)
	}
	sends := ed.Sends()
	if last := sends[len(sends)-1].Text; !strings.HasPrefix(last, "💓 Heartbeat") {
		t.Fatalf("last post %q, want the heartbeat report", last)
	}
}

func TestHeartbeatNaturalLanguageGoesToTheAgent(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.command(t, "on")

	h.svc.dispatchInbound(context.Background(), bridge.Inbound{
		Peer: h.peer,
		Text: "/heartbeat every half hour on weekdays, 7 to 23 Oslo time, and watch my merge requests",
	})
	if sent := h.sentTexts(); len(sent) != 0 {
		t.Fatalf("the bridge answered itself: %q", sent)
	}
	select {
	case in := <-h.disp.inbound:
		for _, want := range []string{
			"every half hour on weekdays, 7 to 23 Oslo time, and watch my merge requests",
			"heartbeat tool", "UTC", "agenda file", "Current heartbeat:", "Heartbeat is on",
		} {
			if !strings.Contains(in.Text, want) {
				t.Errorf("agent prompt missing %q:\n%s", want, in.Text)
			}
		}
		if in.Heartbeat != nil {
			t.Error("a /heartbeat request is a human turn, not a heartbeat turn")
		}
	default:
		t.Fatal("nothing was queued for the agent")
	}
}

func TestHeartbeatConfigurerAppliesToTheSessionsChat(t *testing.T) {
	h := newHeartbeatHarness(t)
	ctx := context.Background()

	cmd, err := heartbeat.ParseCommand("on every 2h hours 07:30-21 days weekdays")
	if err != nil {
		t.Fatal(err)
	}
	text, err := h.svc.ApplyHeartbeat(ctx, "S1", cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "is on") || !strings.Contains(text, "07:30-21:00 UTC") {
		t.Fatalf("status: %q", text)
	}
	r := h.row(t)
	if r.State != heartbeat.StateOn || r.Every != 2*time.Hour || !r.WeekdaysOnly {
		t.Fatalf("row: %+v", r)
	}
	if want := time.Date(2026, 10, 1, 11, 30, 0, 0, time.UTC); !r.NextBeatAt.Equal(want) {
		t.Fatalf("next beat %s, want %s (slots start at the window start)", r.NextBeatAt, want)
	}

	if _, err := h.svc.ApplyHeartbeat(ctx, "no-such-session", cmd); err == nil {
		t.Fatal("an unbound session must be refused")
	}
	h.svc.heartbeat = nil
	if _, err := h.svc.HeartbeatStatus(ctx, "S1"); !errors.Is(err, tools.ErrHeartbeatUnavailable) {
		t.Fatalf("outside daemon mode: %v", err)
	}
}

// TestHeartbeatLatePartEventStaysQuiet: runParts can lag behind the run,
// so a heartbeat run's tool event may be handled after the dispatcher's
// quiet flag is cleared. The run's guard still marks it quiet.
func TestHeartbeatLatePartEventStaysQuiet(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.svc.cfg.ToolUpdatesEnabled = true
	if _, err := h.svc.SetToolVerbosity("full"); err != nil {
		t.Fatal(err)
	}
	ev := pubsub.Event[message.PartEvent]{Payload: message.PartEvent{
		SessionID: "S1",
		Part:      message.ToolCall{ID: "toolu_01late", Name: "bash", Input: `{"command":"ls"}`, Finished: true},
	}}

	quiet := newRunTextGuard(0)
	quiet.quiet = true
	h.disp.handlePartEvent(ev, quiet)
	// Control: the same event from a human run posts its card. Cards are
	// sent asynchronously, so wait for the control's and then make sure
	// it is the only one.
	h.disp.handlePartEvent(ev, newRunTextGuard(0))
	waitFor(t, "tool card", func() bool { return len(h.ad.Sends()) >= 1 })
	time.Sleep(200 * time.Millisecond)
	if n := len(h.ad.Sends()); n != 1 {
		t.Fatalf("%d tool cards posted, want only the human run's", n)
	}
}

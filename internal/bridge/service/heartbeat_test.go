package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
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
	"github.com/opencode-ai/opencode/internal/permission"
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
	// toolsLoading makes ResolvedTools report a tool set still loading.
	toolsLoading bool
	// busyRuns makes that many Run calls fail with ErrSessionBusy first,
	// as when another actor holds the session.
	busyRuns int
	// holdBeats holds every heartbeat run open until Cancel, which ends
	// it with cancelled: one of the ways the agent ends a cancelled run
	// (heldBeatCancels), streamCancelled when unset.
	holdBeats bool
	cancelled agentpkg.AgentEvent
	held      chan struct{}
	cancels   int
}

// The agent ends a run cancelled while the model streams with an error,
// and one cancelled while a tool runs with a response that carries the
// text the model wrote before the tool call and a canceled finish.
var (
	streamCancelled = agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeError, Error: agentpkg.ErrRequestCancelled}
	toolCancelled   = agentpkg.AgentEvent{
		Type: agentpkg.AgentEventTypeResponse,
		Message: message.Message{Parts: []message.ContentPart{
			message.TextContent{Text: "Let me check CI."},
			message.Finish{Reason: message.FinishReasonCanceled},
		}},
	}
	heldBeatCancels = []struct {
		name string
		ev   agentpkg.AgentEvent
	}{
		{"while the model streams", streamCancelled},
		{"while a tool runs", toolCancelled},
	}
)

func (a *heartbeatStubAgent) ResolvedTools() ([]tools.BaseTool, bool) {
	if a.toolsLoading {
		return nil, false
	}
	if a.noHeartbeatTool {
		return nil, true
	}
	return []tools.BaseTool{tools.NewHeartbeatTool(nil)}, true
}

func (a *heartbeatStubAgent) Run(_ context.Context, _, content string, _ int, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	a.mu.Lock()
	if a.busyRuns > 0 {
		a.busyRuns--
		a.mu.Unlock()
		return nil, agentpkg.ErrSessionBusy
	}
	a.prompts = append(a.prompts, content)
	reply, runErr, delay := a.reply, a.runErr, a.delay
	var held chan struct{}
	cancelled := a.cancelled
	if cancelled.Type == "" {
		cancelled = streamCancelled
	}
	if a.holdBeats && strings.HasPrefix(content, "[Heartbeat") {
		a.held = make(chan struct{})
		held = a.held
	}
	a.mu.Unlock()
	ch := make(chan agentpkg.AgentEvent, 1)
	if held != nil {
		go func() {
			<-held
			ch <- cancelled
			close(ch)
		}()
		return ch, nil
	}
	time.Sleep(delay)
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

// Cancel ends a held heartbeat run.
func (a *heartbeatStubAgent) Cancel(string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancels++
	if a.held != nil {
		close(a.held)
		a.held = nil
	}
}

func (a *heartbeatStubAgent) Model() models.Model { return models.Model{ID: "stub-model"} }

func (a *heartbeatStubAgent) runs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.prompts...)
}

// stubSessions reports a fixed message count for every session. root,
// when set, is every session's root, as for a flow step's session.
type stubSessions struct {
	session.Service
	messages int64
	root     string
}

func (s *stubSessions) Get(_ context.Context, id string) (session.Session, error) {
	root := s.root
	if root == "" {
		root = id
	}
	return session.Session{ID: id, RootSessionID: root, MessageCount: s.messages}, nil
}

// directStubAdapter reports D-prefixed peers as direct messages. It does
// not look for a thread itself, so the bridge's own thread check is what
// keeps a "D…|<ts>" peer out of the reminder.
type directStubAdapter struct{ *stubAdapter }

func (a directStubAdapter) IsDirectPeer(_ context.Context, peerID string) bool {
	return strings.HasPrefix(peerID, "D")
}

// mediatedStubAdapter is a direct-message adapter whose inbound is
// mediated by the orchestrator: it holds no identity lock.
type mediatedStubAdapter struct{ directStubAdapter }

func (mediatedStubAdapter) InboundActive() bool { return false }

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
	svc.adapters[adapterKey("slack", "default")] = directStubAdapter{ad}
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
	return h.commandFor(t, h.peer, args)
}

func (h *heartbeatHarness) commandFor(t *testing.T, peer bridge.PeerRef, args string) string {
	t.Helper()
	reply := h.svc.cmdHeartbeat(context.Background(), bridge.Inbound{Peer: peer, Command: "heartbeat", CommandArgs: args})
	return reply.Text
}

// useRunLoop replaces the bare dispatcher with a real one, run loop and
// parts loop included.
func (h *heartbeatHarness) useRunLoop(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.svc.ctx, h.svc.cancel = ctx, cancel
	delete(h.svc.dispatchers, "S1")
	h.disp = h.svc.dispatcherFor("S1")
	// Release a held beat before Stop waits for the dispatcher (cleanups
	// run last-registered first).
	t.Cleanup(func() { h.ag.Cancel("") })
}

func (h *heartbeatHarness) binding(t *testing.T) store.Binding {
	t.Helper()
	b, err := h.svc.store.GetBinding(context.Background(), "proj", "slack", "default", "D1")
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	return b
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
	if h.disp.heartbeatQueued.Load() {
		t.Fatal("the heartbeat claim was not released after the beat")
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

// TestHeartbeatReminderNeedsTheHeartbeatTool: the reminder advertises
// describing the heartbeat in your own words, which only the heartbeat
// tool can carry out, so an agent without it gets no reminder. A tool set
// still loading defers the decision to a later tick instead of using it up.
func TestHeartbeatReminderNeedsTheHeartbeatTool(t *testing.T) {
	h := newHeartbeatHarness(t)
	ctx := context.Background()

	h.ag.noHeartbeatTool = true
	h.svc.remindNewIdentities(ctx, h.clock)
	if sent := h.sentTexts(); len(sent) != 0 {
		t.Fatalf("reminded although the agent has no heartbeat tool: %q", sent)
	}

	h.ag.noHeartbeatTool = false
	h.ag.toolsLoading = true
	h.svc.remindNewIdentities(ctx, h.clock)
	if sent := h.sentTexts(); len(sent) != 0 {
		t.Fatalf("reminded while the tool set was still loading: %q", sent)
	}

	// Once the tools are known (same process, so the pass was not used up).
	h.ag.toolsLoading = false
	h.svc.remindNewIdentities(ctx, h.clock)
	if sent := h.sentTexts(); len(sent) != 1 {
		t.Fatalf("no reminder once the heartbeat tool is known: %q", sent)
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

// TestHeartbeatReminderOnlyReachesDirectMessagesThisDaemonServes: the
// bridge's project can be shared with flow and pool pods, so an identity's
// bindings include channels, threads, other pods' review threads and the
// external relay. Only a top-level DM of an adapter this process owns,
// whose session is a daemon conversation, gets the reminder.
func TestHeartbeatReminderOnlyReachesDirectMessagesThisDaemonServes(t *testing.T) {
	direct := func(ad *stubAdapter) bridge.Adapter { return directStubAdapter{ad} }
	tests := []struct {
		name    string
		channel string
		peer    string
		adapter func(*stubAdapter) bridge.Adapter
		prep    func(*heartbeatHarness)
		want    bool
	}{
		{name: "direct message", peer: "D7", want: true},
		{name: "thread in a direct message", peer: "D7|1700000000.000100"},
		{name: "channel", peer: "C7"},
		{name: "channel thread", peer: "C7|1700000000.000100"},
		{name: "adapter that cannot tell a direct message", peer: "D7",
			adapter: func(ad *stubAdapter) bridge.Adapter { return ad }},
		{name: "mediated adapter", peer: "D7",
			adapter: func(ad *stubAdapter) bridge.Adapter { return mediatedStubAdapter{directStubAdapter{ad}} }},
		{name: "external relay", channel: "external", peer: "D7"},
		{name: "flow step session", peer: "D7",
			prep: func(h *heartbeatHarness) { h.svc.app.Sessions = &stubSessions{messages: 3, root: "flow-root"} }},
		{name: "interactive step session", peer: "D7",
			prep: func(h *heartbeatHarness) {
				h.svc.app.Permissions = permission.NewPermissionService()
				h.svc.app.Permissions.MarkInteractiveSession("S1")
			}},
		{name: "switched off", peer: "D7",
			prep: func(h *heartbeatHarness) {
				off := false
				h.svc.cfg.HeartbeatReminder = &off
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHeartbeatHarness(t)
			// Only the adapter under test: the harness's own D1 binding
			// would be reminded too.
			delete(h.svc.adapters, adapterKey("slack", "default"))
			channel := tc.channel
			if channel == "" {
				channel = "slack"
			}
			ad := newStubAdapter(channel, "bot")
			wrap := direct
			if tc.adapter != nil {
				wrap = tc.adapter
			}
			h.svc.adapters[adapterKey(channel, "bot")] = wrap(ad)
			if _, err := h.svc.store.UpsertBinding(context.Background(), store.Binding{
				ProjectID: "proj", Channel: channel, IdentityID: "bot", PeerID: tc.peer, SessionID: "S1",
			}); err != nil {
				t.Fatalf("UpsertBinding: %v", err)
			}
			if tc.prep != nil {
				tc.prep(h)
			}

			h.svc.remindNewIdentities(context.Background(), h.clock)
			if got := len(ad.Sends()) == 1; got != tc.want {
				t.Fatalf("reminded = %v (%d posts), want %v", got, len(ad.Sends()), tc.want)
			}
		})
	}
}

// TestHeartbeatSchedulerSkipsMediatedAdapters: a mediated adapter takes no
// identity lock, so several processes can hold it; none schedules beats.
func TestHeartbeatSchedulerSkipsMediatedAdapters(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.writeAgenda(t, "- Check CI\n")
	h.command(t, "on")
	h.svc.adapters[adapterKey("slack", "default")] = mediatedStubAdapter{directStubAdapter{h.ad}}

	h.svc.heartbeatTick(context.Background())
	if n := h.drain(); n != 0 {
		t.Fatalf("a mediated adapter's binding got %d beats", n)
	}
	if !h.row(t).NextBeatAt.Equal(h.clock) {
		t.Fatal("the schedule of a binding this process does not serve moved")
	}
	if got := h.command(t, "on"); !strings.Contains(got, "Scheduled beats do not run for this chat") {
		t.Fatalf("/heartbeat on does not say beats will not run: %q", got)
	}
}

// TestHeartbeatNowRunsBehindTheCurrentTurn: /heartbeat now sent mid-turn,
// and the heartbeat tool's now (always called inside the session's own
// turn), queue the beat behind that turn instead of refusing it.
func TestHeartbeatNowRunsBehindTheCurrentTurn(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.writeAgenda(t, "- Check CI\n")
	h.ag.busy = true

	if got := h.command(t, "now"); !strings.Contains(got, "Beat now: queued; it runs when the current turn ends.") {
		t.Fatalf("reply: %q", got)
	}
	if n := h.drain(); n != 1 || len(h.ag.runs()) != 1 {
		t.Fatalf("/heartbeat now while busy: %d queued, %d runs", n, len(h.ag.runs()))
	}

	text, err := h.svc.ApplyHeartbeat(context.Background(), "S1", heartbeat.Command{Now: true})
	if err != nil || !strings.Contains(text, "queued; it runs when the current turn ends") {
		t.Fatalf("tool now: %q, %v", text, err)
	}
	if n := h.drain(); n != 1 {
		t.Fatalf("tool now queued %d beats", n)
	}
}

// TestHeartbeatManualBeatWaitsOutAnotherActor: a manual beat that finds
// the session held by another actor waits like a message. Handing it to
// the scheduler would drop it, since only an "on" heartbeat is scheduled.
func TestHeartbeatManualBeatWaitsOutAnotherActor(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.writeAgenda(t, "- Check CI\n")
	h.ag.busyRuns = 2

	h.command(t, "now")
	h.drain()
	if n := len(h.ag.runs()); n != 1 {
		t.Fatalf("the manual beat ran %d times, want 1", n)
	}
	if r := h.row(t); r.LastStatus != heartbeat.OutcomeSilent {
		t.Fatalf("outcome %q", r.LastStatus)
	}
}

// TestHeartbeatLateBeatOutsideActiveHoursMovesOn: a beat that only gets
// its turn outside the active hours fires at the next allowed slot, not
// at 03:00.
func TestHeartbeatLateBeatOutsideActiveHoursMovesOn(t *testing.T) {
	tests := []struct {
		name string
		prep func(*heartbeatHarness)
	}{
		{"catch-up after an overnight restart", func(h *heartbeatHarness) {}},
		{"held back by a busy session", func(h *heartbeatHarness) {
			h.ag.busy = true
			h.svc.heartbeatTick(context.Background())
			h.ag.busy = false
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHeartbeatHarness(t)
			h.writeAgenda(t, "- Check CI\n")
			h.command(t, "on hours 07-23")
			tc.prep(h)

			h.clock = time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)
			h.svc.heartbeatTick(context.Background())
			if n := h.drain(); n != 0 {
				t.Fatalf("a late beat fired at 03:00Z outside hours 07-23 (%d)", n)
			}
			if want := time.Date(2026, 10, 2, 7, 0, 0, 0, time.UTC); !h.row(t).NextBeatAt.Equal(want) {
				t.Fatalf("next beat %s, want %s", h.row(t).NextBeatAt, want)
			}

			h.clock = time.Date(2026, 10, 2, 7, 0, 0, 0, time.UTC)
			h.svc.heartbeatTick(context.Background())
			if n := h.drain(); n != 1 {
				t.Fatalf("the 07:00Z beat did not fire (%d)", n)
			}
		})
	}
}

// TestHeartbeatMessagePreemptsBeat: a human message that arrives while a
// beat runs cancels the beat, so the human is answered right away. The
// cancelled beat posts nothing, no failure line, and its slot is not
// handed back. The beat may be cancelled while the model streams or while
// a tool runs.
func TestHeartbeatMessagePreemptsBeat(t *testing.T) {
	for _, tc := range heldBeatCancels {
		t.Run(tc.name, func(t *testing.T) {
			h := newHeartbeatHarness(t)
			h.useRunLoop(t)
			h.writeAgenda(t, "- Check CI\n")
			h.ag.holdBeats = true
			h.ag.cancelled = tc.ev
			h.ag.reply = "hello back"
			h.command(t, "on")
			ctx := context.Background()

			h.svc.heartbeatTick(ctx)
			next := h.row(t).NextBeatAt
			waitFor(t, "the beat's run", func() bool { return len(h.ag.runs()) == 1 })

			h.svc.dispatchInbound(ctx, bridge.Inbound{Peer: h.peer, Text: "are you there?"})
			waitFor(t, "the reply to the message", func() bool { return len(h.sentTexts()) > 0 })
			time.Sleep(50 * time.Millisecond)
			if sent := h.sentTexts(); len(sent) != 1 || sent[0] != "hello back" {
				t.Fatalf("posts %q, want only the reply to the message", sent)
			}
			r := h.row(t)
			if r.LastStatus != heartbeat.OutcomeSkipped || r.LastError != heartbeatPreemptedReason {
				t.Fatalf("beat outcome %q (%q)", r.LastStatus, r.LastError)
			}
			if !r.NextBeatAt.Equal(next) {
				t.Fatalf("next beat moved from %s to %s", next, r.NextBeatAt)
			}
		})
	}
}

// TestHeartbeatAbortedBeatPostsNothing: a beat cancelled by /abort, with
// no message waiting, posts nothing and is recorded as skipped, not as a
// failure.
func TestHeartbeatAbortedBeatPostsNothing(t *testing.T) {
	for _, tc := range heldBeatCancels {
		t.Run(tc.name, func(t *testing.T) {
			h := newHeartbeatHarness(t)
			h.writeAgenda(t, "- Check CI\n")
			h.ag.holdBeats = true
			h.ag.cancelled = tc.ev
			h.command(t, "on")
			ctx := context.Background()

			h.svc.heartbeatTick(ctx)
			in := <-h.disp.inbound
			done := make(chan struct{})
			go func() {
				defer close(done)
				h.disp.handleInbound(ctx, in)
			}()
			waitFor(t, "the beat's run", func() bool { return len(h.ag.runs()) == 1 })

			h.ag.Cancel("S1")
			<-done
			if sent := h.sentTexts(); len(sent) != 0 {
				t.Fatalf("an aborted beat posted %q", sent)
			}
			if r := h.row(t); r.LastStatus != heartbeat.OutcomeSkipped || r.LastError != "cancelled" {
				t.Fatalf("beat outcome %q (%q)", r.LastStatus, r.LastError)
			}
		})
	}
}

// TestHeartbeatLeavesTheHumanRunsLatePartsAlone: the human run before a
// beat can still have part events in flight when the beat starts. They
// are that run's, and reach the chat.
func TestHeartbeatLeavesTheHumanRunsLatePartsAlone(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.svc.cfg.ToolUpdatesEnabled = true
	if _, err := h.svc.SetToolVerbosity("full"); err != nil {
		t.Fatal(err)
	}
	h.writeAgenda(t, "- Check CI\n")
	h.ag.holdBeats = true
	t.Cleanup(func() { h.ag.Cancel("") })
	h.command(t, "on")
	ctx := context.Background()

	h.svc.heartbeatTick(ctx)
	in := <-h.disp.inbound
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.disp.handleInbound(ctx, in)
	}()
	waitFor(t, "the beat's run", func() bool { return len(h.ag.runs()) == 1 })

	human := newRunTextGuard(0)
	h.disp.handlePartEvent(pubsub.Event[message.PartEvent]{Payload: message.PartEvent{
		SessionID: "S1",
		Part:      message.ToolCall{ID: "toolu_01human", Name: "bash", Input: `{"command":"ls"}`, Finished: true},
	}}, human)
	waitFor(t, "the human run's tool card", func() bool { return len(h.ad.Sends()) == 1 })

	h.ag.Cancel("S1")
	<-done
}

// TestHeartbeatAgendaSkipNoticeOnce: a beat that starts being skipped for
// a missing agenda (a redeploy wiped the working directory) says so once,
// not on every skip.
func TestHeartbeatAgendaSkipNoticeOnce(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.command(t, "on")
	ctx := context.Background()

	h.svc.heartbeatTick(ctx)
	sent := h.sentTexts()
	want := "💓 Heartbeat 10:00Z skipped: HEARTBEAT.md does not exist. Tell me what to check (/heartbeat <request>) or run /heartbeat off."
	if len(sent) != 1 || sent[0] != want {
		t.Fatalf("notice %q, want %q", sent, want)
	}
	h.clock = h.clock.Add(time.Hour)
	h.svc.heartbeatTick(ctx)
	if n := len(h.sentTexts()); n != 1 {
		t.Fatalf("a second skip posted again (%d posts)", n)
	}

	// The agenda comes back, a beat runs, then the agenda goes again.
	h.writeAgenda(t, "- Check CI\n")
	h.clock = h.clock.Add(time.Hour)
	h.svc.heartbeatTick(ctx)
	h.drain()
	if err := os.Remove(filepath.Join(h.dir, heartbeat.DefaultAgendaFile)); err != nil {
		t.Fatal(err)
	}
	h.clock = h.clock.Add(time.Hour)
	h.svc.heartbeatTick(ctx)
	if n := len(h.sentTexts()); n != 2 {
		t.Fatalf("no notice when skipping started again (%d posts)", n)
	}
}

// TestHeartbeatFireQueuesOneBeatAcrossRacingCallers: the scheduler and
// /heartbeat now can reach fireHeartbeat at once; one beat is queued.
func TestHeartbeatFireQueuesOneBeatAcrossRacingCallers(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.writeAgenda(t, "- Check CI\n")
	h.command(t, "on")
	row, b := h.row(t), h.binding(t)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			h.svc.fireHeartbeat(context.Background(), row, b, h.clock, false)
		}()
	}
	close(start)
	wg.Wait()
	if n := h.drain(); n != 1 {
		t.Fatalf("%d beats queued, want 1", n)
	}
}

// TestHeartbeatStaleRowNeverUndoesAChange: the scheduler works from a row
// read earlier; a /heartbeat change made since then wins.
func TestHeartbeatStaleRowNeverUndoesAChange(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.writeAgenda(t, "- Check CI\n")
	ctx := context.Background()

	t.Run("off after the tick read the row", func(t *testing.T) {
		h.command(t, "on")
		stale := h.row(t)
		h.command(t, "off")
		h.svc.fireHeartbeat(ctx, stale, h.binding(t), h.clock, false)
		if r := h.row(t); r.State != heartbeat.StateOff {
			t.Fatalf("a stale write turned the heartbeat back %s", r.State)
		}
		if n := h.drain(); n != 0 {
			t.Fatalf("%d beats queued after off", n)
		}
	})

	t.Run("a new interval while the beat lost its session", func(t *testing.T) {
		h.command(t, "on")
		h.ag.busyRuns = 1
		h.svc.heartbeatTick(ctx)
		h.command(t, "every 3h")
		want := h.row(t).NextBeatAt
		h.drain()
		if got := h.row(t).NextBeatAt; !got.Equal(want) {
			t.Fatalf("the deferred beat moved the new schedule from %s to %s", want, got)
		}
	})
}

// TestHeartbeatSkipsAnInteractiveStepsSession: an interactive flow step
// owns its session; a beat would run the default agent on it.
func TestHeartbeatSkipsAnInteractiveStepsSession(t *testing.T) {
	h := newHeartbeatHarness(t)
	h.writeAgenda(t, "- Check CI\n")
	h.command(t, "on")
	h.svc.app.Permissions = permission.NewPermissionService()
	h.svc.app.Permissions.MarkInteractiveSession("S1")

	h.svc.heartbeatTick(context.Background())
	if n := h.drain(); n != 0 {
		t.Fatalf("%d beats queued on an interactive step's session", n)
	}
	if !h.row(t).NextBeatAt.Equal(h.clock) {
		t.Fatal("a held-back beat must keep its due time")
	}
	if got := h.command(t, "now"); !strings.Contains(got, "an interactive flow step owns this session") {
		t.Fatalf("reply: %q", got)
	}
	if n := h.drain(); n != 0 {
		t.Fatalf("/heartbeat now queued %d beats on an interactive step's session", n)
	}
}

// TestHeartbeatOneBeatPerSessionPerSlot: a session bound to two chats gets
// one beat per slot, and its report reaches both chats.
func TestHeartbeatOneBeatPerSessionPerSlot(t *testing.T) {
	h := newHeartbeatHarness(t)
	ctx := context.Background()
	if _, err := h.svc.store.UpsertBinding(ctx, store.Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D2", SessionID: "S1",
	}); err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	other := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D2"}
	h.writeAgenda(t, "- Check CI\n")
	h.ag.reply = "CI on main is red."
	h.command(t, "on")
	h.commandFor(t, other, "on")

	h.svc.heartbeatTick(ctx)
	beats := h.drain()
	h.svc.heartbeatTick(ctx)
	beats += h.drain()
	if beats != 1 {
		t.Fatalf("%d beats in one slot, want 1", beats)
	}
	for _, peer := range []string{"D1", "D2"} {
		r, err := h.svc.store.GetHeartbeat(ctx, "proj", "slack", "default", peer)
		if err != nil {
			t.Fatal(err)
		}
		if want := h.clock.Add(time.Hour); !r.NextBeatAt.Equal(want) {
			t.Errorf("%s next beat %s, want %s", peer, r.NextBeatAt, want)
		}
	}
	if sent := h.sentTexts(); len(sent) != 2 {
		t.Fatalf("report posts %q, want one per chat", sent)
	}
}

// TestHeartbeatModelNeedsAConfiguredProvider: /heartbeat model accepts a
// supported model only when its provider is configured, as the agent
// factory needs it to build the beat's agent.
func TestHeartbeatModelNeedsAConfiguredProvider(t *testing.T) {
	dir := t.TempDir()
	body := `{"providers": {"anthropic": {"apiKey": "test-key"}, "openai": {"disabled": true}}}`
	if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	config.Reset()
	if _, err := config.Load(dir, false); err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	t.Cleanup(config.Reset)
	h := newHeartbeatHarness(t)
	h.command(t, "every 2h")

	if got := h.command(t, "model "+string(firstModelOf(models.ProviderOpenAI))); !strings.Contains(got, "not configured") {
		t.Fatalf("a model of a disabled provider: %q", got)
	}
	if h.row(t).Model != "" {
		t.Fatal("a model of a disabled provider was stored")
	}
	id := firstModelOf(models.ProviderAnthropic)
	h.command(t, "model "+string(id))
	if got := h.row(t).Model; got != string(id) {
		t.Fatalf("model %q stored, want %q", got, id)
	}
}

func firstModelOf(p models.ModelProvider) models.ModelID {
	var ids []string
	for id, m := range models.SupportedModels {
		if m.Provider == p {
			ids = append(ids, string(id))
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		panic("no model for provider " + string(p))
	}
	return models.ModelID(ids[0])
}

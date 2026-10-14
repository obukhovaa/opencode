package service

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/config"
	opencodedb "github.com/opencode-ai/opencode/internal/db"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/task"
)

// cancelStubAgent records Cancel calls; a cancelled session stops being busy.
type cancelStubAgent struct {
	agentpkg.Service
	mu        sync.Mutex
	busy      map[string]bool
	cancelled []string
	// onCancel runs inside Cancel, before the session stops being busy.
	onCancel func(id string)
}

func (a *cancelStubAgent) IsSessionBusy(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.busy[id]
}

func (a *cancelStubAgent) Cancel(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.onCancel != nil {
		a.onCancel(id)
	}
	a.cancelled = append(a.cancelled, id)
	a.busy[id] = false
}

func newCancelTestService(t *testing.T, ag *cancelStubAgent) (*Service, *sessionDispatch) {
	t.Helper()
	svc, conn := newOrchestratorForTest(t)
	a := app.NewForTest(context.Background(), ag)
	a.Sessions = session.NewService(opencodedb.New(conn), "proj")
	a.PrimaryAgents = map[config.AgentName]agentpkg.Service{config.AgentCoder: ag, "other": ag}
	a.PrimaryAgentKeys = []config.AgentName{config.AgentCoder, "other"}
	svc.app = a
	if _, err := svc.store.UpsertBinding(context.Background(), store.Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "S1",
	}); err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	// A dispatcher without its goroutines: queued inbound stays put.
	d := &sessionDispatch{svc: svc, sessionID: "S1", inbound: make(chan bridge.Inbound, dispatchInboundCap)}
	svc.dispatchMu.Lock()
	svc.dispatchers["S1"] = d
	svc.dispatchMu.Unlock()
	return svc, d
}

func peerD1() bridge.PeerRef {
	return bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}
}

func TestStopAndCancelAreAbortAliases(t *testing.T) {
	svc := &Service{}
	cmds := svc.ChatCommands()
	for _, name := range []string{"abort", "stop", "cancel"} {
		if cmds[name] == nil {
			t.Errorf("/%s is not a chat command", name)
		}
	}
	var help strings.Builder
	for _, e := range svc.helpEntriesForChannel("slack") {
		help.WriteString(e.Cmd + "\n")
	}
	for _, name := range []string{"/stop", "/cancel"} {
		if !strings.Contains(help.String(), name) {
			t.Errorf("help does not list %s", name)
		}
	}
}

func TestStopCancelsRunAndDropsQueuedMessages(t *testing.T) {
	ag := &cancelStubAgent{busy: map[string]bool{"S1": true}}
	svc, d := newCancelTestService(t, ag)
	d.inbound <- bridge.Inbound{Peer: peerD1(), Text: "queued one"}
	d.inbound <- bridge.Inbound{Peer: peerD1(), Text: "queued two"}
	d.overflow = []bridge.Inbound{{Peer: peerD1(), Text: "overflowed"}}
	d.inbound <- bridge.Inbound{Peer: peerD1(), Heartbeat: &bridge.HeartbeatTurn{}}
	d.heartbeatQueued.Store(true)
	svc.app.EnqueueForTest("S1", app.QueuedMessage{Text: "tui queued"})
	epoch := d.abortEpoch.Load()

	reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerD1(), Command: "stop"})
	if reply == nil || !strings.Contains(reply.Text, "Aborted in-flight run") {
		t.Fatalf("reply = %+v, want an abort confirmation", reply)
	}
	if !strings.Contains(reply.Text, "Dropped 4 queued message(s)") {
		t.Errorf("reply %q should report the 4 dropped user messages", reply.Text)
	}
	if len(ag.cancelled) != 1 || ag.cancelled[0] != "S1" {
		t.Errorf("Cancel calls = %v, want [S1]", ag.cancelled)
	}
	if n := len(d.inbound) + len(d.overflow); n != 0 {
		t.Errorf("dispatcher still holds %d queued inbound", n)
	}
	if n := svc.app.QueueLen("S1"); n != 0 {
		t.Errorf("app queue length = %d, want 0", n)
	}
	if d.heartbeatQueued.Load() {
		t.Error("dropped heartbeat turn kept its claim")
	}
	if d.abortEpoch.Load() == epoch {
		t.Error("abort epoch not bumped: a message waiting out the busy session would still run")
	}
}

func TestStopOnIdleSessionReportsNothingToAbort(t *testing.T) {
	ag := &cancelStubAgent{busy: map[string]bool{}}
	svc, _ := newCancelTestService(t, ag)
	reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerD1(), Command: "stop"})
	if reply == nil || !strings.Contains(reply.Text, "not running anything") {
		t.Fatalf("reply = %+v", reply)
	}
	if len(ag.cancelled) != 0 {
		t.Errorf("Cancel called on an idle session: %v", ag.cancelled)
	}
}

func TestResetCancelsTheRun(t *testing.T) {
	ag := &cancelStubAgent{busy: map[string]bool{"S1": true}}
	svc, d := newCancelTestService(t, ag)
	d.inbound <- bridge.Inbound{Peer: peerD1(), Text: "queued"}

	reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerD1(), Command: "reset"})
	if reply == nil || !strings.Contains(reply.Text, "Cancelled the run") {
		t.Fatalf("reply = %+v, want the reset to say the run was cancelled", reply)
	}
	if len(ag.cancelled) != 1 || ag.cancelled[0] != "S1" {
		t.Errorf("Cancel calls = %v, want [S1]", ag.cancelled)
	}
	if _, err := svc.store.GetBinding(context.Background(), "proj", "slack", "default", "D1"); err == nil {
		t.Error("binding still present after /reset")
	}
}

func TestAgentSwitchRefusedWhileBusy(t *testing.T) {
	ag := &cancelStubAgent{busy: map[string]bool{"S1": true}}
	svc, _ := newCancelTestService(t, ag)

	reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerD1(), Command: "agent", CommandArgs: "other"})
	if reply == nil || !strings.Contains(reply.Text, "/stop") {
		t.Fatalf("reply = %+v, want a refusal pointing at /stop", reply)
	}
	if got := svc.app.ActiveAgentName(); got != config.AgentCoder {
		t.Errorf("active agent switched to %q while busy", got)
	}

	ag.busy["S1"] = false
	reply = svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerD1(), Command: "agent", CommandArgs: "other"})
	if reply == nil || !strings.Contains(reply.Text, "Active agent: other") {
		t.Fatalf("reply = %+v, want the switch once idle", reply)
	}
}

func peerD2() bridge.PeerRef {
	return bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D2"}
}

func TestResetCancelsOnlyWhenTheLastPeerLeavesAndNotAFlowStep(t *testing.T) {
	tests := []struct {
		name         string
		secondPeer   bool
		interactive  bool
		wantCancels  int
		wantBound    bool
		wantCancelTx bool
	}{
		{name: "sole peer cancels", wantCancels: 1, wantCancelTx: true},
		{name: "other peer still bound keeps the run", secondPeer: true, wantCancels: 0, wantBound: true},
		{name: "interactive flow step keeps the run", interactive: true, wantCancels: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := &cancelStubAgent{busy: map[string]bool{"S1": true}}
			svc, _ := newCancelTestService(t, ag)
			svc.app.Permissions = permission.NewPermissionService()
			if tt.secondPeer {
				if _, err := svc.store.UpsertBinding(context.Background(), store.Binding{
					ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D2", SessionID: "S1",
				}); err != nil {
					t.Fatalf("UpsertBinding: %v", err)
				}
			}
			if tt.interactive {
				svc.app.Permissions.MarkInteractiveSession("S1")
			}

			reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerD1(), Command: "reset"})
			if reply == nil {
				t.Fatal("no reply")
			}
			if got := strings.Contains(reply.Text, "Cancelled the run"); got != tt.wantCancelTx {
				t.Errorf("reply %q: mentions cancel = %v, want %v", reply.Text, got, tt.wantCancelTx)
			}
			if len(ag.cancelled) != tt.wantCancels {
				t.Errorf("Cancel calls = %v, want %d", ag.cancelled, tt.wantCancels)
			}
			if !ag.IsSessionBusy("S1") != (tt.wantCancels > 0) {
				t.Errorf("busy = %v after /reset", ag.IsSessionBusy("S1"))
			}
			if _, err := svc.store.GetBinding(context.Background(), "proj", "slack", "default", "D1"); err == nil {
				t.Error("resetting peer is still bound")
			}
			_, err := svc.store.GetBinding(context.Background(), "proj", "slack", "default", "D2")
			if bound := err == nil; bound != tt.wantBound {
				t.Errorf("other peer bound = %v, want %v", bound, tt.wantBound)
			}
		})
	}
}

func TestStopOrdersQueueDrainBeforeCancel(t *testing.T) {
	ag := &cancelStubAgent{busy: map[string]bool{"S1": true}}
	svc, d := newCancelTestService(t, ag)
	d.inbound <- bridge.Inbound{Peer: peerD1(), Text: "queued"}
	svc.app.EnqueueForTest("S1", app.QueuedMessage{Text: "tui queued"})
	epoch := d.abortEpoch.Load()

	var queued, appQueued int
	var epochBumped bool
	ag.onCancel = func(string) {
		queued = len(d.inbound)
		appQueued = svc.app.QueueLen("S1")
		epochBumped = d.abortEpoch.Load() != epoch
	}
	svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerD1(), Command: "stop"})

	if len(ag.cancelled) != 1 {
		t.Fatalf("Cancel calls = %v, want 1", ag.cancelled)
	}
	if queued != 0 || appQueued != 0 {
		t.Errorf("at Cancel time dispatcher held %d and app queue %d messages, want both drained", queued, appQueued)
	}
	if !epochBumped {
		t.Error("abort epoch not bumped before Cancel")
	}
}

func TestStopKillsBackgroundTasksOfTheSession(t *testing.T) {
	task.ResetGlobalRegistry()
	reg := task.NewRegistry(func() string { return t.TempDir() })
	task.SetGlobalRegistry(reg)
	t.Cleanup(task.ResetGlobalRegistry)

	mk := func(kind task.Kind, session string) *task.Task {
		tk := &task.Task{ID: task.NewTaskID(kind), SessionID: session, Kind: kind}
		if err := reg.Register(tk); err != nil {
			t.Fatalf("Register: %v", err)
		}
		return tk
	}
	bash, mon, other := mk(task.KindBash, "S1"), mk(task.KindMonitor, "S1"), mk(task.KindBash, "S2")

	ag := &cancelStubAgent{busy: map[string]bool{}}
	svc, _ := newCancelTestService(t, ag)
	reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerD1(), Command: "stop"})
	if reply == nil || !strings.Contains(reply.Text, "Stopped 2 background task(s)") {
		t.Fatalf("reply = %+v, want the stopped tasks reported", reply)
	}
	for _, tk := range []*task.Task{bash, mon} {
		if tk.State() != task.StateKilled {
			t.Errorf("task %s state = %v, want killed", tk.ID, tk.State())
		}
	}
	if other.State() != task.StateRunning {
		t.Errorf("another session's task state = %v, want running", other.State())
	}
}

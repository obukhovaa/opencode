package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/config"
	opencodedb "github.com/opencode-ai/opencode/internal/db"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/session"
)

// twoAgentApp wires coder (active) and hivemind as primary agents.
func twoAgentApp(sessions session.Service, coder, hive agentpkg.Service) *app.App {
	return &app.App{
		Messages:         &stubMessageSvc{},
		Sessions:         sessions,
		PrimaryAgents:    map[config.AgentName]agentpkg.Service{config.AgentCoder: coder, config.AgentHivemind: hive},
		PrimaryAgentKeys: []config.AgentName{config.AgentCoder, config.AgentHivemind},
	}
}

func TestHandleInboundRunsOnTheSessionsAgent(t *testing.T) {
	coder, hive := &busyRetryStubAgent{}, &busyRetryStubAgent{}
	svc, _ := newDispatchTestSvc(t, coder)
	sessions := &stubSessions{agentName: string(config.AgentHivemind)}
	svc.app = twoAgentApp(sessions, coder, hive)

	newBareDispatch(svc, "S1").handleInbound(context.Background(), testInbound("hello"))

	if hive.runCallCount() != 1 || coder.runCallCount() != 0 {
		t.Fatalf("runs: hivemind=%d coder=%d, want 1 and 0 (the session's agent, not the active one)",
			hive.runCallCount(), coder.runCallCount())
	}
	if sessions.setCalls != 0 {
		t.Errorf("stamped %d times, want 0: the session already carries hivemind", sessions.setCalls)
	}
}

func TestHandleInboundStampsTheAgentItUsed(t *testing.T) {
	coder, hive := &busyRetryStubAgent{}, &busyRetryStubAgent{}
	svc, _ := newDispatchTestSvc(t, coder)
	sessions := &stubSessions{}
	svc.app = twoAgentApp(sessions, coder, hive)

	newBareDispatch(svc, "S1").handleInbound(context.Background(), testInbound("hello"))

	if coder.runCallCount() != 1 {
		t.Fatalf("coder ran %d times, want 1 (no stored agent: the active one)", coder.runCallCount())
	}
	if sessions.agentName != string(config.AgentCoder) {
		t.Errorf("stamped agent = %q, want coder", sessions.agentName)
	}
}

func TestHandleInboundUnknownStoredAgentFallsBackAndRestamps(t *testing.T) {
	coder, hive := &busyRetryStubAgent{}, &busyRetryStubAgent{}
	svc, _ := newDispatchTestSvc(t, coder)
	sessions := &stubSessions{agentName: "retired-agent"}
	svc.app = twoAgentApp(sessions, coder, hive)

	newBareDispatch(svc, "S1").handleInbound(context.Background(), testInbound("hello"))

	if coder.runCallCount() != 1 || hive.runCallCount() != 0 {
		t.Fatalf("runs: coder=%d hivemind=%d, want the active coder to run once", coder.runCallCount(), hive.runCallCount())
	}
	if sessions.agentName != string(config.AgentCoder) {
		t.Errorf("stamped agent = %q, want coder", sessions.agentName)
	}
}

type failingStampSessions struct{ stubSessions }

func (f *failingStampSessions) SetAgentNameIf(context.Context, string, string, string) (bool, error) {
	return false, errors.New("db down")
}

func TestHandleInboundRunsWhenStampingFails(t *testing.T) {
	coder, hive := &busyRetryStubAgent{}, &busyRetryStubAgent{}
	svc, _ := newDispatchTestSvc(t, coder)
	svc.app = twoAgentApp(&failingStampSessions{}, coder, hive)

	newBareDispatch(svc, "S1").handleInbound(context.Background(), testInbound("hello"))

	if coder.runCallCount() != 1 {
		t.Fatalf("coder ran %d times, want 1: a failed stamp must not fail the run", coder.runCallCount())
	}
}

func TestHeartbeatRunsOnTheSessionsAgent(t *testing.T) {
	h := newHeartbeatHarness(t)
	hive := &heartbeatStubAgent{reply: "hello from hivemind"}
	sessions := h.svc.app.Sessions.(*stubSessions)
	sessions.agentName = string(config.AgentHivemind)
	h.svc.app.PrimaryAgents[config.AgentHivemind] = hive
	h.svc.app.PrimaryAgentKeys = append(h.svc.app.PrimaryAgentKeys, config.AgentHivemind)

	h.writeAgenda(t, "- Check open merge requests\n")
	h.command(t, "on")
	h.svc.heartbeatTick(context.Background())
	if n := h.drain(); n != 1 {
		t.Fatalf("queued %d beats, want 1", n)
	}

	if got := len(hive.runs()); got != 1 {
		t.Errorf("hivemind ran %d beats, want 1", got)
	}
	if got := len(h.ag.runs()); got != 0 {
		t.Errorf("the active coder ran %d beats, want 0", got)
	}
}

func TestCmdAgentPersistsOnTheBoundSession(t *testing.T) {
	ctx := context.Background()
	svc, conn := newOrchestratorForTest(t)
	sessSvc := session.NewService(opencodedb.New(conn), "proj")
	coder, hive := &busyRetryStubAgent{}, &busyRetryStubAgent{}
	svc.app = twoAgentApp(sessSvc, coder, hive)
	peer := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}
	if _, err := svc.store.UpsertBinding(ctx, store.Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "S1",
	}); err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	reply := svc.cmdAgent(ctx, bridge.Inbound{Peer: peer, CommandArgs: "hivemind"})
	if reply == nil || !strings.Contains(reply.Text, "hivemind") {
		t.Fatalf("reply = %+v, want a confirmation naming hivemind", reply)
	}
	got, err := sessSvc.Get(ctx, "S1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.AgentName != "hivemind" {
		t.Errorf("persisted agent = %q, want hivemind", got.AgentName)
	}
	if svc.app.ActiveAgentName() != config.AgentHivemind {
		t.Errorf("active agent = %q, want hivemind (the default for new sessions)", svc.app.ActiveAgentName())
	}

	// A refused (unknown) agent leaves the stored choice alone.
	reply = svc.cmdAgent(ctx, bridge.Inbound{Peer: peer, CommandArgs: "nope"})
	if reply == nil || !strings.Contains(reply.Text, "Failed") {
		t.Fatalf("reply = %+v, want a failure", reply)
	}
	if got, _ := sessSvc.Get(ctx, "S1"); got.AgentName != "hivemind" {
		t.Errorf("persisted agent after a refused switch = %q, want hivemind", got.AgentName)
	}
}

func TestCmdAgentWithoutBoundSessionOnlySwitchesTheDefault(t *testing.T) {
	ctx := context.Background()
	svc, conn := newOrchestratorForTest(t)
	sessSvc := session.NewService(opencodedb.New(conn), "proj")
	coder, hive := &busyRetryStubAgent{}, &busyRetryStubAgent{}
	svc.app = twoAgentApp(sessSvc, coder, hive)

	reply := svc.cmdAgent(ctx, bridge.Inbound{
		Peer:        bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "unbound"},
		CommandArgs: "hivemind",
	})
	if reply == nil || !strings.Contains(reply.Text, "hivemind") {
		t.Fatalf("reply = %+v", reply)
	}
	if svc.app.ActiveAgentName() != config.AgentHivemind {
		t.Errorf("active agent = %q, want hivemind", svc.app.ActiveAgentName())
	}
	if got, _ := sessSvc.Get(ctx, "S1"); got.AgentName != "" {
		t.Errorf("unrelated session got agent %q, want none", got.AgentName)
	}
}

func TestStampSessionAgentIsCompareAndSet(t *testing.T) {
	tests := []struct {
		name   string
		stored string
		prev   string
		stamp  config.AgentName
		want   string
	}{
		{name: "first stamp", stored: "", prev: "", stamp: config.AgentCoder, want: "coder"},
		{name: "replaces the value it resolved against", stored: "retired", prev: "retired", stamp: config.AgentCoder, want: "coder"},
		{name: "a concurrent /agent switch is kept", stored: "hivemind", prev: "", stamp: config.AgentCoder, want: "hivemind"},
		{name: "already stamped is a no-op", stored: "coder", prev: "coder", stamp: config.AgentCoder, want: "coder"},
		{name: "unregistered agent is never stamped", stored: "", prev: "", stamp: "ghost", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			svc, conn := newOrchestratorForTest(t)
			sessSvc := session.NewService(opencodedb.New(conn), "proj")
			svc.app = twoAgentApp(sessSvc, &busyRetryStubAgent{}, &busyRetryStubAgent{})
			if tt.stored != "" {
				if err := sessSvc.SetAgentName(ctx, "S1", tt.stored); err != nil {
					t.Fatalf("SetAgentName: %v", err)
				}
			}

			svc.stampSessionAgent(ctx, "S1", tt.stamp, tt.prev)

			got, err := sessSvc.Get(ctx, "S1")
			if err != nil {
				t.Fatalf("get session: %v", err)
			}
			if got.AgentName != tt.want {
				t.Errorf("stored agent = %q, want %q", got.AgentName, tt.want)
			}
		})
	}
}

// casLosingSessions loses the first losses compare-and-sets, as when a run's
// stamp lands between /agent's read and its write.
type casLosingSessions struct {
	session.Service
	losses int
}

func (c *casLosingSessions) SetAgentNameIf(ctx context.Context, id, expected, name string) (bool, error) {
	if c.losses > 0 {
		c.losses--
		return false, nil
	}
	return c.Service.SetAgentNameIf(ctx, id, expected, name)
}

func TestCmdAgentLosingTheCompareAndSet(t *testing.T) {
	tests := []struct {
		name       string
		losses     int
		wantStored string
		wantNote   bool
	}{
		{name: "one lost race is retried", losses: 1, wantStored: "hivemind"},
		{name: "persistent loss keeps the stored agent and says so", losses: 2, wantStored: "", wantNote: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			svc, conn := newOrchestratorForTest(t)
			real := session.NewService(opencodedb.New(conn), "proj")
			svc.app = twoAgentApp(&casLosingSessions{Service: real, losses: tt.losses}, &busyRetryStubAgent{}, &busyRetryStubAgent{})
			if _, err := svc.store.UpsertBinding(ctx, store.Binding{
				ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "S1",
			}); err != nil {
				t.Fatalf("UpsertBinding: %v", err)
			}

			reply := svc.cmdAgent(ctx, bridge.Inbound{
				Peer:        bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"},
				CommandArgs: "hivemind",
			})
			if reply == nil {
				t.Fatal("no reply")
			}
			if got := strings.Contains(reply.Text, "changed meanwhile"); got != tt.wantNote {
				t.Errorf("reply %q: note = %v, want %v", reply.Text, got, tt.wantNote)
			}
			got, err := real.Get(ctx, "S1")
			if err != nil {
				t.Fatalf("get session: %v", err)
			}
			if got.AgentName != tt.wantStored {
				t.Errorf("stored agent = %q, want %q", got.AgentName, tt.wantStored)
			}
		})
	}
}

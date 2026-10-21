package service

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/config"
)

const refusedOnChannel = "is not available on this channel"

// peerExt is an external conversation bound to S1 by newExternalCommandService.
func peerExt() bridge.PeerRef {
	return bridge.PeerRef{Channel: "external", Identity: "c3", PeerID: "aid1:flow:run1"}
}

// newExternalCommandService is newCancelTestService with a second session
// S2 (another user's conversation) and the external peer peerExt bound to
// S1 next to the Slack peer D1. commands, when non-nil, is the external
// channel's configured allowlist.
func newExternalCommandService(t *testing.T, ag *cancelStubAgent, commands []string) *Service {
	t.Helper()
	svc, _ := newCancelTestService(t, ag)
	if _, err := svc.app.Sessions.CreateWithID(context.Background(), "S2", "Other user's plan"); err != nil {
		t.Fatalf("create S2: %v", err)
	}
	p := peerExt()
	if _, err := svc.store.UpsertBinding(context.Background(), store.Binding{
		ProjectID: "proj", Channel: p.Channel, IdentityID: p.Identity, PeerID: p.PeerID, SessionID: "S1",
	}); err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	svc.cfg.Channels.External = &bridge.ExternalChannelConfig{Enabled: true, Commands: commands}
	return svc
}

func boundSession(t *testing.T, svc *Service, p bridge.PeerRef) string {
	t.Helper()
	b, err := svc.store.GetBinding(context.Background(), "proj", p.Channel, p.Identity, p.PeerID)
	if err != nil {
		return ""
	}
	return b.SessionID
}

// By default an external peer can run only /stop and /reset (CD-5331):
// every other known command is refused with a reply, and refusing it
// changes nothing — no session is listed or bound, and neither the active
// agent nor the verbosity moves.
func TestExternalChannelRefusesCommandsOffTheAllowlist(t *testing.T) {
	cases := []struct{ cmd, args string }{
		{"sessions", ""},
		{"session", ""},
		{"session", "S2"},
		{"agent", ""},
		{"agent", "other"},
		{"model", ""},
		{"model", "gpt-5"},
		{"help", ""},
		{"crons", ""},
		{"rename", "hijacked"},
		{"compact", ""},
		{"verbosity", "full"},
		{"heartbeat", "on"},
		{"skip", ""},
	}
	for _, c := range cases {
		t.Run(strings.TrimSpace("/"+c.cmd+" "+c.args), func(t *testing.T) {
			svc := newExternalCommandService(t, &cancelStubAgent{busy: map[string]bool{}}, nil)
			verbosity := svc.ToolVerbosity()
			reply := svc.handleChatCommand(context.Background(),
				bridge.Inbound{Peer: peerExt(), Command: c.cmd, CommandArgs: c.args})
			if reply == nil || reply.Text != "`/"+c.cmd+"` "+refusedOnChannel+"." {
				t.Fatalf("reply = %+v, want the refusal", reply)
			}
			for _, leak := range []string{"S2", "Other user's plan", "coder", "other"} {
				if strings.Contains(reply.Text, leak) {
					t.Errorf("refusal names %q: %s", leak, reply.Text)
				}
			}
			if got := boundSession(t, svc, peerExt()); got != "S1" {
				t.Errorf("external peer bound to %q, want S1", got)
			}
			if got := svc.app.ActiveAgentName(); got != config.AgentCoder {
				t.Errorf("active agent = %q, want coder", got)
			}
			if got := svc.ToolVerbosity(); got != verbosity {
				t.Errorf("verbosity = %q, want %q", got, verbosity)
			}
			if s1, err := svc.app.Sessions.Get(context.Background(), "S1"); err != nil || s1.Title == "hijacked" {
				t.Errorf("S1 = (%+v, %v), want it untouched", s1, err)
			}
		})
	}
}

// /stop and /reset, and their aliases, run on external by default.
func TestExternalChannelRunsStopAndReset(t *testing.T) {
	for _, cmd := range []string{"stop", "cancel", "abort"} {
		t.Run("/"+cmd, func(t *testing.T) {
			ag := &cancelStubAgent{busy: map[string]bool{"S1": true}}
			svc := newExternalCommandService(t, ag, nil)
			reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerExt(), Command: cmd})
			if reply == nil || !strings.Contains(reply.Text, "Aborted in-flight run on session S1") {
				t.Fatalf("reply = %+v, want the abort", reply)
			}
		})
	}
	for _, cmd := range []string{"reset", "new"} {
		t.Run("/"+cmd, func(t *testing.T) {
			svc := newExternalCommandService(t, &cancelStubAgent{busy: map[string]bool{}}, nil)
			reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerExt(), Command: cmd})
			if reply == nil || !strings.Contains(reply.Text, "Session reset") {
				t.Fatalf("reply = %+v, want the reset", reply)
			}
			if got := boundSession(t, svc, peerExt()); got != "" {
				t.Errorf("external peer still bound to %q after /%s", got, cmd)
			}
		})
	}
}

// A configured allowlist replaces the default, folds aliases, and is what
// /help lists.
func TestExternalChannelConfiguredAllowlist(t *testing.T) {
	ag := &cancelStubAgent{busy: map[string]bool{"S1": true}}
	svc := newExternalCommandService(t, ag, []string{"abort", "/Help"})

	if reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerExt(), Command: "stop"}); reply == nil || !strings.Contains(reply.Text, "Aborted") {
		t.Errorf("/stop reply = %+v, want the abort (alias of an allowed command)", reply)
	}
	if reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerExt(), Command: "reset"}); reply == nil || !strings.Contains(reply.Text, refusedOnChannel) {
		t.Errorf("/reset reply = %+v, want the refusal (off the configured list)", reply)
	}

	reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerExt(), Command: "help"})
	if reply == nil {
		t.Fatal("/help: nil reply")
	}
	var listed []string
	for _, line := range strings.Split(reply.Text, "\n") {
		listed = append(listed, strings.Fields(line)[0])
	}
	if got, want := strings.Join(listed, " "), "/abort /stop /cancel /help"; got != want {
		t.Errorf("/help on external lists %q, want %q", got, want)
	}
}

// The allowlist is the external channel's alone: a Slack peer still runs
// /agent and /sessions.
func TestExternalAllowlistLeavesChatChannelsAlone(t *testing.T) {
	svc := newExternalCommandService(t, &cancelStubAgent{busy: map[string]bool{}}, nil)
	if reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerD1(), Command: "agent", CommandArgs: "other"}); reply == nil || !strings.Contains(reply.Text, "Active agent: other") {
		t.Errorf("slack /agent reply = %+v, want the switch", reply)
	}
	if reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerD1(), Command: "sessions"}); reply == nil || strings.Contains(reply.Text, refusedOnChannel) {
		t.Errorf("slack /sessions reply = %+v, want the listing", reply)
	}
}

// Text whose first word is not a known command is not refused: it falls
// through to the agent as a prompt.
func TestExternalChannelUnknownCommandFallsThrough(t *testing.T) {
	svc := newExternalCommandService(t, &cancelStubAgent{busy: map[string]bool{}}, nil)
	cmd, args := splitChatCommand("/etc/hosts looks wrong")
	if reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerExt(), Command: cmd, CommandArgs: args}); !reply.IsEmpty() {
		t.Errorf("reply = %+v, want none (falls through to the agent)", reply)
	}
}

// End to end through dispatchInbound: the refusal reaches the external
// peer over its adapter, and the inbound neither re-binds the peer nor
// reaches the agent.
func TestDispatchInboundRefusesSessionsOnExternal(t *testing.T) {
	svc := newExternalCommandService(t, &cancelStubAgent{busy: map[string]bool{}}, nil)
	ad := newStubAdapter("external", "c3")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}
	queued := len(svc.dispatchers["S1"].inbound)

	for _, text := range []string{"/sessions", "/session S2"} {
		svc.dispatchInbound(context.Background(), bridge.Inbound{Peer: peerExt(), Text: text})
	}

	sends := ad.Sends()
	if len(sends) != 2 {
		t.Fatalf("adapter sends = %d, want 2 refusals: %+v", len(sends), sends)
	}
	for _, out := range sends {
		if !strings.Contains(out.Text, refusedOnChannel) || out.Peer.PeerID != peerExt().PeerID {
			t.Errorf("send = %+v, want a refusal to the external peer", out)
		}
		if strings.Contains(out.Text, "Other user's plan") {
			t.Errorf("refusal leaks another session: %s", out.Text)
		}
	}
	if got := boundSession(t, svc, peerExt()); got != "S1" {
		t.Errorf("external peer bound to %q, want S1", got)
	}
	if got := len(svc.dispatchers["S1"].inbound); got != queued {
		t.Errorf("queued inbound = %d, want %d (a refused command must not reach the agent)", got, queued)
	}
}

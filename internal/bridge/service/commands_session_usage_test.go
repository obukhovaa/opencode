package service

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
)

type contextUsageStub struct {
	*compactStubAgent
	usage agentpkg.SessionContextUsage
}

func (a *contextUsageStub) ContextUsage(context.Context, string) (agentpkg.SessionContextUsage, error) {
	return a.usage, nil
}

func TestCmdSessionSeparatesCurrentContextAndCumulativeUsage(t *testing.T) {
	ctx := context.Background()
	active := &compactStubAgent{model: models.Model{ContextWindow: 10000}}
	svc, sessions, _ := newCompactTestService(t, active, nil)
	sess, err := sessions.Get(ctx, "S1")
	if err != nil {
		t.Fatal(err)
	}
	sess.PromptTokens, sess.CompletionTokens = 900, 100
	sess.TotalPromptTokens, sess.TotalCompletionTokens = 100000, 20000
	sess.AgentName = "other"
	if _, err = sessions.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := sessions.SetAgentName(ctx, "S1", "other"); err != nil {
		t.Fatal(err)
	}
	svc.app.PrimaryAgents[config.AgentName("other")] = &contextUsageStub{
		compactStubAgent: &compactStubAgent{model: models.Model{ContextWindow: 20000}},
		usage:            agentpkg.SessionContextUsage{Tokens: 1500, Reported: true, Estimated: true},
	}
	reply := svc.cmdSession(ctx, bridge.Inbound{Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}})
	for _, want := range []string{"Context: ~1.5k / 20.0k tokens (7.5%) (estimated)", "Usage (session total): 100.0k in / 20.0k out"} {
		if !strings.Contains(reply.Text, want) {
			t.Fatalf("missing %q in %s", want, reply.Text)
		}
	}
}

func TestCmdSessionDoesNotInventTotalsOrContext(t *testing.T) {
	ctx := context.Background()
	svc, sessions, _ := newCompactTestService(t, &compactStubAgent{}, nil)
	peer := bridge.Inbound{Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}}
	empty := svc.cmdSession(ctx, peer)
	if !strings.Contains(empty.Text, "Context: not reported yet") || !strings.Contains(empty.Text, "Usage (session total): 0 in / 0 out") {
		t.Fatal(empty.Text)
	}
	sess, _ := sessions.Get(ctx, "S1")
	sess.PromptTokens, sess.CompletionTokens = 100, 900 // Legacy cache-in-output split.
	if _, err := sessions.Save(ctx, sess); err != nil {
		t.Fatal(err)
	}
	reply := svc.cmdSession(ctx, peer)
	if !strings.Contains(reply.Text, "Context: 1.0k tokens (last reported)") || !strings.Contains(reply.Text, "Usage (session total): not recorded") {
		t.Fatal(reply.Text)
	}
}

package app

import (
	"context"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

// requesterAgent extends fakeAgent to report the requester each Run ctx
// carries.
type requesterAgent struct {
	fakeAgent
	got chan string
}

func (r *requesterAgent) Run(ctx context.Context, sid string, content string, max int, a ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	r.got <- tools.RequesterFromContext(ctx)
	return r.fakeAgent.Run(ctx, sid, content, max, a...)
}

// The auto-resumed turn runs on a fresh background ctx; the task's
// requester must be put back on it so the turn's traces stay attributed.
func TestTaskDepsResumeSessionCarriesRequester(t *testing.T) {
	tests := []struct {
		name      string
		requester string
	}{
		{name: "requester is replayed", requester: "alice@"},
		{name: "no requester stays empty", requester: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := &requesterAgent{got: make(chan string, 1)}
			d := &taskDeps{app: &App{activeAgent: ag}}

			d.ResumeSession("s1", tt.requester, nil)

			select {
			case got := <-ag.got:
				if got != tt.requester {
					t.Fatalf("resumed run requester = %q, want %q", got, tt.requester)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("ResumeSession never started a run")
			}
		})
	}
}

type runRecorder struct {
	agentpkg.Service
	name string
	ran  chan string
}

func (r *runRecorder) Run(_ context.Context, _ string, _ string, _ int, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	r.ran <- r.name
	ch := make(chan agentpkg.AgentEvent)
	close(ch)
	return ch, nil
}

// The auto-resume must run under the agent the session is stamped with, not
// the process-wide active one.
func TestTaskDepsResumeSessionUsesTheSessionsAgent(t *testing.T) {
	tests := []struct {
		name    string
		stamped string
		want    string
	}{
		{name: "stamped session resumes on its agent", stamped: "hivemind", want: "hivemind"},
		{name: "unstamped session resumes on the active agent", stamped: "", want: "coder"},
		{name: "unregistered stamp falls back to the active agent", stamped: "retired", want: "coder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ran := make(chan string, 1)
			a := &App{
				Sessions: &fakeSessions{agentName: tt.stamped},
				PrimaryAgents: map[config.AgentName]agentpkg.Service{
					config.AgentCoder:    &runRecorder{name: "coder", ran: ran},
					config.AgentHivemind: &runRecorder{name: "hivemind", ran: ran},
				},
				PrimaryAgentKeys: []config.AgentName{config.AgentCoder, config.AgentHivemind},
			}
			(&taskDeps{app: a}).ResumeSession("s1", "", nil)
			select {
			case got := <-ran:
				if got != tt.want {
					t.Fatalf("resumed on %q, want %q", got, tt.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("ResumeSession never started a run")
			}
		})
	}
}

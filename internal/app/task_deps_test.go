package app

import (
	"context"
	"testing"
	"time"

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

			d.ResumeSession("s1", tt.requester)

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

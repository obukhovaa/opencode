package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/flow"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

type planningQueueAgent struct {
	fakeAgent
	started, release chan struct{}
}

func (a *planningQueueAgent) Run(ctx context.Context, id, prompt string, _ int, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	close(a.started)
	select {
	case <-a.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	data, _ := json.Marshal(tools.FlowRequest{Operation: "create", Complexity: "S", Definition: "flow:\n  steps:\n    - id: work\n      prompt: finish work\n"})
	resp, err := tools.NewFlowTool().Run(context.WithValue(ctx, tools.SessionIDContextKey, id), tools.ToolCall{Input: string(data)})
	if err != nil {
		return nil, err
	}
	if resp.IsError {
		return nil, fmt.Errorf("%s", resp.Content)
	}
	ch := make(chan agentpkg.AgentEvent)
	close(ch)
	return ch, nil
}

type planningMessages struct {
	message.Service
	mu     sync.Mutex
	stored []message.CreateMessageParams
}

func (m *planningMessages) Create(_ context.Context, _ string, p message.CreateMessageParams) (message.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stored = append(m.stored, p)
	return message.Message{Role: p.Role, Parts: p.Parts}, nil
}

type planningQueueFlow struct {
	flow.Service
	received chan string
}

func (f planningQueueFlow) RunWithOptions(ctx context.Context, _, _ string, args map[string]any, opts flow.RunOptions) (<-chan agentpkg.AgentEvent, <-chan *flow.FlowState, error) {
	step := opts.Definition.Spec.Steps[0]
	_, err := opts.BeforeStep(ctx, &step, args)
	if err != nil {
		return nil, nil, err
	}
	// The real engine substitutes args only after parsing template shell
	// markup. Observe that contract instead of treating the raw template as
	// the final model prompt (which would encourage unsafe text interpolation).
	if !strings.Contains(step.Prompt, "${args.user_steering}") {
		return nil, nil, fmt.Errorf("step template does not consume steering")
	}
	f.received <- fmt.Sprint(args["user_steering"])
	events := make(chan agentpkg.AgentEvent)
	states := make(chan *flow.FlowState)
	close(events)
	close(states)
	return events, states, nil
}
func TestDynamicFlowQueuesSteeringWhilePlanning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "openspec"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	actor := &planningQueueAgent{started: make(chan struct{}), release: make(chan struct{})}
	msgs := &planningMessages{}
	received := make(chan string, 1)
	a := &App{ctx: ctx, Sessions: reviewSessionService{}, Messages: msgs, Registry: workflowRegistry{}, PrimaryAgents: map[config.AgentName]agentpkg.Service{config.AgentCoder: actor}, PrimaryAgentKeys: []config.AgentName{config.AgentCoder}, Flows: planningQueueFlow{received: received}}
	if err := a.StartDynamicFlow(ctx, "planning-root", "build"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-actor.started:
	case <-ctx.Done():
		t.Fatal("builder did not start")
	}
	if err := a.QueueFlowSteering("planning-root", "include regression tests"); err != nil {
		t.Fatal(err)
	}
	msgs.mu.Lock()
	if len(msgs.stored) != 1 || msgs.stored[0].Role != message.User {
		t.Fatal("planning message not stored as user input")
	}
	msgs.mu.Unlock()
	close(actor.release)
	select {
	case prompt := <-received:
		if !strings.Contains(prompt, "include regression tests") {
			t.Fatalf("first step lost steering: %s", prompt)
		}
	case <-ctx.Done():
		t.Fatal("first step not dispatched")
	}
	waitWorkflowIdle(t, ctx, a, "planning-root")
	dir, _ := flow.DynamicDirectory()
	m, err := flow.LoadDynamic(dir, "planning-root")
	if err != nil || len(m.Steering) != 1 {
		t.Fatalf("pending steering was not persisted: %+v %v", m, err)
	}
}

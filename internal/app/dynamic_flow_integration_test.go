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

	registry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/contextfile"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/flow"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/question"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/pressly/goose/v3"
)

type workflowRegistry struct{ registry.Registry }

func (workflowRegistry) Get(id string) (registry.AgentInfo, bool) {
	return registry.AgentInfo{ID: id, Mode: config.AgentModeAgent, DynamicFlow: &config.DynamicFlow{Enabled: true, FlowComplexity: "S"}}, true
}
func (workflowRegistry) ListByMode(config.AgentMode) []registry.AgentInfo { return nil }
func (workflowRegistry) IsToolEnabled(string, string) bool                { return true }

type workflowAgent struct {
	fakeAgent
	mu         sync.Mutex
	calls      []string
	question   question.Service
	started    chan string
	definition string
	pause      bool
}

func (a *workflowAgent) Run(ctx context.Context, id, prompt string, _ int, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	data, _ := json.Marshal(tools.FlowRequest{Operation: "create", Complexity: "S", Definition: a.definition})
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, id)
	response, err := tools.NewFlowTool().Run(ctx, tools.ToolCall{Input: string(data)})
	if err != nil {
		return nil, err
	}
	if response.IsError {
		return nil, fmt.Errorf("%s", response.Content)
	}
	ch := make(chan agentpkg.AgentEvent)
	close(ch)
	return ch, nil
}
func (a *workflowAgent) RunWith(ctx context.Context, id, prompt string, _ int, opts agentpkg.RunOptions, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	if !opts.NonInteractive {
		return nil, fmt.Errorf("flow must retain background drain semantics")
	}
	a.mu.Lock()
	a.calls = append(a.calls, prompt)
	pause := a.pause
	a.pause = false
	a.mu.Unlock()
	ch := make(chan agentpkg.AgentEvent, 1)
	go func() {
		defer close(ch)
		if pause {
			if _, err := a.question.Ask(ctx, id, []question.Prompt{{Question: "Continue?", Options: []question.Option{{Label: "Yes"}}}}); err != nil {
				ch <- agentpkg.AgentEvent{Error: err}
				return
			}
			a.started <- id
			<-ctx.Done()
			ch <- agentpkg.AgentEvent{Error: ctx.Err()}
			return
		}
		ch <- agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeResponse, Message: message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "done"}}}}
	}()
	return ch, nil
}

type workflowFactory struct {
	agentpkg.AgentFactory
	agent *workflowAgent
}

func (f workflowFactory) NewAgent(context.Context, string, map[string]any, string, bool, []bridge.PeerRef, *contextfile.StepContext, contextfile.TemplateVars, agentpkg.ModelOverride) (agentpkg.Service, error) {
	return f.agent, nil
}
func (f workflowFactory) QuestionService() question.Service { return f.agent.question }
func (f workflowFactory) ResetStepCache()                   {}

func TestDynamicFlowLifecycleIntegration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "openspec"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if config.Get() == nil {
		if _, err := config.Load(home, false); err != nil {
			t.Fatal(err)
		}
	}
	database, err := db.NewSQLiteProvider(filepath.Join(home, "db")).Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	goose.SetBaseFS(db.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(database, "migrations/sqlite"); err != nil {
		t.Fatal(err)
	}
	q := db.NewQuerier(database)
	sessions := session.NewService(q, "workflow-test")
	messages := message.NewService(q, database)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root, err := sessions.Create(ctx, "workflow")
	if err != nil {
		t.Fatal(err)
	}
	questions := question.NewService()
	requests := questions.Subscribe(ctx)
	actor := &workflowAgent{question: questions, started: make(chan string, 1), pause: true, definition: "name: lifecycle\nflow:\n  steps:\n    - id: implement\n      prompt: implement\n      rules:\n        - then: verify\n    - id: verify\n      prompt: verify\n"}
	a := &App{ctx: ctx, Sessions: sessions, Messages: messages, Registry: workflowRegistry{}, PrimaryAgents: map[config.AgentName]agentpkg.Service{config.AgentCoder: actor}, PrimaryAgentKeys: []config.AgentName{config.AgentCoder}, flowQuery: q}
	a.Flows = flow.NewService(sessions, messages, q, permission.NewPermissionService(), workflowFactory{agent: actor})
	if err := a.StartDynamicFlow(ctx, root.ID, "build feature"); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-requests:
		if err := questions.Reply(req.Payload.ID, [][]string{{"Yes"}}); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		msgs, _ := messages.List(context.Background(), root.ID)
		snap, _ := a.DynamicFlowSnapshot(context.Background(), root.ID)
		t.Fatalf("question not delivered; messages=%+v snapshot=%+v", msgs, snap)
	}
	select {
	case <-actor.started:
	case <-ctx.Done():
		t.Fatal("step not started")
	}
	if err := a.QueueFlowSteering(root.ID, "include regression coverage"); err != nil {
		t.Fatal(err)
	}
	if err := a.ControlFlow(ctx, root.ID, "implement", "cancel"); err != nil {
		t.Fatal(err)
	}
	waitWorkflowIdle(t, ctx, a, root.ID)
	snap, err := a.DynamicFlowSnapshot(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status == "completed" {
		t.Fatalf("cancelled flow completed: %+v", snap)
	}
	// Rebuild the App to exercise recovery from persisted manifest and SQLite,
	// with no in-memory run or regenerated plan.
	recovered := &App{ctx: ctx, Sessions: session.NewService(q, "workflow-test"), Messages: messages, Registry: workflowRegistry{}, PrimaryAgents: a.PrimaryAgents, PrimaryAgentKeys: a.PrimaryAgentKeys, flowQuery: q}
	recovered.Flows = flow.NewService(recovered.Sessions, messages, q, permission.NewPermissionService(), workflowFactory{agent: actor})
	if err := recovered.ResumeFlow(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	waitWorkflowIdle(t, ctx, recovered, root.ID)
	snap, err = recovered.DynamicFlowSnapshot(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Status != "completed" || len(snap.Steps) != 2 {
		t.Fatalf("unexpected final state: %+v", snap)
	}
	actor.mu.Lock()
	calls := strings.Join(actor.calls, "\n")
	actor.mu.Unlock()
	if !strings.Contains(calls, "include regression coverage") {
		t.Fatalf("steering lost across recovery: %s", calls)
	}
	if snap.Steps[0].SessionID == root.ID || snap.Steps[0].SessionID == snap.Steps[1].SessionID {
		t.Fatal("step sessions were not isolated")
	}
}
func waitWorkflowIdle(t *testing.T, ctx context.Context, a *App, id string) {
	t.Helper()
	for a.FlowBusy(id) {
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("flow did not settle")
		}
	}
}

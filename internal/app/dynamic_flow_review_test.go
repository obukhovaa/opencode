package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	registry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/flow"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/session"
)

type reviewFlowQuery struct {
	db.QuerierWithTx
	rows    []db.FlowState
	updates int
}

func (q *reviewFlowQuery) ListFlowStatesByRootSession(context.Context, string) ([]db.FlowState, error) {
	return q.rows, nil
}
func (q *reviewFlowQuery) UpdateFlowState(_ context.Context, p db.UpdateFlowStateParams) (db.FlowState, error) {
	q.updates++
	return db.FlowState{SessionID: p.SessionID, Status: p.Status}, nil
}

type reviewSessionService struct{ session.Service }

func (reviewSessionService) Get(_ context.Context, id string) (session.Session, error) {
	return session.Session{ID: id}, nil
}

type reviewFlowRegistry struct{ workflowRegistry }

func (reviewFlowRegistry) Get(id string) (registry.AgentInfo, bool) {
	return registry.AgentInfo{ID: id, Mode: config.AgentModeAgent, Model: "custom.model", MaxTokens: 4321, DynamicFlow: &config.DynamicFlow{Enabled: true, FlowComplexity: "XS"}}, true
}

type reviewFlowService struct {
	flow.Service
	seen chan flow.RunOptions
}

func (f reviewFlowService) RunWithOptions(_ context.Context, _, _ string, _ map[string]any, opts flow.RunOptions) (<-chan agentpkg.AgentEvent, <-chan *flow.FlowState, error) {
	f.seen <- opts
	events := make(chan agentpkg.AgentEvent)
	close(events)
	states := make(chan *flow.FlowState)
	close(states)
	return events, states, nil
}

func reviewFlowHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("OPENCODE_PROFILE", "")
	if err := os.WriteFile(filepath.Join(home, "openspec"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", home+string(os.PathListSeparator)+os.Getenv("PATH"))
	return home
}
func reviewApp() *App {
	return &App{Sessions: reviewSessionService{}, Registry: reviewFlowRegistry{}, PrimaryAgents: map[config.AgentName]agentpkg.Service{config.AgentCoder: &fakeAgent{}}, PrimaryAgentKeys: []config.AgentName{config.AgentCoder}, flowQuery: &reviewFlowQuery{}}
}

func TestReviewOrdinaryFlowToolHonorsConfiguredLimit(t *testing.T) {
	reviewFlowHome(t)
	a := reviewApp()
	input, _ := json.Marshal(tools.FlowRequest{Operation: "create", Complexity: "XXL", Definition: "name: oversized\nflow:\n  steps:\n    - id: work\n      prompt: work\n      maxTurns: 21\n"})
	ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, "root")
	ctx = context.WithValue(ctx, tools.AgentIDContextKey, config.AgentCoder)
	result, err := tools.NewFlowTool(a.submitOrdinaryFlow).Run(ctx, tools.ToolCall{Input: string(input)})
	if err != nil || !result.IsError || !strings.Contains(result.Content, "turn limit 20") {
		t.Fatalf("configured XS cap bypassed: %+v %v", result, err)
	}
	if a.FlowBusy("root") {
		t.Fatal("invalid definition started execution")
	}
}

func TestReviewOrdinaryFlowToolExecutesWithPrimaryIdentity(t *testing.T) {
	reviewFlowHome(t)
	a := reviewApp()
	seen := make(chan flow.RunOptions, 1)
	a.Flows = reviewFlowService{seen: seen}
	input, _ := json.Marshal(tools.FlowRequest{Operation: "create", Complexity: "XXL", Definition: "name: ordinary\nflow:\n  steps:\n    - id: work\n      prompt: work\n"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, "root")
	ctx = context.WithValue(ctx, tools.AgentIDContextKey, config.AgentCoder)
	result, err := tools.NewFlowTool(a.submitOrdinaryFlow).Run(ctx, tools.ToolCall{Input: string(input)})
	if err != nil || result.IsError {
		t.Fatalf("ordinary tool unavailable: %+v %v", result, err)
	}
	select {
	case opts := <-seen:
		if opts.Definition.Spec.Steps[0].Agent != string(config.AgentCoder) || opts.Definition.Spec.Steps[0].MaxTurns != 20 {
			t.Fatalf("identity/cap lost: %+v", opts.Definition)
		}
	case <-ctx.Done():
		t.Fatal("flow never executed")
	}
	waitWorkflowIdle(t, ctx, a, "root")
	dir, _ := flow.DynamicDirectory()
	m, err := flow.LoadDynamic(dir, "root")
	if err != nil || m.Agent != string(config.AgentCoder) || m.Complexity != "XS" {
		t.Fatalf("wrong manifest: %+v %v", m, err)
	}
	info, _ := a.Registry.Get(string(config.AgentCoder))
	if info.Model != "custom.model" || info.MaxTokens != 4321 {
		t.Fatal("flow changed agent model/settings")
	}
}

func TestReviewCompletedAdoptedStandaloneDoesNotRerun(t *testing.T) {
	reviewFlowHome(t)
	a := reviewApp()
	a.flowQuery = &reviewFlowQuery{rows: []db.FlowState{{FlowID: "saved", StepID: "work", SessionID: "root", Status: "completed"}}}
	f, err := flow.ParseDynamic("name: saved\nflow:\n  steps:\n    - id: work\n      prompt: work\n", "saved", "coder", "XS", nil)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := flow.DynamicDirectory()
	if err := flow.SaveDynamic(dir, flow.DynamicManifest{RootSessionID: "root", Definition: *f, Standalone: true, SessionPrefix: "prefix", Agent: "coder", Complexity: "XS"}); err != nil {
		t.Fatal(err)
	}
	if err := a.ResumeFlow(context.Background(), "root"); err == nil || !strings.Contains(err.Error(), "completed") {
		t.Fatalf("completed standalone was rerun: %v", err)
	}
	if a.FlowBusy("root") {
		t.Fatal("completed flow acquired runtime")
	}
}

func TestReviewUnsafeSkipRejectedBeforeStateMutation(t *testing.T) {
	for _, tc := range []struct{ name, extra string }{{"schema", "      output:\n        schema:\n          type: object\n"}, {"conditional", "      rules:\n        - if: ${args.ready} == true\n          then: work\n"}} {
		t.Run(tc.name, func(t *testing.T) {
			reviewFlowHome(t)
			a := reviewApp()
			q := &reviewFlowQuery{rows: []db.FlowState{{FlowID: "saved", StepID: "work", SessionID: "step", Status: "failed"}}}
			a.flowQuery = q
			f, err := flow.ParseDynamic("name: saved\nflow:\n  steps:\n    - id: work\n      prompt: work\n"+tc.extra, "saved", "coder", "XS", nil)
			if err != nil {
				t.Fatal(err)
			}
			dir, _ := flow.DynamicDirectory()
			if err := flow.SaveDynamic(dir, flow.DynamicManifest{RootSessionID: "root", Definition: *f, Agent: "coder", Complexity: "XS"}); err != nil {
				t.Fatal(err)
			}
			err = a.ControlFlow(context.Background(), "root", "work", "skip")
			if err == nil || q.updates != 0 {
				t.Fatalf("unsafe skip mutated state: err=%v updates=%d", err, q.updates)
			}
		})
	}
}

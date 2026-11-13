package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	registry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/flowrun"
	"github.com/opencode-ai/opencode/internal/langfuse"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/question"
	"github.com/opencode-ai/opencode/internal/session"
)

type storedPlanner struct{ *workflowAgent }

func (p storedPlanner) RunWith(ctx context.Context, id, prompt string, maxTurns int, opts agentpkg.RunOptions, atts ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	if strings.HasPrefix(prompt, "Build a dynamic workflow") {
		return p.workflowAgent.Run(ctx, id, prompt, maxTurns, atts...)
	}
	return p.workflowAgent.RunWith(ctx, id, prompt, maxTurns, opts, atts...)
}

type storedWorkflowRegistry struct{ workflowRegistry }

func (storedWorkflowRegistry) EvaluatePermission(string, string, string) permission.Action {
	return permission.ActionAllow
}

func newStoredWorkflow(t *testing.T) (*App, *workflowAgent, string, string, context.Context) {
	a, actor, root, home, ctx, _ := newStoredWorkflowDB(t)
	return a, actor, root, home, ctx
}
func newStoredWorkflowDB(t *testing.T) (*App, *workflowAgent, string, string, context.Context, *sql.DB) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("OPENCODE_PROFILE", "")
	config.Reset()
	viper.Reset()
	flow.Invalidate()
	t.Cleanup(func() { config.Reset(); viper.Reset(); flow.Invalidate() })
	_, err := config.Load(home, false)
	require.NoError(t, err)
	config.Get().WorkingDir = home
	database, err := db.NewSQLiteProvider(filepath.Join(home, "db")).Connect()
	require.NoError(t, err)
	goose.SetBaseFS(db.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.Up(database, "migrations/sqlite"))
	q := db.NewQuerier(database)
	sessions := session.NewService(q, "stored-flow-test")
	messages := message.NewService(q, database)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	questions := question.NewService()
	actor := &workflowAgent{question: questions, started: make(chan string, 1), definition: "name: generated\nflow:\n  steps:\n    - id: work\n      prompt: work\n"}
	perms := permission.NewPermissionService()
	a := &App{ctx: ctx, Sessions: sessions, Messages: messages, Questions: questions, Permissions: perms, Registry: storedWorkflowRegistry{}, PrimaryAgents: map[config.AgentName]agentpkg.Service{config.AgentCoder: storedPlanner{actor}}, PrimaryAgentKeys: []config.AgentName{config.AgentCoder}, flowQuery: q, FlowRuns: flowrun.New(database, q, "stored-flow-test", "sqlite"), flowProjectID: "stored-flow-test"}
	session.SetDeleteHandler(sessions, a.handleFlowSessionDelete)
	a.Flows = flow.NewService(sessions, messages, q, perms, workflowFactory{agent: actor})
	t.Cleanup(func() { a.ShutdownFlows(time.Second); cancel(); database.Close() })
	root, err := sessions.Create(ctx, "stored workflow")
	require.NoError(t, err)
	return a, actor, root.ID, home, ctx, database
}
func writeNamedWorkflow(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, ".agents", "flows", "existing.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte("name: Authored\nflow:\n  args:\n    type: object\n    properties:\n      goal: {type: string}\n    required: [goal]\n  steps:\n    - id: first\n      prompt: 'Authored ${args.goal}'\n      rules:\n        - then: last\n    - id: last\n      prompt: Authored finish\n"), 0600))
	flow.Invalidate()
	return path
}
func TestStoredNamedFlowRunsWithoutOpenSpecAndAllowsSequentialRuns(t *testing.T) {
	a, actor, root, home, ctx := newStoredWorkflow(t)
	actor.output = "FINAL-RESULT-731"
	writeNamedWorkflow(t, home)
	t.Setenv("PATH", "")
	req := tools.FlowRequest{Operation: "run", FlowID: "existing", Args: map[string]any{"goal": "original"}, InvocationID: "invoke-1"}
	first, err := a.SubmitFlow(ctx, root, req)
	require.NoError(t, err)
	waitWorkflowIdle(t, ctx, a, root)
	state, err := a.CurrentFlow(ctx, root)
	require.NoError(t, err)
	require.Equal(t, "completed", state.Status)
	events, e := a.ReplayFlowEvents(ctx, root, "", 100)
	require.NoError(t, e)
	terminal := false
	for _, event := range events {
		if event.Type == "session.flow.completed" {
			terminal = true
			require.Contains(t, event.Output, "FINAL-RESULT-731")
		}
	}
	require.True(t, terminal)
	history, e := a.Messages.List(ctx, root)
	require.NoError(t, e)
	require.Contains(t, history[len(history)-1].Content().String(), "FINAL-RESULT-731")
	repeated, err := a.SubmitFlow(ctx, root, req)
	require.NoError(t, err)
	require.Equal(t, first.RunID, repeated.RunID)
	req.InvocationID = "invoke-2"
	second, err := a.SubmitFlow(ctx, root, req)
	require.NoError(t, err)
	require.NotEqual(t, first.RunID, second.RunID)
	waitWorkflowIdle(t, ctx, a, root)
	actor.mu.Lock()
	calls := append([]string(nil), actor.calls...)
	actor.mu.Unlock()
	require.Len(t, calls, 4)
	children, err := a.Sessions.ListChildren(ctx, root)
	require.NoError(t, err)
	require.Len(t, children, 5)
	for _, s := range children {
		if s.ID == root {
			continue
		}
		require.Equal(t, root, s.RootSessionID)
		conv, ok, err := a.SessionFlowConversation(ctx, s.ID)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, root, conv)
	}
}
func TestStoredNamedResumeUsesSnapshotAndDurableSteering(t *testing.T) {
	a, actor, root, home, ctx := newStoredWorkflow(t)
	pending, err := a.PendingFlowMessages(ctx, root)
	require.NoError(t, err)
	require.Empty(t, pending)
	source := writeNamedWorkflow(t, home)
	actor.pause = true
	requests := a.Questions.Subscribe(ctx)
	first, err := a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "run", FlowID: "existing", Args: map[string]any{"goal": "snapshot"}, InvocationID: "persisted"})
	require.NoError(t, err)
	select {
	case q := <-requests:
		require.NoError(t, a.Questions.Reply(q.Payload.ID, [][]string{{"Yes"}}))
	case <-ctx.Done():
		t.Fatal("question absent")
	}
	select {
	case <-actor.started:
	case <-ctx.Done():
		t.Fatal("step absent")
	}
	require.NoError(t, a.QueueFlowMessages(ctx, root, []FlowSteeringMessage{{ID: "message-1", Text: "retain this steering", Origin: json.RawMessage(`{"channel":"external","peerId":"one"}`)}}))
	// Inspection must expose durable inputs without acknowledging them, even
	// when a new UI instance reads the queue after reconnecting.
	for range 2 {
		pending, err = a.PendingFlowMessages(ctx, root)
		require.NoError(t, err)
		require.Len(t, pending, 1)
		require.Equal(t, "retain this steering", pending[0].Text)
	}

	require.NoError(t, a.ControlFlow(ctx, root, "first", "cancel"))
	require.NoError(t, os.Remove(source))
	flow.Invalidate()
	// A new profile directory models a replacement worker without legacy files.
	t.Setenv("HOME", t.TempDir())
	recovered := &App{ctx: ctx, Sessions: a.Sessions, Messages: a.Messages, Questions: a.Questions, Permissions: a.Permissions, Registry: a.Registry, PrimaryAgents: a.PrimaryAgents, PrimaryAgentKeys: a.PrimaryAgentKeys, flowQuery: a.flowQuery, FlowRuns: a.FlowRuns, flowProjectID: a.flowProjectID, Flows: a.Flows}
	t.Cleanup(func() { recovered.ShutdownFlows(time.Second) })
	require.NoError(t, recovered.ResumeFlow(ctx, root))
	waitWorkflowIdle(t, ctx, recovered, root)
	state, err := recovered.CurrentFlow(ctx, root)
	require.NoError(t, err)
	require.Equal(t, "completed", state.Status)
	require.Equal(t, first.RunID, state.ID)
	actor.mu.Lock()
	calls := strings.Join(actor.calls, "\n")
	actor.mu.Unlock()
	require.Contains(t, calls, "Authored snapshot")
	require.Contains(t, calls, "retain this steering")
	pending, err = recovered.PendingFlowMessages(ctx, root)
	require.NoError(t, err)
	require.Empty(t, pending, "delivered messages must disappear from the queue viewer")
	inbox, err := a.FlowRuns.PeekInbox(ctx, state.ID)
	require.NoError(t, err)
	require.Empty(t, inbox)
	events, err := a.ReplayFlowEvents(ctx, root, "", 100)
	require.NoError(t, err)
	delivered := false
	for _, e := range events {
		if e.Type == "session.flow.messages.delivered" {
			delivered = true
			require.Equal(t, []string{"message-1"}, e.MessageIDs)
		}
	}
	require.True(t, delivered)
}
func TestStoredPlanPersistsBeforeBuilderAndPreservesInvocation(t *testing.T) {
	a, _, root, home, ctx := newStoredWorkflow(t)
	require.NoError(t, os.WriteFile(filepath.Join(home, "openspec"), []byte("#!/bin/sh\nexit 0\n"), 0700))
	t.Setenv("PATH", home)
	result, err := a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "plan", Goal: "implement small task", InvocationID: "plan-1"})
	require.NoError(t, err)
	waitWorkflowIdle(t, ctx, a, root)
	r, err := a.CurrentFlow(ctx, root)
	require.NoError(t, err)
	require.Equal(t, result.RunID, r.ID)
	require.Equal(t, "completed", r.Status)
	var d storedFlowDefinition
	require.NoError(t, json.Unmarshal(r.Snapshot, &d))
	require.False(t, d.Planning)
	require.NotEmpty(t, d.Manifest.Definition.Spec.Steps)
	repeated, err := a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "plan", Goal: "implement small task", InvocationID: "plan-1"})
	require.NoError(t, err)
	require.Equal(t, result.RunID, repeated.RunID)
}

func TestStoredFlowDeletionOwnsRunAndStepHistory(t *testing.T) {
	a, _, root, home, ctx := newStoredWorkflow(t)
	writeNamedWorkflow(t, home)
	result, err := a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "run", FlowID: "existing", Args: map[string]any{"goal": "deletion"}})
	require.NoError(t, err)
	waitWorkflowIdle(t, ctx, a, root)
	snapshot, err := a.DynamicFlowSnapshot(ctx, root)
	require.NoError(t, err)
	require.ErrorIs(t, a.Sessions.Delete(ctx, snapshot.Steps[0].SessionID), ErrFlowPermission)
	child, err := a.Sessions.CreateTaskSession(ctx, "ordinary-child", root, "ordinary task")
	require.NoError(t, err)
	require.NoError(t, a.Sessions.Delete(ctx, child.ID))
	require.NoError(t, a.Sessions.Delete(ctx, root))
	_, err = a.FlowRuns.Get(ctx, result.RunID)
	require.ErrorIs(t, err, flowrun.ErrNotFound)
	for _, step := range snapshot.Steps {
		_, err = a.Sessions.Get(ctx, step.SessionID)
		require.ErrorIs(t, err, flowrun.ErrNotFound)
	}
}

func TestStoredQuestionRecoveryUsesExactToolCall(t *testing.T) {
	for _, mode := range []string{"pending-crash", "answered-result-written", "canceled-result-written", "accepted-answer-with-canceled-result"} {
		t.Run(mode, func(t *testing.T) {
			a, _, root, _, ctx := newStoredWorkflow(t)
			stored, _, err := a.FlowRuns.Create(ctx, flowrun.Run{ID: "question-run", ConversationID: root, Kind: "generated", Status: "running", InvocationID: "question-invocation", Snapshot: json.RawMessage(`{}`), Origin: json.RawMessage(`{}`)})
			require.NoError(t, err)
			lease, err := a.FlowRuns.Claim(ctx, stored.ID, "question-owner", time.Minute)
			require.NoError(t, err)
			t.Cleanup(func() { _ = a.FlowRuns.Release(context.Background(), lease, "") })
			child, err := a.Sessions.CreateFlowSession(ctx, "question-step", root, "question")
			require.NoError(t, err)
			_, err = a.Messages.Create(ctx, child.ID, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "question-call", Name: tools.QuestionToolName, Input: `{"questions":[]}`, Finished: true}}})
			require.NoError(t, err)
			prompts := []question.Prompt{{Question: "Choose?", Options: []question.Option{{Label: "Yes"}}}}
			desc, _ := json.Marshal(question.DurableDescriptor{ToolCallID: "question-call", Questions: prompts})
			q, err := a.FlowRuns.PutQuestion(ctx, lease, flowrun.Question{ID: "logical-question", SessionID: child.ID, StepID: "ask", RequestID: "old-request", Descriptor: desc})
			require.NoError(t, err)
			if mode == "answered-result-written" || mode == "accepted-answer-with-canceled-result" {
				answer, _ := json.Marshal(question.DurableAnswer{Answers: [][]string{{"Yes"}}})
				_, _, err = a.FlowRuns.AnswerQuestion(ctx, stored.ID, q.ID, "answer-one", answer)
				require.NoError(t, err)
			}
			if mode != "pending-crash" {
				_, err = a.Messages.Create(ctx, child.ID, message.CreateMessageParams{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "question-call", Name: tools.QuestionToolName, Content: "closed tool", IsError: mode == "canceled-result-written" || mode == "accepted-answer-with-canceled-result"}}})
				require.NoError(t, err)
			}
			events := a.Questions.Subscribe(ctx)
			run := &dynamicRun{record: &stored, lease: lease}
			result := make(chan error, 1)
			go func() { result <- a.restoreFlowQuestions(ctx, run) }()
			if mode == "pending-crash" {
				select {
				case event := <-events:
					require.Equal(t, "question-call", event.Payload.ToolCallID)
					answer, _ := json.Marshal(question.DurableAnswer{Answers: [][]string{{"Yes"}}})
					_, _, err = a.FlowRuns.AnswerQuestion(ctx, stored.ID, q.ID, "late-answer", answer)
					require.NoError(t, err)
					require.NoError(t, a.Questions.Reply(event.Payload.ID, [][]string{{"Yes"}}))
				case <-ctx.Done():
					t.Fatal("pending question was not restored")
				}
			}
			require.NoError(t, <-result)
			current, err := a.FlowRuns.GetQuestion(ctx, stored.ID, q.ID)
			require.NoError(t, err)
			require.Equal(t, "delivered", current.Status)
			messages, err := a.Messages.List(ctx, child.ID)
			require.NoError(t, err)
			results := 0
			for _, m := range messages {
				for _, r := range m.ToolResults() {
					if r.ToolCallID == "question-call" {
						results++
					}
				}
			}
			require.Equal(t, 1, results)
			if mode == "accepted-answer-with-canceled-result" {
				supplemental := 0
				for _, m := range messages {
					if m.Role == message.User && strings.Contains(m.Content().String(), "durable user answer") {
						supplemental++
						require.Contains(t, m.Content().String(), "Yes")
					}
				}
				require.Equal(t, 1, supplemental)
				require.NoError(t, a.restoreFlowQuestions(ctx, run))
				again, e := a.Messages.List(ctx, child.ID)
				require.NoError(t, e)
				require.Len(t, again, len(messages))
			}
			_, _, err = a.FlowRuns.AnswerQuestion(ctx, stored.ID, q.ID, "unrelated-late", json.RawMessage(`{"answers":[["No"]]}`))
			require.Error(t, err)
		})
	}
}

func TestStoredFlowDeleteTreeFromOrdinaryChildDeletesDurableRoot(t *testing.T) {
	a, _, root, home, ctx := newStoredWorkflow(t)
	writeNamedWorkflow(t, home)
	result, err := a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "run", FlowID: "existing", Args: map[string]any{"goal": "descendant deletion"}})
	require.NoError(t, err)
	waitWorkflowIdle(t, ctx, a, root)
	child, err := a.Sessions.CreateTaskSession(ctx, "delete-tree-child", root, "ordinary child")
	require.NoError(t, err)
	require.NoError(t, a.Sessions.DeleteTree(ctx, child.ID))
	_, err = a.FlowRuns.Get(ctx, result.RunID)
	require.ErrorIs(t, err, flowrun.ErrNotFound)
	_, err = a.Sessions.Get(ctx, root)
	require.ErrorIs(t, err, flowrun.ErrNotFound)
}

func TestNewAppSharesDerivedProjectWithDurableFlows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("OPENCODE_DISABLE_CRON", "1")
	config.Reset()
	viper.Reset()
	registry.InvalidateRegistry()
	t.Cleanup(func() { config.Reset(); viper.Reset(); registry.InvalidateRegistry() })
	_, err := config.Load(home, false)
	require.NoError(t, err)
	cfg := config.Get()
	cfg.WorkingDir = home
	cfg.MCPServers = map[string]config.MCPServer{}
	cfg.Providers[models.ProviderAnthropic] = config.Provider{APIKey: "test-key"}
	for id, entry := range cfg.Agents {
		entry.Model = models.Claude45Haiku
		cfg.Agents[id] = entry
	}
	database, err := db.NewSQLiteProvider(filepath.Join(home, "actual-app")).Connect()
	require.NoError(t, err)
	defer database.Close()
	goose.SetBaseFS(db.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.Up(database, "migrations/sqlite"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := New(ctx, database, nil, "")
	require.NoError(t, err)
	defer a.Shutdown()
	for _, agent := range a.PrimaryAgents {
		agent.Tools()
	}
	root, err := a.Sessions.Create(ctx, "actual constructor")
	require.NoError(t, err)
	require.NotEmpty(t, root.ProjectID)
	require.Equal(t, root.ProjectID, a.FlowRuns.ProjectID())
	_, _, err = a.FlowRuns.Create(ctx, flowrun.Run{ConversationID: root.ID, Kind: "named", Status: "ready", InvocationID: "actual-constructor", Snapshot: json.RawMessage(`{}`), Origin: json.RawMessage(`{}`)})
	require.NoError(t, err)
}

func TestStoredNamedPromptPinningKeepsSteeringOnResume(t *testing.T) {
	a, actor, root, home, ctx := newStoredWorkflow(t)
	var requests atomic.Int32
	var version atomic.Int32
	version.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"name":"fixture/prompt","version":%d,"type":"text","prompt":"Remote v%d has no steering template"}`, version.Load(), version.Load())
	}))
	defer server.Close()
	langfuse.InitPrompts("test", "test", server.URL, langfuse.PromptOptions{CacheTTL: time.Millisecond, Timeout: time.Second})
	t.Cleanup(func() { langfuse.InitPrompts("", "", "", langfuse.PromptOptions{}) })
	path := filepath.Join(home, ".agents", "flows", "existing.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte("name: Remote\nflow:\n  steps:\n    - id: first\n      langfusePromptPath: fixture/prompt\n"), 0600))
	flow.Invalidate()
	actor.pause = true
	questions := a.Questions.Subscribe(ctx)
	result, err := a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "run", FlowID: "existing"})
	require.NoError(t, err)
	select {
	case q := <-questions:
		require.NoError(t, a.Questions.Reply(q.Payload.ID, [][]string{{"Yes"}}))
	case <-ctx.Done():
		t.Fatal("question absent")
	}
	select {
	case <-actor.started:
	case <-ctx.Done():
		t.Fatal("step absent")
	}
	require.NoError(t, a.QueueFlowMessages(ctx, root, []FlowSteeringMessage{{ID: "steer", Text: "User steering must reach remote prompt"}}))
	require.NoError(t, a.ControlFlow(ctx, root, "first", "cancel"))
	version.Store(2)
	require.NoError(t, os.Remove(path))
	flow.Invalidate()
	require.NoError(t, a.ResumeFlow(ctx, root))
	waitWorkflowIdle(t, ctx, a, root)
	final, err := a.CurrentFlow(ctx, root)
	require.NoError(t, err)
	require.Equal(t, "completed", final.Status)
	actor.mu.Lock()
	calls := strings.Join(actor.calls, "\n")
	actor.mu.Unlock()
	require.Contains(t, calls, "Remote v1")
	require.NotContains(t, calls, "Remote v2")
	require.Contains(t, calls, "User steering must reach remote prompt")
	require.Equal(t, int32(1), requests.Load())
	pinned, err := a.FlowRuns.GetPrompt(ctx, result.RunID, "first")
	require.NoError(t, err)
	require.Contains(t, string(pinned), `"version":1`)
	require.NotContains(t, string(pinned), "User steering must reach")
}

func TestStoredNamedRecoveryRehydratesOnlySecretArguments(t *testing.T) {
	a, actor, root, home, ctx := newStoredWorkflow(t)
	config.Get().MCPServers = map[string]config.MCPServer{"credential-server": {FlowArgHeaders: map[string]string{"X-Auth": "credential"}}}
	path := writeNamedWorkflow(t, home)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	body := strings.Replace(string(data), "goal: {type: string}", "goal: {type: string}\n      credential: {type: string}", 1)
	body = strings.Replace(body, "required: [goal]", "required: [goal, credential]", 1)
	require.NoError(t, os.WriteFile(path, []byte(body), 0600))
	flow.Invalidate()
	actor.pause = true
	questions := a.Questions.Subscribe(ctx)
	result, err := a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "run", FlowID: "existing", Args: map[string]any{"goal": "original goal", "credential": "secret-before"}, InvocationID: "secret-request"})
	require.NoError(t, err)
	select {
	case q := <-questions:
		require.NoError(t, a.Questions.Reply(q.Payload.ID, [][]string{{"Yes"}}))
	case <-ctx.Done():
		t.Fatal("question absent")
	}
	select {
	case <-actor.started:
	case <-ctx.Done():
		t.Fatal("step absent")
	}
	require.NoError(t, a.ControlFlow(ctx, root, "first", "cancel"))
	stored, err := a.FlowRuns.Get(ctx, result.RunID)
	require.NoError(t, err)
	require.NotContains(t, string(stored.Snapshot), "secret-before")
	require.NotContains(t, string(stored.Origin), "secret-before")
	resumed := tools.WithJobArgs(ctx, map[string]any{"goal": "untrusted replacement", "credential": "secret-after"})
	require.NoError(t, a.ResumeFlow(resumed, root))
	waitWorkflowIdle(t, ctx, a, root)
	final, err := a.CurrentFlow(ctx, root)
	require.NoError(t, err)
	require.Equal(t, "completed", final.Status)
	actor.mu.Lock()
	calls := strings.Join(actor.calls, "\n")
	actor.mu.Unlock()
	require.Contains(t, calls, "original goal")
	require.NotContains(t, calls, "untrusted replacement")
	require.NotContains(t, calls, "secret-after")
	rows, err := a.flowQuery.ListFlowStatesByRootSession(ctx, result.RunID)
	require.NoError(t, err)
	for _, row := range rows {
		require.NotContains(t, row.Args.String, "secret-after")
	}
}

func TestStoredInteractionQueueSurvivesCrashBeforeQuestionUnanswered(t *testing.T) {
	a, _, root, _, ctx := newStoredWorkflow(t)
	record, _, err := a.FlowRuns.Create(ctx, flowrun.Run{ID: "interruption-run", ConversationID: root, Kind: "generated", Status: "running", InvocationID: "interruption", Snapshot: json.RawMessage(`{}`), Origin: json.RawMessage(`{}`)})
	require.NoError(t, err)
	lease, err := a.FlowRuns.Claim(ctx, record.ID, "recovery-owner", time.Minute)
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.FlowRuns.Release(context.Background(), lease, "") })
	step, err := a.Sessions.CreateFlowSession(ctx, "interruption-step", root, "await input")
	require.NoError(t, err)
	_, err = a.Messages.Create(ctx, step.ID, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "interrupted-call", Name: tools.QuestionToolName, Input: `{"questions":[]}`, Finished: true}}})
	require.NoError(t, err)
	descriptor, _ := json.Marshal(question.DurableDescriptor{ToolCallID: "interrupted-call", Questions: []question.Prompt{{Question: "Continue?"}}})
	_, err = a.FlowRuns.PutQuestion(ctx, lease, flowrun.Question{ID: "interrupted-question", SessionID: step.ID, RequestID: "old-widget", Descriptor: descriptor})
	require.NoError(t, err)
	input := FlowSteeringMessage{ID: "directions-1", Text: "Change the goal before continuing", TargetSessionID: step.ID, TargetStepID: "await", TargetQuestionID: "interrupted-question"}
	payload, _ := json.Marshal(input)
	_, err = a.FlowRuns.QueueInboxBatch(ctx, record.ID, []flowrun.Inbox{{ID: "queued-before-crash", Payload: payload}})
	require.NoError(t, err)
	// No Unanswered call happened: worker died immediately after the batch commit.
	run := &dynamicRun{record: &record, lease: lease}
	recoveryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	require.NoError(t, a.restoreFlowQuestions(recoveryCtx, run))
	history, err := a.Messages.List(ctx, step.ID)
	require.NoError(t, err)
	require.Len(t, history, 3)
	require.Equal(t, message.Tool, history[1].Role)
	require.Contains(t, history[1].ToolResults()[0].Content, "new directions")
	require.Equal(t, message.User, history[2].Role)
	require.Equal(t, input.Text, history[2].Content().String())
	rootHistory, e := a.Messages.List(ctx, root)
	require.NoError(t, e)
	require.Len(t, rootHistory, 1)
	require.Equal(t, input.Text, rootHistory[0].Content().String())
	pending, err := a.FlowRuns.PeekInbox(ctx, record.ID)
	require.NoError(t, err)
	require.Empty(t, pending)
	_, _, err = a.FlowRuns.AnswerQuestion(ctx, record.ID, "interrupted-question", "late-widget-click", json.RawMessage(`{"answers":[["Yes"]]}`))
	require.Error(t, err)
	require.NoError(t, a.restoreFlowQuestions(ctx, run))
	again, err := a.Messages.List(ctx, step.ID)
	require.NoError(t, err)
	require.Len(t, again, 3)
}

func TestStoredAdmissionChecksOriginBeforeLeaseClaim(t *testing.T) {
	a, _, root, _, ctx := newStoredWorkflow(t)
	origin, _ := json.Marshal(flowOrigin{Peer: tools.Peer{Channel: "external", Identity: "agent", PeerID: "tenant-one"}})
	record, _, err := a.FlowRuns.Create(ctx, flowrun.Run{ID: "admission-run", ConversationID: root, Kind: "generated", Status: "suspended", InvocationID: "admission", Snapshot: json.RawMessage(`{}`), Origin: origin})
	require.NoError(t, err)
	bad := tools.WithPeer(ctx, tools.Peer{Channel: "external", Identity: "agent", PeerID: "tenant-two"})
	_, err = a.ReserveFlowAdmission(bad, root)
	require.ErrorIs(t, err, ErrFlowPermission)
	good := tools.WithPeer(ctx, tools.Peer{Channel: "external", Identity: "agent", PeerID: "tenant-one"})
	release, err := a.ReserveFlowAdmission(good, root)
	require.NoError(t, err)
	_, err = a.FlowRuns.Claim(ctx, record.ID, "foreign", time.Minute)
	require.ErrorIs(t, err, flowrun.ErrBusy)
	release()
	lease, err := a.FlowRuns.Claim(ctx, record.ID, "foreign", time.Minute)
	require.NoError(t, err)
	require.NoError(t, a.FlowRuns.Release(ctx, lease, ""))
}

func TestStoredNamedPrefixIsScopedToEachConversation(t *testing.T) {
	a, _, root, home, ctx := newStoredWorkflow(t)
	path := writeNamedWorkflow(t, home)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	source := strings.Replace(string(data), "flow:\n", "flow:\n  session:\n    prefix: shared-authored-prefix\n", 1)
	require.NoError(t, os.WriteFile(path, []byte(source), 0600))
	flow.Invalidate()
	first, err := a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "run", FlowID: "existing", Args: map[string]any{"goal": "one"}})
	require.NoError(t, err)
	waitWorkflowIdle(t, ctx, a, root)
	other, err := a.Sessions.Create(ctx, "another conversation")
	require.NoError(t, err)
	second, err := a.SubmitFlow(ctx, other.ID, tools.FlowRequest{Operation: "run", FlowID: "existing", Args: map[string]any{"goal": "two"}})
	require.NoError(t, err)
	waitWorkflowIdle(t, ctx, a, other.ID)
	one, err := a.DynamicFlowSnapshot(ctx, root)
	require.NoError(t, err)
	two, err := a.DynamicFlowSnapshot(ctx, other.ID)
	require.NoError(t, err)
	require.NotEqual(t, first.RunID, second.RunID)
	require.NotEqual(t, one.Steps[0].SessionID, two.Steps[0].SessionID)
	stored, err := a.FlowRuns.Get(ctx, first.RunID)
	require.NoError(t, err)
	var saved storedFlowDefinition
	require.NoError(t, json.Unmarshal(stored.Snapshot, &saved))
	require.Equal(t, "shared-authored-prefix", saved.Manifest.Definition.Spec.Session.Prefix)
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, source, string(actual))
}

func TestStoredAdmissionCannotBypassLegacyRunningFlow(t *testing.T) {
	a, actor, root, _, ctx := newStoredWorkflow(t)
	legacy := flow.DynamicManifest{RootSessionID: root, Agent: "coder", Complexity: "S", Definition: flow.Flow{ID: "legacy", Name: "Legacy", Spec: flow.FlowSpec{Steps: []flow.Step{{ID: "work", Agent: "coder", Prompt: "work"}}}}}
	dir, err := flow.DynamicDirectory()
	require.NoError(t, err)
	require.NoError(t, flow.SaveDynamic(dir, legacy))
	child, err := a.Sessions.CreateFlowSession(ctx, root+"-legacy-work", root, "legacy work")
	require.NoError(t, err)
	_, err = a.flowQuery.CreateFlowState(ctx, db.CreateFlowStateParams{SessionID: child.ID, RootSessionID: root, FlowID: "legacy", StepID: "work", Status: "running", Iteration: 1})
	require.NoError(t, err)
	_, err = a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "plan", Goal: "must not overlap"})
	require.ErrorIs(t, err, flowrun.ErrBusy)
	runs, err := a.FlowRuns.List(ctx, root)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, root, runs[0].ID)
	actor.mu.Lock()
	calls := append([]string(nil), actor.calls...)
	actor.mu.Unlock()
	require.Empty(t, calls)
}

func TestStoredMySQLLegacyRunningImportRequiresOwnerReconciliation(t *testing.T) {
	a, _, root, _, ctx := newStoredWorkflow(t)
	legacy := flow.DynamicManifest{RootSessionID: root, Agent: "coder", Standalone: true, Definition: flow.Flow{ID: "legacy", Name: "Legacy", Spec: flow.FlowSpec{Steps: []flow.Step{{ID: "work", Agent: "coder", Prompt: "work"}}}}}
	dir, err := flow.DynamicDirectory()
	require.NoError(t, err)
	require.NoError(t, flow.SaveDynamic(dir, legacy))
	_, err = a.flowQuery.CreateFlowState(ctx, db.CreateFlowStateParams{SessionID: root, RootSessionID: root, FlowID: "legacy", StepID: "work", Status: "running", Iteration: 1})
	require.NoError(t, err)
	original := a.FlowRuns
	// No connection supplied: refusal must precede creating a new ownership row.
	a.FlowRuns = flowrun.New(nil, a.flowQuery, "stored-flow-test", "mysql")
	_, err = a.importLegacyFlow(ctx, root)
	require.ErrorIs(t, err, ErrFlowPermission)
	a.FlowRuns = original
	runs, err := original.List(ctx, root)
	require.NoError(t, err)
	require.Empty(t, runs)
}

func TestStoredExpiredDeadlineCanBeTerminatedWithoutExecution(t *testing.T) {
	a, _, root, home, ctx := newStoredWorkflow(t)
	writeNamedWorkflow(t, home)
	stored, _, err := a.FlowRuns.Create(ctx, flowrun.Run{ID: "expired-run", ConversationID: root, Kind: "named", Status: "running", InvocationID: "expired", Deadline: time.Now().Add(-time.Minute).UnixMilli(), Snapshot: json.RawMessage(`{}`), Origin: json.RawMessage(`{}`)})
	require.NoError(t, err)
	require.NoError(t, a.ControlFlow(ctx, root, "", "terminate"))
	old, err := a.FlowRuns.Get(ctx, stored.ID)
	require.NoError(t, err)
	require.Equal(t, "terminated", old.Status)
	next, err := a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "run", FlowID: "existing", Args: map[string]any{"goal": "after expiry"}})
	require.NoError(t, err)
	require.NotEqual(t, stored.ID, next.RunID)
	waitWorkflowIdle(t, ctx, a, root)
}

func TestStoredPlannerSteeringKeepsToolResultBeforeRootUserMessage(t *testing.T) {
	for _, planner := range []bool{false, true} {
		name := "ordinary-submitting-turn"
		if planner {
			name = "controller-planner"
		}
		t.Run(name, func(t *testing.T) {
			a, _, root, _, ctx := newStoredWorkflow(t)
			record, _, err := a.FlowRuns.Create(ctx, flowrun.Run{ID: "root-planner-run", ConversationID: root, Kind: "generated", Status: "planning", InvocationID: "root-planner", Snapshot: json.RawMessage(`{}`), Origin: json.RawMessage(`{}`)})
			require.NoError(t, err)
			lease, err := a.FlowRuns.Claim(ctx, record.ID, "root-planner-owner", time.Minute)
			require.NoError(t, err)
			run := &dynamicRun{record: &record, lease: lease, cancel: func() {}, done: make(chan struct{})}
			a.dynamicMu.Lock()
			a.dynamicRuns = map[string]*dynamicRun{root: run}
			a.dynamicMu.Unlock()
			defer func() {
				a.dynamicMu.Lock()
				delete(a.dynamicRuns, root)
				a.dynamicMu.Unlock()
				close(run.done)
				_ = a.FlowRuns.Release(context.Background(), lease, "")
			}()
			_, err = a.Messages.Create(ctx, root, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "root-question-call", Name: tools.QuestionToolName, Finished: true}}})
			require.NoError(t, err)
			requests := a.Questions.Subscribe(ctx)
			questionCtx := question.WithToolCallID(ctx, "root-question-call")
			if planner {
				questionCtx = question.WithFlowRunID(questionCtx, record.ID)
			}
			answer := make(chan error, 1)
			go func() {
				_, e := a.Questions.Ask(questionCtx, root, []question.Prompt{{Question: "Need details?"}})
				answer <- e
			}()
			var pending question.Request
			select {
			case event := <-requests:
				pending = event.Payload
			case <-ctx.Done():
				t.Fatal("question missing")
			}
			require.NoError(t, a.QueueFlowMessages(ctx, root, []FlowSteeringMessage{{ID: "planner-steer", Text: "Use these new details"}}))
			accepted, err := a.Messages.List(ctx, root)
			require.NoError(t, err)
			require.Len(t, accepted, 1, "acceptance must not insert a user message before the pending tool result")
			if !planner {
				select {
				case <-answer:
					t.Fatal("controller interrupted ordinary submitting-turn question")
				default:
				}
				require.NoError(t, a.Questions.Reply(pending.ID, [][]string{{"Yes"}}))
				require.NoError(t, <-answer)
				return
			}
			err = <-answer
			var unanswered *question.UnansweredError
			require.ErrorAs(t, err, &unanswered)
			_, err = a.Messages.Create(ctx, root, message.CreateMessageParams{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "root-question-call", Name: tools.QuestionToolName, Content: unanswered.Note}}})
			require.NoError(t, err)
			drained, err := a.drainStoredInteraction(ctx, run, root)
			require.NoError(t, err)
			require.Len(t, drained, 1)
			history, err := a.Messages.List(ctx, root)
			require.NoError(t, err)
			require.Len(t, history, 3)
			require.Equal(t, message.Tool, history[1].Role)
			require.Equal(t, message.User, history[2].Role)
			require.Equal(t, "Use these new details", history[2].Content().String())
			again, err := a.drainStoredInteraction(ctx, run, root)
			require.NoError(t, err)
			require.Empty(t, again)
			require.NoError(t, a.QueueFlowMessages(ctx, root, []FlowSteeringMessage{{ID: "planner-steer", Text: "Use these new details"}}))
			final, err := a.Messages.List(ctx, root)
			require.NoError(t, err)
			require.Len(t, final, 3)
		})
	}
}

func TestStoredTurnAdmissionBlocksUnownedRootButAllowsManagement(t *testing.T) {
	a, _, root, _, ctx := newStoredWorkflow(t)
	record, _, err := a.FlowRuns.Create(ctx, flowrun.Run{ID: "turn-owner-run", ConversationID: root, Kind: "generated", Status: "running", InvocationID: "turn-owner", Snapshot: json.RawMessage(`{}`), Origin: json.RawMessage(`{}`)})
	require.NoError(t, err)
	require.ErrorIs(t, a.admitSessionTurn(ctx, root), agentpkg.ErrSessionBusy)
	lease, err := a.FlowRuns.Claim(ctx, record.ID, "owner", time.Minute)
	require.NoError(t, err)
	defer a.FlowRuns.Release(context.Background(), lease, "")
	owned := tools.WithFlowOwnershipGuard(ctx, func(ctx context.Context) error { return a.FlowRuns.CheckLease(ctx, lease) })
	require.NoError(t, a.admitSessionTurn(owned, root))
	require.NoError(t, a.FlowRuns.Transition(ctx, lease, "suspended"))
	require.NoError(t, a.admitSessionTurn(ctx, root))
}

type configuredStoredRegistry struct {
	storedWorkflowRegistry
	size string
}

func (r configuredStoredRegistry) Get(id string) (registry.AgentInfo, bool) {
	info, ok := r.storedWorkflowRegistry.Get(id)
	info.DynamicFlow = &config.DynamicFlow{Enabled: true, FlowComplexity: r.size}
	return info, ok
}

type plannerBudgetObservation struct {
	turns, taskTurns, parallel int
	runID                      string
}
type inspectedPlanner struct {
	storedPlanner
	observed chan plannerBudgetObservation
}

func (p inspectedPlanner) RunWith(ctx context.Context, id, prompt string, maxTurns int, opts agentpkg.RunOptions, atts ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	if strings.HasPrefix(prompt, "Build a dynamic workflow") {
		observation := plannerBudgetObservation{turns: maxTurns, taskTurns: tools.FlowTaskTurns(ctx), runID: question.FlowRunIDFromContext(ctx)}
		var releases []func()
		for i := 0; i < 5; i++ {
			release, ok := tools.AcquireFlowTask(ctx)
			if ok {
				observation.parallel++
				releases = append(releases, release)
			}
		}
		for _, release := range releases {
			release()
		}
		p.observed <- observation
	}
	return p.storedPlanner.RunWith(ctx, id, prompt, maxTurns, opts, atts...)
}
func TestStoredPlannerBudgetsApplyBeforeDefinitionExists(t *testing.T) {
	for _, tc := range []struct {
		size            string
		turns, parallel int
	}{{"XS", 20, 1}, {"", 80, 3}} {
		t.Run("size-"+tc.size, func(t *testing.T) {
			a, actor, root, home, ctx := newStoredWorkflow(t)
			a.Registry = configuredStoredRegistry{size: tc.size}
			observed := make(chan plannerBudgetObservation, 1)
			a.PrimaryAgents[config.AgentCoder] = inspectedPlanner{storedPlanner{actor}, observed}
			require.NoError(t, os.WriteFile(filepath.Join(home, "openspec"), []byte("#!/bin/sh\nexit 0\n"), 0700))
			t.Setenv("PATH", home)
			result, err := a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "plan", Goal: "bounded planner"})
			require.NoError(t, err)
			var got plannerBudgetObservation
			select {
			case got = <-observed:
			case <-ctx.Done():
				t.Fatal("planner not invoked")
			}
			require.Equal(t, tc.turns, got.turns)
			require.Equal(t, tc.turns, got.taskTurns)
			require.Equal(t, tc.parallel, got.parallel)
			require.Equal(t, result.RunID, got.runID)
			waitWorkflowIdle(t, ctx, a, root)
		})
	}
}

func TestStoredQueueAcceptanceSurvivesQuestionWakeupFailure(t *testing.T) {
	a, _, root, _, ctx, database := newStoredWorkflowDB(t)
	record, _, err := a.FlowRuns.Create(ctx, flowrun.Run{ID: "wakeup-failure", ConversationID: root, Kind: "generated", Status: "planning", InvocationID: "wakeup", Snapshot: json.RawMessage(`{}`), Origin: json.RawMessage(`{}`)})
	require.NoError(t, err)
	lease, err := a.FlowRuns.Claim(ctx, record.ID, "old-owner", time.Minute)
	require.NoError(t, err)
	var canceled atomic.Bool
	run := &dynamicRun{record: &record, lease: lease, cancel: func() { canceled.Store(true) }, done: make(chan struct{})}
	a.dynamicMu.Lock()
	a.dynamicRuns = map[string]*dynamicRun{root: run}
	a.dynamicMu.Unlock()
	defer func() { a.dynamicMu.Lock(); delete(a.dynamicRuns, root); a.dynamicMu.Unlock(); close(run.done) }()
	requests := a.Questions.Subscribe(ctx)
	questionCtx := question.WithFlowRunID(question.WithToolCallID(ctx, "wakeup-call"), record.ID)
	answer := make(chan error, 1)
	go func() {
		_, e := a.Questions.Ask(questionCtx, root, []question.Prompt{{Question: "Need more?"}})
		answer <- e
	}()
	var pending question.Request
	select {
	case event := <-requests:
		pending = event.Payload
	case <-ctx.Done():
		t.Fatal("no question")
	}
	defer func() { _ = a.Questions.Reject(pending.ID); <-answer }()
	// Simulate ownership changing exactly when enqueue commits, before wakeup.
	_, err = database.Exec(`CREATE TRIGGER replace_owner_after_inbox AFTER INSERT ON conversation_flow_inbox BEGIN UPDATE conversation_flow_runs SET lease_token=lease_token+1,lease_owner='replacement' WHERE id=NEW.run_id; END`)
	require.NoError(t, err)
	input := []FlowSteeringMessage{{ID: "accepted-once", Text: "Keep this accepted direction"}}
	require.NoError(t, a.QueueFlowMessages(ctx, root, input))
	require.True(t, canceled.Load())
	queued, err := a.FlowRuns.PeekInbox(ctx, record.ID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	require.NoError(t, a.QueueFlowMessages(ctx, root, input))
	again, err := a.FlowRuns.PeekInbox(ctx, record.ID)
	require.NoError(t, err)
	require.Len(t, again, 1)
}

func TestStoredSteeringTargetsVisibleQuestionRatherThanSortedHiddenRequest(t *testing.T) {
	a, _, root, _, ctx := newStoredWorkflow(t)
	record, _, err := a.FlowRuns.Create(ctx, flowrun.Run{ID: "visible-questions", ConversationID: root, Kind: "generated", Status: "running", InvocationID: "visible-questions", Snapshot: json.RawMessage(`{}`), Origin: json.RawMessage(`{}`)})
	require.NoError(t, err)
	lease, err := a.FlowRuns.Claim(ctx, record.ID, "visible-owner", time.Minute)
	require.NoError(t, err)
	run := &dynamicRun{record: &record, lease: lease, cancel: func() {}, done: make(chan struct{})}
	a.dynamicMu.Lock()
	a.dynamicRuns = map[string]*dynamicRun{root: run}
	a.dynamicMu.Unlock()
	defer func() {
		a.dynamicMu.Lock()
		delete(a.dynamicRuns, root)
		a.dynamicMu.Unlock()
		close(run.done)
		_ = a.FlowRuns.Release(context.Background(), lease, "")
	}()
	requests := a.Questions.Subscribe(ctx)
	type waiting struct {
		request question.Request
		done    chan error
	}
	ask := func(id string) waiting {
		child, err := a.Sessions.CreateFlowSession(ctx, id, root, id)
		require.NoError(t, err)
		_, err = a.flowQuery.CreateFlowState(ctx, db.CreateFlowStateParams{SessionID: child.ID, RootSessionID: record.ID, FlowID: "parallel", StepID: id, Status: "running", Iteration: 1})
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() {
			defer close(done)
			_, err := a.Questions.Ask(question.WithFlowRunID(question.WithToolCallID(ctx, id+"-call"), record.ID), child.ID, []question.Prompt{{Question: id}})
			done <- err
		}()
		select {
		case event := <-requests:
			return waiting{event.Payload, done}
		case <-ctx.Done():
			t.Fatal("question missing")
			return waiting{}
		}
	}
	// Arrival order makes Z visible; the old sort incorrectly chose hidden A.
	visible := ask("z-visible")
	hidden := ask("a-hidden")
	defer func() { _ = a.Questions.Reject(hidden.request.ID); <-hidden.done }()
	hinted := question.WithSteeringQuestion(ctx, visible.request.ID)
	require.NoError(t, a.QueueFlowMessages(hinted, root, []FlowSteeringMessage{{ID: "visible-steering", Text: "Change the visible task"}}))
	var unanswered *question.UnansweredError
	require.ErrorAs(t, <-visible.done, &unanswered)
	select {
	case <-hidden.done:
		t.Fatal("hidden question was interrupted")
	default:
	}
	queued, err := a.FlowRuns.PeekInbox(ctx, record.ID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	var input FlowSteeringMessage
	require.NoError(t, json.Unmarshal(queued[0].Payload, &input))
	require.Equal(t, visible.request.SessionID, input.TargetSessionID)
	// A widget can settle between the adapter snapshot and enqueue. Its old hint
	// must queue ordinary boundary input, never retarget the remaining hidden ask.
	require.NoError(t, a.QueueFlowMessages(hinted, root, []FlowSteeringMessage{{ID: "stale-visible-steering", Text: "Follow-up after visible question closed"}}))
	queued, err = a.FlowRuns.PeekInbox(ctx, record.ID)
	require.NoError(t, err)
	require.Len(t, queued, 2)
	input = FlowSteeringMessage{}
	require.NoError(t, json.Unmarshal(queued[1].Payload, &input))
	require.Empty(t, input.TargetSessionID)
	select {
	case <-hidden.done:
		t.Fatal("stale visible hint was retargeted to hidden question")
	default:
	}
}

func TestStoredQuestionStartConsumesInputAcceptedBeforeAsk(t *testing.T) {
	a, _, root, _, ctx := newStoredWorkflow(t)
	record, _, err := a.FlowRuns.Create(ctx, flowrun.Run{ID: "before-question", ConversationID: root, Kind: "generated", Status: "running", InvocationID: "before-question", Snapshot: json.RawMessage(`{}`), Origin: json.RawMessage(`{}`)})
	require.NoError(t, err)
	lease, err := a.FlowRuns.Claim(ctx, record.ID, "before-owner", time.Minute)
	require.NoError(t, err)
	run := &dynamicRun{record: &record, lease: lease, cancel: func() {}, done: make(chan struct{})}
	a.dynamicMu.Lock()
	a.dynamicRuns = map[string]*dynamicRun{root: run}
	a.dynamicMu.Unlock()
	defer func() {
		a.dynamicMu.Lock()
		delete(a.dynamicRuns, root)
		a.dynamicMu.Unlock()
		close(run.done)
		_ = a.FlowRuns.Release(context.Background(), lease, "")
	}()
	child, err := a.Sessions.CreateFlowSession(ctx, "before-question-step", root, "before")
	require.NoError(t, err)
	_, err = a.flowQuery.CreateFlowState(ctx, db.CreateFlowStateParams{SessionID: child.ID, RootSessionID: record.ID, FlowID: "before", StepID: "before", Status: "running", Iteration: 1})
	require.NoError(t, err)
	// No question exists when the adapter accepts this input.
	require.NoError(t, a.QueueFlowMessages(question.WithSteeringQuestion(ctx, ""), root, []FlowSteeringMessage{{ID: "ahead", Text: "Use this direction before asking anything else"}}))
	_, err = a.Messages.Create(ctx, child.ID, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "before-call", Name: tools.QuestionToolName, Finished: true}}})
	require.NoError(t, err)
	requests := a.Questions.Subscribe(ctx)
	askCtx := question.WithFlowRunID(question.WithToolCallID(ctx, "before-call"), record.ID)
	askCtx = question.WithStartHook(askCtx, func(ctx context.Context, request question.Request) error {
		return a.startStoredQuestion(ctx, run, request)
	})
	_, err = a.Questions.Ask(askCtx, child.ID, []question.Prompt{{Question: "Obsolete question?"}})
	var unanswered *question.UnansweredError
	require.ErrorAs(t, err, &unanswered)
	select {
	case event := <-requests:
		t.Fatalf("settled question was published: %+v", event)
	default:
	}
	stored, err := a.FlowRuns.Questions(ctx, record.ID)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Equal(t, "interrupted", stored[0].Status)
	_, err = a.Messages.Create(ctx, child.ID, message.CreateMessageParams{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "before-call", Name: tools.QuestionToolName, Content: unanswered.Note}}})
	require.NoError(t, err)
	delivered, err := a.drainStoredInteraction(ctx, run, child.ID)
	require.NoError(t, err)
	require.Len(t, delivered, 1)
	require.Contains(t, delivered[0].Content().String(), "before asking anything else")
	pending, err := a.FlowRuns.PeekInbox(ctx, record.ID)
	require.NoError(t, err)
	require.Empty(t, pending)
}

package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/contextfile"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/flowrun"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/question"
)

type namedInteractionFactory struct {
	workflowFactory
	peers chan []bridge.PeerRef
}

func (f namedInteractionFactory) NewAgent(_ context.Context, _ string, _ map[string]any, _ string, interactive bool, peers []bridge.PeerRef, _ *contextfile.StepContext, _ contextfile.TemplateVars, _ agentpkg.ModelOverride) (agentpkg.Service, error) {
	if !interactive {
		return f.agent, nil
	}
	f.peers <- peers
	return namedInteractionAgent{workflowAgent: f.agent}, nil
}

type namedInteractionAgent struct{ *workflowAgent }

func (a namedInteractionAgent) RunWith(ctx context.Context, id, _ string, _ int, _ agentpkg.RunOptions, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	done := make(chan agentpkg.AgentEvent, 1)
	go func() {
		defer close(done)
		answers, err := a.question.Ask(ctx, id, []question.Prompt{{Question: "Proceed with the scoped work?", Options: []question.Option{{Label: "Proceed"}}}})
		if err != nil {
			done <- agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeError, Error: err}
			return
		}
		done <- agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeResponse, Message: message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "Reviewer answered: " + answers[0][0]}}}}
	}()
	return done, nil
}

func writeNamedInteractiveWorkflow(t *testing.T, home, schema string) string {
	t.Helper()
	path := filepath.Join(home, ".agents", "flows", "interactive.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	definition := "name: Interactive\nflow:\n" + schema + "  steps:\n    - id: prepare\n      prompt: Prepare scope\n      rules:\n        - then: clarify\n    - id: clarify\n      interactive: true\n      interaction:\n        target: ${args.reviewer}\n      prompt: Ask the reviewer\n"
	require.NoError(t, os.WriteFile(path, []byte(definition), 0600))
	flow.Invalidate()
	return path
}
func installNamedInteractionFactory(a *App, actor *workflowAgent) <-chan []bridge.PeerRef {
	peers := make(chan []bridge.PeerRef, 1)
	a.Flows = flow.NewService(a.Sessions, a.Messages, a.flowQuery, a.Permissions, namedInteractionFactory{workflowFactory{agent: actor}, peers})
	return peers
}
func finishNamedInteraction(t *testing.T, a *App, root string, ctx context.Context, requests <-chan pubsub.Event[question.Request]) question.Request {
	t.Helper()
	var req question.Request
	select {
	case event := <-requests:
		req = event.Payload
	case <-time.After(3 * time.Second):
		record, err := a.CurrentFlow(ctx, root)
		t.Fatalf("interactive question absent; run=%+v err=%v", record, err)
	}
	require.NotEqual(t, root, req.SessionID)
	conversation, owned, err := a.SessionFlowConversation(ctx, req.SessionID)
	require.NoError(t, err)
	require.True(t, owned)
	require.Equal(t, root, conversation)
	require.NoError(t, a.Questions.Reply(req.ID, [][]string{{"Proceed"}}))
	waitWorkflowIdle(t, ctx, a, root)
	record, err := a.CurrentFlow(ctx, root)
	require.NoError(t, err)
	require.Equal(t, "completed", record.Status)
	state, err := a.flowQuery.GetFlowState(ctx, req.SessionID)
	require.NoError(t, err)
	require.Contains(t, state.Output.String, "Reviewer answered: Proceed")
	return req
}
func TestStoredNamedInteractionInheritsConversation(t *testing.T) {
	for _, peer := range []tools.Peer{
		{Channel: "slack", Identity: "daemon", PeerID: "D123"},
		{Channel: "slack", Identity: "daemon", PeerID: "C123|123.456"},
		{Channel: "mattermost", Identity: "daemon", PeerID: "channel|thread"},
		{Channel: "telegram", Identity: "daemon", PeerID: "-1000123"},
		{Channel: "external", Identity: "relay", PeerID: "tenant/conversation"},
	} {
		t.Run(peer.Channel+"/"+peer.PeerID, func(t *testing.T) {
			a, actor, root, home, ctx := newStoredWorkflow(t)
			peers := installNamedInteractionFactory(a, actor)
			path := writeNamedInteractiveWorkflow(t, home, "  args:\n    type: object\n    properties:\n      reviewer: {type: object}\n    required: [reviewer]\n")
			source, err := os.ReadFile(path)
			require.NoError(t, err)
			requests := a.Questions.Subscribe(ctx)
			callerArgs := map[string]any{}
			req := tools.FlowRequest{Operation: "run", FlowID: "interactive", Args: callerArgs, InvocationID: "interactive-1"}
			ctx = tools.WithPeer(ctx, peer)
			result, err := a.SubmitFlow(ctx, root, req)
			require.NoError(t, err)
			finishNamedInteraction(t, a, root, ctx, requests)
			require.Equal(t, []bridge.PeerRef{{Channel: peer.Channel, Identity: peer.Identity, PeerID: peer.PeerID}}, <-peers)
			record, err := a.CurrentFlow(ctx, root)
			require.NoError(t, err)
			var saved storedFlowDefinition
			require.NoError(t, json.Unmarshal(record.Snapshot, &saved))
			require.Equal(t, map[string]any{"channel": peer.Channel, "identity": peer.Identity, "peerId": peer.PeerID}, saved.Args["reviewer"])
			require.Empty(t, callerArgs)
			again, err := a.SubmitFlow(ctx, root, req)
			require.NoError(t, err)
			require.Equal(t, result.RunID, again.RunID)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, source, after)
		})
	}
}

func TestStoredNamedInteractionPreservesExplicitTargets(t *testing.T) {
	origin := tools.Peer{Channel: "slack", Identity: "daemon", PeerID: "C123|111.222"}
	peer := map[string]any{"channel": origin.Channel, "identity": origin.Identity, "peerId": origin.PeerID}
	for _, tc := range []struct {
		name     string
		target   any
		accepted bool
	}{
		{"object", peer, true}, {"array", []any{peer}, true}, {"null", nil, false}, {"empty", map[string]any{}, false}, {"text", "reviewer", false}, {"empty array", []any{}, false},
		{"other thread", map[string]any{"channel": "slack", "identity": "daemon", "peerId": "C123|333.444"}, false},
		{"other identity", map[string]any{"channel": "slack", "identity": "other", "peerId": origin.PeerID}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, actor, root, home, ctx := newStoredWorkflow(t)
			peers := installNamedInteractionFactory(a, actor)
			writeNamedInteractiveWorkflow(t, home, "")
			requests := a.Questions.Subscribe(ctx)
			_, err := a.SubmitFlow(tools.WithPeer(ctx, origin), root, tools.FlowRequest{Operation: "run", FlowID: "interactive", Args: map[string]any{"reviewer": tc.target}})
			require.NoError(t, err)
			if tc.accepted {
				finishNamedInteraction(t, a, root, ctx, requests)
				<-peers
			} else {
				waitWorkflowIdle(t, ctx, a, root)
				record, err := a.CurrentFlow(ctx, root)
				require.NoError(t, err)
				require.Equal(t, "failed", record.Status)
				require.Empty(t, peers)
			}
			record, err := a.CurrentFlow(ctx, root)
			require.NoError(t, err)
			var saved storedFlowDefinition
			require.NoError(t, json.Unmarshal(record.Snapshot, &saved))
			require.Equal(t, tc.target, saved.Args["reviewer"])
		})
	}
}

func TestStoredNamedInteractionLegacyResumeUsesRecordedOrigin(t *testing.T) {
	for _, explicitOriginal := range []bool{false, true} {
		name := "missing original target"
		if explicitOriginal {
			name = "explicit original target"
		}
		t.Run(name, func(t *testing.T) {
			a, actor, root, home, ctx := newStoredWorkflow(t)
			peers := installNamedInteractionFactory(a, actor)
			path := writeNamedInteractiveWorkflow(t, home, "")
			definition, err := flow.Get("interactive")
			require.NoError(t, err)
			origin := tools.Peer{Channel: "slack", Identity: "daemon", PeerID: "C123|111.222"}
			other := tools.Peer{Channel: "slack", Identity: "daemon", PeerID: "C123|333.444"}
			originalArgs := map[string]any{}
			expectedPeer := origin
			if explicitOriginal {
				expectedPeer = other
				originalArgs["reviewer"] = map[string]any{"channel": other.Channel, "identity": other.Identity, "peerId": other.PeerID}
			}
			snapshot, err := json.Marshal(storedFlowDefinition{Manifest: flow.DynamicManifest{Agent: "coder", RootSessionID: root, Definition: *definition, Standalone: true}, Args: originalArgs})
			require.NoError(t, err)
			originJSON, err := json.Marshal(flowOrigin{Peer: origin, AuthorizedPeers: []tools.Peer{other}})
			require.NoError(t, err)
			record, _, err := a.FlowRuns.Create(ctx, flowrun.Run{ConversationID: root, InvocationID: "legacy-interactive", Kind: "named", Status: "failed", Snapshot: snapshot, Origin: originJSON})
			require.NoError(t, err)
			for _, step := range []struct{ id, status string }{{"prepare", "completed"}, {"clarify", "failed"}} {
				id := record.ID + "-interactive-" + step.id
				_, err = a.flowQuery.CreateSession(ctx, db.CreateSessionParams{ID: id, ProjectID: sql.NullString{String: a.flowProjectID, Valid: true}, RootSessionID: sql.NullString{String: root, Valid: true}, Title: step.id})
				require.NoError(t, err)
				_, err = a.flowQuery.CreateFlowState(ctx, db.CreateFlowStateParams{SessionID: id, RootSessionID: record.ID, FlowID: "interactive", StepID: step.id, Status: step.status, Args: sql.NullString{String: `{}`, Valid: true}, Iteration: 1})
				require.NoError(t, err)
			}
			require.NoError(t, os.Remove(path))
			flow.Invalidate()
			requests := a.Questions.Subscribe(ctx)
			require.NoError(t, a.ResumeFlow(tools.WithPeer(ctx, other), root))
			request := finishNamedInteraction(t, a, root, ctx, requests)
			require.Equal(t, []bridge.PeerRef{{Channel: expectedPeer.Channel, Identity: expectedPeer.Identity, PeerID: expectedPeer.PeerID}}, <-peers)
			state, err := a.flowQuery.GetFlowState(ctx, request.SessionID)
			require.NoError(t, err)
			require.Contains(t, state.Args.String, expectedPeer.PeerID)
			actor.mu.Lock()
			calls := strings.Join(actor.calls, "\n")
			actor.mu.Unlock()
			require.NotContains(t, calls, "Prepare scope")
		})
	}
}

func TestStoredNamedInteractionDefaultsHonorArgumentSchema(t *testing.T) {
	for _, tc := range []struct {
		name, schema, want string
		noOrigin           bool
	}{
		{"required without origin", "  args:\n    type: object\n    properties:\n      reviewer: {type: object}\n    required: [reviewer]\n", `missing required argument "reviewer"`, true},
		{"declared incompatible type", "  args:\n    type: object\n    properties:\n      reviewer: {type: string}\n", `argument "reviewer" must be a string`, false},
		{"undeclared forbidden target", "  args:\n    type: object\n    properties: {}\n    additionalProperties: false\n", `unexpected argument "reviewer"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, actor, root, home, ctx := newStoredWorkflow(t)
			writeNamedInteractiveWorkflow(t, home, tc.schema)
			if !tc.noOrigin {
				ctx = tools.WithPeer(ctx, tools.Peer{Channel: "slack", Identity: "daemon", PeerID: "D123"})
			}
			_, err := a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "run", FlowID: "interactive"})
			require.ErrorContains(t, err, tc.want)
			_, err = a.CurrentFlow(ctx, root)
			require.ErrorIs(t, err, flowrun.ErrNotFound)
			actor.mu.Lock()
			defer actor.mu.Unlock()
			require.Empty(t, actor.calls)
		})
	}
}

func TestStoredNamedInteractionWithoutOriginDoesNotInventTarget(t *testing.T) {
	a, actor, root, home, ctx := newStoredWorkflow(t)
	peers := installNamedInteractionFactory(a, actor)
	writeNamedInteractiveWorkflow(t, home, "")
	_, err := a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "run", FlowID: "interactive"})
	require.NoError(t, err)
	waitWorkflowIdle(t, ctx, a, root)
	record, err := a.CurrentFlow(ctx, root)
	require.NoError(t, err)
	require.Equal(t, "failed", record.Status)
	require.Empty(t, peers)
	var saved storedFlowDefinition
	require.NoError(t, json.Unmarshal(record.Snapshot, &saved))
	require.Empty(t, saved.Args)
}

func TestStoredNamedInteractionUsesReferencedArgumentName(t *testing.T) {
	a, actor, root, home, ctx := newStoredWorkflow(t)
	peers := installNamedInteractionFactory(a, actor)
	path := writeNamedInteractiveWorkflow(t, home, "")
	source, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(string(source), "args.reviewer", "args.scope_owner")), 0600))
	flow.Invalidate()
	requests := a.Questions.Subscribe(ctx)
	_, err = a.SubmitFlow(tools.WithPeer(ctx, tools.Peer{Channel: "slack", Identity: "daemon", PeerID: "D123"}), root, tools.FlowRequest{Operation: "run", FlowID: "interactive"})
	require.NoError(t, err)
	finishNamedInteraction(t, a, root, ctx, requests)
	<-peers
	record, err := a.CurrentFlow(ctx, root)
	require.NoError(t, err)
	var saved storedFlowDefinition
	require.NoError(t, json.Unmarshal(record.Snapshot, &saved))
	require.Contains(t, saved.Args, "scope_owner")
	require.NotContains(t, saved.Args, "reviewer")
}

func TestStoredNamedInteractionDoesNotDefaultMalformedReferences(t *testing.T) {
	for _, target := range []string{"reviewer", "${args.}", "${args.reviewer} ${args.other}", "${args.reviewer", "prefix ${args.reviewer}"} {
		t.Run(target, func(t *testing.T) {
			a, _, root, home, ctx := newStoredWorkflow(t)
			path := writeNamedInteractiveWorkflow(t, home, "")
			source, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(string(source), "${args.reviewer}", target)), 0600))
			flow.Invalidate()
			_, err = a.SubmitFlow(tools.WithPeer(ctx, tools.Peer{Channel: "slack", Identity: "daemon", PeerID: "D123"}), root, tools.FlowRequest{Operation: "run", FlowID: "interactive"})
			require.NoError(t, err)
			waitWorkflowIdle(t, ctx, a, root)
			record, err := a.CurrentFlow(ctx, root)
			require.NoError(t, err)
			require.Equal(t, "failed", record.Status)
			var saved storedFlowDefinition
			require.NoError(t, json.Unmarshal(record.Snapshot, &saved))
			require.Empty(t, saved.Args)
		})
	}
}

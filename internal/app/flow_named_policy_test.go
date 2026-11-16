package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	registry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/flowrun"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

type namedPolicyRegistry struct {
	storedWorkflowRegistry
	policies map[string]*[]string
}

func (r namedPolicyRegistry) Get(id string) (registry.AgentInfo, bool) {
	info, ok := r.storedWorkflowRegistry.Get(id)
	info.DynamicFlow.NamedFlows = r.policies[id]
	return info, ok
}
func TestStoredNamedFlowAllowlistAdmission(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ids     *[]string
		allowed bool
	}{
		{"omitted", nil, true}, {"empty", new([]string), false},
		{"exact", ptrFlowIDs("existing"), true}, {"different case", ptrFlowIDs("Existing"), false},
		{"no glob expansion", ptrFlowIDs("*"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, root, home, ctx := newStoredWorkflow(t)
			a.Registry = namedPolicyRegistry{policies: map[string]*[]string{"coder": tc.ids}}
			writeNamedWorkflow(t, home)
			listed, err := a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "list"})
			require.NoError(t, err)
			require.Equal(t, tc.allowed, len(listed.Flows) == 1)
			_, err = a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: "run", FlowID: "existing", Args: map[string]any{"goal": "test"}, InvocationID: "policy"})
			if tc.allowed {
				require.NoError(t, err)
				waitWorkflowIdle(t, ctx, a, root)
				return
			}
			require.ErrorIs(t, err, ErrFlowPermission)
			_, err = a.CurrentFlow(ctx, root)
			require.ErrorIs(t, err, flowrun.ErrNotFound)
			children, err := a.Sessions.ListChildren(ctx, root)
			require.NoError(t, err)
			require.Len(t, children, 1)
			require.Equal(t, root, children[0].ID)
		})
	}
}
func ptrFlowIDs(ids ...string) *[]string { return &ids }

func TestStoredNamedFlowPolicyRevocationSurvivesRecovery(t *testing.T) {
	a, actor, root, home, ctx := newStoredWorkflow(t)
	policies := map[string]*[]string{"coder": ptrFlowIDs("existing")}
	a.Registry = namedPolicyRegistry{policies: policies}
	source := writeNamedWorkflow(t, home)
	actor.pause = true
	requests := a.Questions.Subscribe(ctx)
	req := tools.FlowRequest{Operation: "run", FlowID: "existing", Args: map[string]any{"goal": "persisted"}, InvocationID: "persisted-policy"}
	_, err := a.SubmitFlow(ctx, root, req)
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
	require.NoError(t, a.ControlFlow(ctx, root, "first", "cancel"))
	before, err := a.CurrentFlow(ctx, root)
	require.NoError(t, err)
	require.NoError(t, os.Remove(source))
	flow.Invalidate()
	policies["coder"] = ptrFlowIDs()
	recovered := &App{ctx: ctx, Sessions: a.Sessions, Messages: a.Messages, Questions: a.Questions, Permissions: a.Permissions, Registry: a.Registry, PrimaryAgents: a.PrimaryAgents, PrimaryAgentKeys: a.PrimaryAgentKeys, flowQuery: a.flowQuery, FlowRuns: a.FlowRuns, flowProjectID: a.flowProjectID, Flows: a.Flows}
	t.Cleanup(func() { recovered.ShutdownFlows(time.Second) })
	// Switching callers must not borrow an unrestricted agent's policy.
	other := context.WithValue(ctx, tools.AgentIDContextKey, config.AgentName("other"))
	require.ErrorIs(t, recovered.ResumeFlow(other, root), ErrFlowPermission)
	require.ErrorIs(t, recovered.ControlFlow(other, root, "", "resume"), ErrFlowPermission)
	_, err = recovered.SubmitFlow(other, root, req)
	require.ErrorIs(t, err, ErrFlowPermission)
	after, err := a.CurrentFlow(ctx, root)
	require.NoError(t, err)
	require.Equal(t, before.Status, after.Status)
	require.Equal(t, before.Version, after.Version)
	require.Equal(t, before.Snapshot, after.Snapshot)
	require.False(t, recovered.FlowBusy(root))
	policies["coder"] = ptrFlowIDs("existing")
	require.NoError(t, recovered.ResumeFlow(other, root))
	waitWorkflowIdle(t, ctx, recovered, root)
	after, err = recovered.CurrentFlow(ctx, root)
	require.NoError(t, err)
	require.Equal(t, "completed", after.Status)
}
func TestStoredNamedPolicyDoesNotDisableGeneratedFlows(t *testing.T) {
	for _, op := range []string{"plan", "create"} {
		t.Run(op, func(t *testing.T) {
			a, _, root, home, ctx := newStoredWorkflow(t)
			a.Registry = namedPolicyRegistry{policies: map[string]*[]string{"coder": ptrFlowIDs()}}
			require.NoError(t, os.WriteFile(filepath.Join(home, "openspec"), []byte("#!/bin/sh\nexit 0\n"), 0700))
			t.Setenv("PATH", home)
			_, err := a.SubmitFlow(ctx, root, tools.FlowRequest{Operation: op, Goal: "test", Complexity: "S", Definition: "flow:\n  steps:\n    - id: work\n      prompt: work\n", InvocationID: op})
			require.NoError(t, err)
			waitWorkflowIdle(t, ctx, a, root)
			state, err := a.CurrentFlow(ctx, root)
			require.NoError(t, err)
			require.Equal(t, "completed", state.Status)
		})
	}
}

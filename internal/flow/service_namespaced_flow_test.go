package flow

import (
	"context"
	"strings"
	"testing"
)

// TestSessionSafeFlowID locks in the collision-free fold contract:
// shared (slash-free) IDs pass through unchanged; namespaced IDs fold
// "/" to "--", which no valid kebab-case flow ID can contain — so the
// folded form can never be byte-identical to a shared flow ID.
func TestSessionSafeFlowID(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"react-on-jira", "react-on-jira"},
		{"id/fix-failing-tests", "id--fix-failing-tests"},
		{"team-2/flow-v2", "team-2--flow-v2"},
	}
	for _, tt := range tests {
		got := sessionSafeFlowID(tt.in)
		if got != tt.want {
			t.Errorf("sessionSafeFlowID(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if strings.Contains(got, "/") {
			t.Errorf("sessionSafeFlowID(%q) = %q still contains a slash", tt.in, got)
		}
	}
	// The distinguishing property the fold exists for: a namespaced ID
	// must NOT fold onto the legacy shared ID with the same words.
	if sessionSafeFlowID("id/fix-failing-tests") == "id-fix-failing-tests" {
		t.Error("fold collides with legacy shared flow ID id-fix-failing-tests")
	}
}

// TestNamespacedFlowRunProducesSlashFreeSessionIDs runs a namespaced
// flow end-to-end against the stub stack and asserts every constructed
// session ID uses the folded (slash-free) flow ID — session IDs travel
// as single URL path segments (`GET /session/{sessionID}` and friends),
// so a raw "/" must never leak into them — while the persisted flow_id
// keeps the unfolded namespaced form.
func TestNamespacedFlowRunProducesSlashFreeSessionIDs(t *testing.T) {
	testFlow := Flow{
		ID:   "id/fix-failing-tests",
		Name: "Fix Failing Tests",
		Spec: FlowSpec{
			Steps: []Step{
				{ID: "step-one", Prompt: "do something"},
			},
		},
	}
	registerTestFlow(t, testFlow)

	wantRoot := "prefix-id--fix-failing-tests-step-one"

	q := &stubQuerier{}
	sessions := &stubSessions{}
	agent := newStubAgent()
	svc := NewService(sessions, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", "id/fix-failing-tests", map[string]any{}, false)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	for range agentEvents {
	}
	for range flowStates {
	}

	if got := agent.callCount(); got != 1 {
		t.Fatalf("agent calls = %d, want 1", got)
	}
	if len(q.createdFlowStates) == 0 {
		t.Fatal("expected CreateFlowState to be called")
	}
	created := q.createdFlowStates[0]
	if created.SessionID != wantRoot {
		t.Errorf("created SessionID = %q, want %q", created.SessionID, wantRoot)
	}
	if created.RootSessionID != wantRoot {
		t.Errorf("created RootSessionID = %q, want %q", created.RootSessionID, wantRoot)
	}
	if created.FlowID != "id/fix-failing-tests" {
		t.Errorf("created FlowID = %q, want id/fix-failing-tests (unfolded)", created.FlowID)
	}
	for _, fs := range q.snapshotFlowStates() {
		if strings.Contains(fs.SessionID, "/") || strings.Contains(fs.RootSessionID, "/") {
			t.Errorf("session IDs must be slash-free, got %q / %q", fs.SessionID, fs.RootSessionID)
		}
	}
}

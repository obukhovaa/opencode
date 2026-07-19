package permission

import (
	"context"
	"testing"
)

func TestAutoApproveToggle(t *testing.T) {
	svc := NewPermissionService()
	sessionID := "test-session-1"

	if svc.IsAutoApproveSession(sessionID) {
		t.Fatal("expected session to not be auto-approved initially")
	}

	svc.AutoApproveSession(sessionID)
	if !svc.IsAutoApproveSession(sessionID) {
		t.Fatal("expected session to be auto-approved after AutoApproveSession")
	}

	svc.RemoveAutoApproveSession(sessionID)
	if svc.IsAutoApproveSession(sessionID) {
		t.Fatal("expected session to not be auto-approved after RemoveAutoApproveSession")
	}
}

func TestAutoApproveIsolation(t *testing.T) {
	svc := NewPermissionService()

	svc.AutoApproveSession("session-a")

	if !svc.IsAutoApproveSession("session-a") {
		t.Fatal("expected session-a to be auto-approved")
	}
	if svc.IsAutoApproveSession("session-b") {
		t.Fatal("expected session-b to not be auto-approved")
	}
}

func TestAutoApproveRequestSkipsDialog(t *testing.T) {
	svc := NewPermissionService()
	sessionID := "auto-session"

	svc.AutoApproveSession(sessionID)

	ctx := context.Background()
	result := svc.Request(ctx, CreatePermissionRequest{
		SessionID: sessionID,
		ToolName:  "bash",
		Action:    "execute",
	})
	if !result {
		t.Fatal("expected auto-approved session to return true from Request")
	}
}

// TestHookAllowAndAutoApproveCompose locks in the precedence inside
// Request that lets RTK + serve-mode auto-approve coexist. RTK's hook
// can return `permissionDecision: "allow"` (sets HookAllowKey via the
// agent loop) OR omit it (current RTK 0.42.4 default: emit only
// updatedInput). In both cases, when the session is auto-approved
// (serve --auto-approve), Request MUST return true without publishing
// to the broker.
//
// Scenario matrix:
//
//	HookAllow  AutoApprove  Want
//	false      false        false (broker dispatch — exercised elsewhere)
//	false      true         true  (this test — RTK rewrite + serve auto-approve)
//	true       false        true  (this test — RTK explicit allow alone)
//	true       true         true  (this test — both signals; first wins)
func TestHookAllowAndAutoApproveCompose(t *testing.T) {
	cases := []struct {
		name        string
		hookAllow   bool
		autoApprove bool
	}{
		{"hook_allow_only", true, false},
		{"auto_approve_only", false, true},
		{"both", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewPermissionService()
			sessionID := "session-" + tc.name
			if tc.autoApprove {
				svc.AutoApproveSession(sessionID)
			}
			ctx := context.Background()
			if tc.hookAllow {
				ctx = context.WithValue(ctx, HookAllowKey, true)
			}
			got := svc.Request(ctx, CreatePermissionRequest{
				SessionID: sessionID,
				ToolName:  "bash",
				Action:    "execute",
			})
			if !got {
				t.Errorf("Request returned false; want true (hookAllow=%v autoApprove=%v)", tc.hookAllow, tc.autoApprove)
			}
		})
	}
}

func TestAutoApproveDenyStillEnforced(t *testing.T) {
	agentPerms := map[string]any{
		"bash": map[string]any{
			"*":    "allow",
			"rm *": "deny",
		},
	}

	action := EvaluateToolPermission("bash", "rm -rf /", agentPerms, nil)
	if action != ActionDeny {
		t.Fatalf("expected ActionDeny for denied command, got %v", action)
	}
}

func TestAutoApproveDisabledToolStillBlocked(t *testing.T) {
	toolsConfig := map[string]bool{
		"bash": false,
	}

	if IsToolEnabled("bash", toolsConfig) {
		t.Fatal("expected bash to be disabled when tools.bash = false")
	}
}

func TestAutoApproveSubagentInheritance(t *testing.T) {
	svc := NewPermissionService()
	parentSession := "parent-session"
	childSession := "child-session"

	// agent-tool.go links each subagent session to its caller; auto-approve
	// resolves through the link live rather than being copied at spawn.
	svc.LinkSession(childSession, parentSession)

	if svc.IsAutoApproveSession(childSession) {
		t.Fatal("expected child to not be auto-approved while parent is not")
	}

	// Toggling the parent ON after the subagent already exists must cover
	// the running subagent — this is the TUI mid-run toggle that used to
	// keep prompting (the inheritance copy had already been skipped).
	svc.AutoApproveSession(parentSession)
	if !svc.IsAutoApproveSession(childSession) {
		t.Fatal("expected running child session to pick up parent auto-approve")
	}

	// Toggling the parent OFF must revoke the child at the same moment.
	svc.RemoveAutoApproveSession(parentSession)
	if svc.IsAutoApproveSession(childSession) {
		t.Fatal("expected child session to lose auto-approve when parent toggled off")
	}

	// Auto-approve set directly on the child survives the parent's state.
	svc.AutoApproveSession(childSession)
	if !svc.IsAutoApproveSession(childSession) {
		t.Fatal("expected directly auto-approved child to stay auto-approved")
	}
}

func TestAutoApproveResolvesThroughSessionChain(t *testing.T) {
	svc := NewPermissionService()

	// root -> mid (e.g. hivemind task) -> leaf (nested workhorse task)
	svc.LinkSession("mid", "root")
	svc.LinkSession("leaf", "mid")
	svc.AutoApproveSession("root")

	if !svc.IsAutoApproveSession("leaf") {
		t.Fatal("expected grandchild session to inherit auto-approve from root")
	}

	ctx := context.Background()
	if !svc.Request(ctx, CreatePermissionRequest{
		SessionID: "leaf",
		ToolName:  "edit",
		Action:    "write",
	}) {
		t.Fatal("expected Request on grandchild session to auto-approve")
	}
}

func TestLinkSessionCycleSafety(t *testing.T) {
	svc := NewPermissionService()

	// Self-links are ignored; a manufactured two-node cycle must terminate.
	svc.LinkSession("a", "a")
	svc.LinkSession("a", "b")
	svc.LinkSession("b", "a")

	if svc.IsAutoApproveSession("a") {
		t.Fatal("expected cyclic chain without auto-approve to resolve false")
	}

	svc.AutoApproveSession("b")
	if !svc.IsAutoApproveSession("a") {
		t.Fatal("expected auto-approve to resolve through cyclic link")
	}
}

func TestPersistentGrantCoversLinkedSubagents(t *testing.T) {
	svc := NewPermissionService()
	svc.LinkSession("task-1", "main")

	// "Allow always" issued on the main session…
	svc.GrantPersistant(PermissionRequest{
		SessionID: "main",
		ToolName:  "edit",
		Action:    "write",
		Path:      "/repo/docs",
	})

	if !svc.Request(context.Background(), CreatePermissionRequest{
		SessionID: "task-1",
		ToolName:  "edit",
		Action:    "write",
		Path:      "/repo/docs/pitch.html",
	}) {
		t.Fatal("expected persistent grant on parent to cover linked subagent")
	}

	// …but a grant on the subagent must not leak upward to the parent.
	svc2 := NewPermissionService()
	svc2.LinkSession("task-1", "main")
	svc2.GrantPersistant(PermissionRequest{
		SessionID: "task-1",
		ToolName:  "edit",
		Action:    "write",
		Path:      "/repo/docs",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // unanswered dialog: Request must fall through and return false
	if svc2.Request(ctx, CreatePermissionRequest{
		SessionID: "main",
		ToolName:  "edit",
		Action:    "write",
		Path:      "/repo/docs/pitch.html",
	}) {
		t.Fatal("expected child grant to not cover the parent session")
	}
}

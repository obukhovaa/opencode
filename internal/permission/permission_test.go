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

	// Simulate the inheritance logic from agent-tool.go:
	// when creating a child task session, if the parent is auto-approved,
	// the child is also auto-approved.
	svc.AutoApproveSession(parentSession)

	if svc.IsAutoApproveSession(parentSession) {
		svc.AutoApproveSession(childSession)
	}

	if !svc.IsAutoApproveSession(childSession) {
		t.Fatal("expected child session to inherit auto-approve from parent")
	}

	// Verify removing parent does not affect child
	svc.RemoveAutoApproveSession(parentSession)
	if !svc.IsAutoApproveSession(childSession) {
		t.Fatal("expected child session to remain auto-approved after parent removal")
	}

	// Verify non-auto-approved parent does not propagate
	svc2 := NewPermissionService()
	if svc2.IsAutoApproveSession("parent2") {
		svc2.AutoApproveSession("child2")
	}
	if svc2.IsAutoApproveSession("child2") {
		t.Fatal("expected child2 to not be auto-approved when parent is not")
	}
}

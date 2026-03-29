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

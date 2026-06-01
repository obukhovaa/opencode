package api

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/permission"
)

// makePendingRequest fires permission.Request in a goroutine and waits for the
// request to be published before returning the request ID. The returned
// result channel resolves to the boolean Request() returned (true=grant,
// false=deny).
func makePendingRequest(t *testing.T, svc permission.Service) (string, <-chan bool) {
	t.Helper()
	sub := svc.Subscribe(context.Background())

	result := make(chan bool, 1)
	sessionID := "test-" + uuid.New().String()
	go func() {
		result <- svc.Request(context.Background(), permission.CreatePermissionRequest{
			SessionID: sessionID,
			ToolName:  "bash",
			Action:    "execute",
			Path:      "/tmp/dummy/file", // non-"." so config.WorkingDirectory() is not called
		})
	}()

	select {
	case evt := <-sub:
		return evt.Payload.ID, result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for permission.Request to publish")
		return "", nil
	}
}

func awaitResult(t *testing.T, result <-chan bool) bool {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for permission.Request to return")
		return false
	}
}

func TestApplyPermissionAction_OnceGrants(t *testing.T) {
	svc := permission.NewPermissionService()
	id, result := makePendingRequest(t, svc)

	if !applyPermissionAction(svc, id, "once") {
		t.Fatal("expected applyPermissionAction(once) to return true")
	}
	if got := awaitResult(t, result); !got {
		t.Fatal("expected Request to return true (granted)")
	}
}

func TestApplyPermissionAction_AlwaysGrants(t *testing.T) {
	svc := permission.NewPermissionService()
	id, result := makePendingRequest(t, svc)

	if !applyPermissionAction(svc, id, "always") {
		t.Fatal("expected applyPermissionAction(always) to return true")
	}
	if got := awaitResult(t, result); !got {
		t.Fatal("expected Request to return true (granted)")
	}
}

func TestApplyPermissionAction_RejectDenies(t *testing.T) {
	svc := permission.NewPermissionService()
	id, result := makePendingRequest(t, svc)

	if !applyPermissionAction(svc, id, "reject") {
		t.Fatal("expected applyPermissionAction(reject) to return true")
	}
	if got := awaitResult(t, result); got {
		t.Fatal("expected Request to return false (denied)")
	}
}

func TestApplyPermissionAction_InvalidVerb(t *testing.T) {
	svc := permission.NewPermissionService()
	id, result := makePendingRequest(t, svc)

	if applyPermissionAction(svc, id, "maybe") {
		t.Fatal("expected applyPermissionAction(unknown) to return false")
	}

	// Side-effect check: neither Grant nor Deny should have fired, so the
	// pending Request goroutine must still be blocked.
	select {
	case got := <-result:
		t.Fatalf("expected Request to remain pending; got result=%v", got)
	case <-time.After(100 * time.Millisecond):
		// pass — request is still pending
	}

	// Cleanup: unblock the goroutine so the test exits cleanly.
	svc.Deny(permission.PermissionRequest{ID: id})
	awaitResult(t, result)
}

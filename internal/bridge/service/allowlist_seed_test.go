package service

import (
	"context"
	"testing"
)

func TestSeedIdentityAllowlistInsertsForPrivateIdentity(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	ctx := context.Background()

	svc.seedIdentityAllowlist(ctx, "slack", "default", "private", []string{"U1", "C2"})

	for _, id := range []string{"U1", "C2"} {
		ok, err := svc.store.IsAllowlisted(ctx, svc.projectID, "slack", "default", id)
		if err != nil {
			t.Fatalf("IsAllowlisted %s: %v", id, err)
		}
		if !ok {
			t.Errorf("expected %s to be allowlisted after seed", id)
		}
	}
}

func TestSeedIdentityAllowlistNoOpForPublicIdentity(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	ctx := context.Background()

	// Public-mode identity: seed must NOT write rows even when peers is non-empty.
	svc.seedIdentityAllowlist(ctx, "mattermost", "default", "public", []string{"u1"})

	ok, err := svc.store.IsAllowlisted(ctx, svc.projectID, "mattermost", "default", "u1")
	if err != nil {
		t.Fatalf("IsAllowlisted: %v", err)
	}
	if ok {
		t.Errorf("public-mode identity must not write seed rows")
	}
}

func TestSeedIdentityAllowlistEmptyPeersNoOp(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	ctx := context.Background()

	// Private mode with empty list — no rows, no error.
	svc.seedIdentityAllowlist(ctx, "slack", "default", "private", nil)

	entries, err := svc.store.ListAllowlist(ctx, svc.projectID, "slack", "default")
	if err != nil {
		t.Fatalf("ListAllowlist: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no rows for empty seed, got %d", len(entries))
	}
}

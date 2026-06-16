package service

import (
	"context"
	"testing"
)

// TestSeedIdentityAllowlistInsertsForPrivateIdentity verifies the seed
// writes rows under the SAME projectID that downstream consumers
// (orchestrator's forwarder, runner-side adapter closure) key on:
// remoteProjectID. See seedIdentityAllowlist docstring for the
// mediated-inbound projectID mismatch this guards against.
func TestSeedIdentityAllowlistInsertsForPrivateIdentity(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	ctx := context.Background()

	svc.seedIdentityAllowlist(ctx, "slack", "default", "private", []string{"U1", "C2"})

	for _, id := range []string{"U1", "C2"} {
		ok, err := svc.store.IsAllowlisted(ctx, svc.remoteProjectID, "slack", "default", id)
		if err != nil {
			t.Fatalf("IsAllowlisted %s: %v", id, err)
		}
		if !ok {
			t.Errorf("expected %s to be allowlisted after seed under remoteProjectID=%q", id, svc.remoteProjectID)
		}
	}
}

// TestSeedIdentityAllowlistDoesNotWriteUnderLocalProjectID pins the
// fact that the local cwd-derived projectID is NOT what the seed uses.
// Pre-fix this test would have failed because seed used s.projectID.
func TestSeedIdentityAllowlistDoesNotWriteUnderLocalProjectID(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	if svc.projectID == svc.remoteProjectID {
		t.Skip("test requires divergent local + remote project IDs (mediated-inbound shape); skipping")
	}
	ctx := context.Background()

	svc.seedIdentityAllowlist(ctx, "slack", "default", "private", []string{"U1"})

	ok, err := svc.store.IsAllowlisted(ctx, svc.projectID, "slack", "default", "U1")
	if err != nil {
		t.Fatalf("IsAllowlisted: %v", err)
	}
	if ok {
		t.Errorf("seed must NOT write under local projectID=%q — orchestrator queries remoteProjectID", svc.projectID)
	}
}

func TestSeedIdentityAllowlistNoOpForPublicIdentity(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	ctx := context.Background()

	// Public-mode identity: seed must NOT write rows even when peers is non-empty.
	svc.seedIdentityAllowlist(ctx, "mattermost", "default", "public", []string{"u1"})

	ok, err := svc.store.IsAllowlisted(ctx, svc.remoteProjectID, "mattermost", "default", "u1")
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

	entries, err := svc.store.ListAllowlist(ctx, svc.remoteProjectID, "slack", "default")
	if err != nil {
		t.Fatalf("ListAllowlist: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no rows for empty seed, got %d", len(entries))
	}
}

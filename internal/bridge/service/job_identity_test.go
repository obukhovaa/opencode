package service

import (
	"context"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// The per-run bridge job identity (openspec change
// agent-pod-pool-runtime, design D9). A pool pod has no boot-time
// OPENCODE_BRIDGE_JOB_ID — one process serves many jobs — so the flow
// runner pushes the identity in per run via SetRemoteJobID. Two things
// must follow from that call: registerRemoteBindings stamps the new
// identity on binding rows, and every job-scoped adapter (the external
// relay) stamps it on outbound frames.

// jobScopedStubAdapter is a stub adapter that also implements
// bridge.JobScopedAdapter, so the propagation path is exercised.
type jobScopedStubAdapter struct {
	*stubAdapter
	mu  sync.Mutex
	ids []string
}

func (a *jobScopedStubAdapter) SetJobID(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ids = append(a.ids, id)
}

func (a *jobScopedStubAdapter) seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.ids...)
}

// TestSetRemoteJobIDPropagatesToAdapters: without this the external
// adapter keeps stamping its boot-time (empty, on a pool pod) job id and
// the orchestrator rejects every relay frame with 400 "jobId is
// required" — the reviewer never sees the question.
func TestSetRemoteJobIDPropagatesToAdapters(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ad := &jobScopedStubAdapter{stubAdapter: newStubAdapter("external", "c3")}
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	svc.SetRemoteJobID("job-1")
	if got := svc.RemoteJobID(); got != "job-1" {
		t.Errorf("RemoteJobID() = %q, want job-1", got)
	}
	svc.SetRemoteJobID("job-2")
	svc.SetRemoteJobID("")

	want := []string{"job-1", "job-2", ""}
	got := ad.seen()
	if len(got) != len(want) {
		t.Fatalf("adapter saw %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("adapter saw %q, want %q", got, want)
		}
	}
	if svc.RemoteJobID() != "" {
		t.Errorf("RemoteJobID() after clear = %q, want empty", svc.RemoteJobID())
	}
}

// TestRegisterRemoteBindingsUsesCurrentJobID: the identity in effect when
// the interactive step starts is the one the binding row carries, not
// whatever the process booted with.
func TestRegisterRemoteBindingsUsesCurrentJobID(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := svc.RegisterAdapter(context.Background(), newStubAdapter("slack", "default")); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}
	reg := &fakeRegistrar{}
	svc.remoteRegistrar = reg
	svc.remoteSelfHost = "pool-0.svc"
	svc.remoteSelfPort = 8080
	svc.remoteProjectID = "default"
	// Pool-pod boot state: no job identity at all.
	svc.SetRemoteJobID("")

	hook := svc.InteractiveHook()
	peers := []bridge.PeerRef{{Channel: "slack", Identity: "default", PeerID: "D-1"}}

	// Before the fix this was the ONLY behaviour a pool pod could have:
	// an empty job id makes registerRemoteBindings skip the remote
	// register entirely (warn-only, non-fatal), so the reviewer's reply
	// has no route back to the pod.
	if err := hook.OnInteractiveStepStart(context.Background(), "S1", peers); err != nil {
		t.Fatalf("OnInteractiveStepStart: %v", err)
	}
	if n := len(registeredRows(reg)); n != 0 {
		t.Fatalf("registered %d bindings with no job identity, want 0", n)
	}

	svc.SetRemoteJobID("job-1")
	if err := hook.OnInteractiveStepStart(context.Background(), "S1", peers); err != nil {
		t.Fatalf("OnInteractiveStepStart: %v", err)
	}
	rows := registeredRows(reg)
	if len(rows) != 1 {
		t.Fatalf("registered %d bindings, want 1", len(rows))
	}
	if rows[0].JobID != "job-1" {
		t.Errorf("binding JobID = %q, want job-1", rows[0].JobID)
	}
}

// registeredRows snapshots fakeRegistrar's accepted bindings.
func registeredRows(f *fakeRegistrar) []bridge.RemoteBinding {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bridge.RemoteBinding(nil), f.registers...)
}

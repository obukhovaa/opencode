package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// fakeRegistrar records every Register / Deregister call. Optional
// `registerErr` simulates a failing orchestrator.
type fakeRegistrar struct {
	mu                sync.Mutex
	registers         []bridge.RemoteBinding
	deregisters       []string // formatted as "channel|identity|peer"
	registerErr       error
	registerErrUntilN int           // when > 0, fail the first N register calls then succeed
	registerCalls     atomic.Int64
}

func (f *fakeRegistrar) Register(_ context.Context, b bridge.RemoteBinding) error {
	n := f.registerCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.registerErrUntilN > 0 && int(n) <= f.registerErrUntilN {
		return errors.New("simulated transient failure")
	}
	if f.registerErr != nil {
		return f.registerErr
	}
	f.registers = append(f.registers, b)
	return nil
}

func (f *fakeRegistrar) Deregister(_ context.Context, projectID, channel, identity, peerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deregisters = append(f.deregisters, channel+"|"+identity+"|"+peerID)
	return nil
}

func (f *fakeRegistrar) registerCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.registers)
}

func (f *fakeRegistrar) deregisterCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.deregisters)
}

// withFastRetry tightens the per-session retry loop so tests don't
// sleep for 5 real minutes. Restores the prior value via t.Cleanup.
func withFastRetry(t *testing.T, d time.Duration) {
	t.Helper()
	prev := remoteRegisterRetryInterval
	remoteRegisterRetryInterval = d
	t.Cleanup(func() {
		remoteRegisterRetryInterval = prev
	})
}

// --- tests -----------------------------------------------------------------

// TestInteractiveHook_RegistersOnStart verifies that the bridge
// service mirrors a successful local bind into the remote registrar
// with the correct wire shape.
func TestInteractiveHook_RegistersOnStart(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ad := newStubAdapter("slack", "default")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	reg := &fakeRegistrar{}
	svc.remoteRegistrar = reg
	svc.remoteSelfHost = "c2-agent-job-T.svc"
	svc.remoteSelfPort = 8080
	svc.remoteJobID = "job-T"
	svc.remoteProjectID = "default"

	hook := svc.InteractiveHook()
	err := hook.OnInteractiveStepStart(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D-1", Mention: "<@U1>"},
	})
	if err != nil {
		t.Fatalf("OnInteractiveStepStart: %v", err)
	}
	if got := reg.registerCount(); got != 1 {
		t.Fatalf("registrar saw %d calls, want 1", got)
	}
	got := reg.registers[0]
	if got.JobID != "job-T" || got.ContainerHost != "c2-agent-job-T.svc" || got.ContainerPort != 8080 {
		t.Errorf("registrar received %+v", got)
	}
	if got.PeerID != "D-1" || got.MentionHandle != "<@U1>" {
		t.Errorf("registrar peer/mention = %+v", got)
	}
}

// TestInteractiveHook_RegisterFailureDoesNotBlockLocalBind verifies
// the openspec contract: local bind happens regardless of remote
// outcome. The agent's flow step proceeds on the strength of the
// local row alone.
func TestInteractiveHook_RegisterFailureDoesNotBlockLocalBind(t *testing.T) {
	withFastRetry(t, 10*time.Millisecond)

	svc, _ := newOrchestratorForTest(t)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ad := newStubAdapter("slack", "default")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	reg := &fakeRegistrar{registerErr: errors.New("orchestrator down")}
	svc.remoteRegistrar = reg
	svc.remoteSelfHost = "h"
	svc.remoteSelfPort = 1
	svc.remoteJobID = "j"
	svc.remoteProjectID = "default"

	hook := svc.InteractiveHook()
	err := hook.OnInteractiveStepStart(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D-1"},
	})
	if err != nil {
		t.Fatalf("OnInteractiveStepStart returned err %v (local bind must succeed regardless of remote)", err)
	}
	// Local binding row exists.
	bindings, err := svc.store.ListBindingsBySession(context.Background(), svc.projectID, "S1")
	if err != nil {
		t.Fatalf("ListBindingsBySession: %v", err)
	}
	if len(bindings) != 1 {
		t.Errorf("local bindings = %d, want 1", len(bindings))
	}

	// Cancel any in-flight retry so the test doesn't leak.
	hook.cancelRemoteRetry("S1")
}

// TestInteractiveHook_RetryEventuallySucceeds verifies the retry
// loop: first call fails, scheduled retry fires after the (test-
// shortened) interval and succeeds.
func TestInteractiveHook_RetryEventuallySucceeds(t *testing.T) {
	withFastRetry(t, 50*time.Millisecond)

	svc, _ := newOrchestratorForTest(t)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ad := newStubAdapter("slack", "default")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	reg := &fakeRegistrar{registerErrUntilN: 1} // fail once then succeed
	svc.remoteRegistrar = reg
	svc.remoteSelfHost = "h"
	svc.remoteSelfPort = 1
	svc.remoteJobID = "j"
	svc.remoteProjectID = "default"

	hook := svc.InteractiveHook()
	if err := hook.OnInteractiveStepStart(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D-1"},
	}); err != nil {
		t.Fatalf("OnInteractiveStepStart: %v", err)
	}

	// Wait up to 500ms for the retry to fire + succeed.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if reg.registerCount() >= 1 && reg.registerCalls.Load() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reg.registerCount() != 1 {
		t.Errorf("eventual register count = %d, want 1 (retry should have succeeded)", reg.registerCount())
	}
	if reg.registerCalls.Load() < 2 {
		t.Errorf("registerCalls = %d, want >= 2 (first attempt + retry)", reg.registerCalls.Load())
	}

	hook.cancelRemoteRetry("S1")
}

// TestInteractiveHook_DeregisterOnComplete verifies the unbind
// path: OnInteractiveStepComplete calls Deregister for every peer
// the runner registered.
func TestInteractiveHook_DeregisterOnComplete(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ad := newStubAdapter("slack", "default")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	reg := &fakeRegistrar{}
	svc.remoteRegistrar = reg
	svc.remoteSelfHost = "h"
	svc.remoteSelfPort = 1
	svc.remoteJobID = "j"
	svc.remoteProjectID = "default"

	hook := svc.InteractiveHook()
	if err := hook.OnInteractiveStepStart(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D-1"},
		{Channel: "slack", Identity: "default", PeerID: "D-2"},
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := reg.registerCount(); got != 2 {
		t.Fatalf("registrar saw %d calls, want 2", got)
	}

	if err := hook.OnInteractiveStepComplete(context.Background(), "S1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := reg.deregisterCount(); got != 2 {
		t.Errorf("deregister calls = %d, want 2", got)
	}
}

// TestInteractiveHook_NoRegistrarSkipsRemoteCalls verifies the
// single-container-mode path: no registrar wired → only local bind
// happens.
func TestInteractiveHook_NoRegistrarSkipsRemoteCalls(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ad := newStubAdapter("slack", "default")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	// No remoteRegistrar set on svc.
	hook := svc.InteractiveHook()
	if err := hook.OnInteractiveStepStart(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D-1"},
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	bindings, _ := svc.store.ListBindingsBySession(context.Background(), svc.projectID, "S1")
	if len(bindings) != 1 {
		t.Errorf("local bindings = %d, want 1", len(bindings))
	}
}

// TestInteractiveHook_IncompleteSelfIdentitySkipsRegister verifies
// the defensive guard: registrar wired but missing host/port/jobID →
// log + skip the remote call rather than POST a malformed row.
func TestInteractiveHook_IncompleteSelfIdentitySkipsRegister(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ad := newStubAdapter("slack", "default")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	reg := &fakeRegistrar{}
	svc.remoteRegistrar = reg
	// Deliberately leave remoteSelfHost / remoteSelfPort / remoteJobID unset.
	svc.remoteProjectID = "default"

	hook := svc.InteractiveHook()
	if err := hook.OnInteractiveStepStart(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D-1"},
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := reg.registerCount(); got != 0 {
		t.Errorf("registrar saw %d calls, want 0 (incomplete self-identity)", got)
	}
}

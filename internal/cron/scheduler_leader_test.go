package cron

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// fakeLeaderLock is a controllable LeaderLock for scheduler tests.
type fakeLeaderLock struct {
	held       atomic.Bool
	acquireErr error
	pingErr    error
	acquires   atomic.Int32
	pings      atomic.Int32
	releases   atomic.Int32
}

func (f *fakeLeaderLock) TryAcquire(_ context.Context) error {
	f.acquires.Add(1)
	if f.acquireErr != nil {
		return f.acquireErr
	}
	f.held.Store(true)
	return nil
}

func (f *fakeLeaderLock) Held() bool { return f.held.Load() }

func (f *fakeLeaderLock) Ping(_ context.Context) error {
	f.pings.Add(1)
	if f.pingErr != nil {
		// Match the production contract: on Ping failure the lock is no
		// longer held by this process. The scheduler reads Held() to
		// decide whether to keep ticking, so the fake must mirror it.
		f.held.Store(false)
		return f.pingErr
	}
	return nil
}

func (f *fakeLeaderLock) Release() error {
	f.releases.Add(1)
	f.held.Store(false)
	return nil
}

func TestSchedulerIsLeader_NoLockMeansAlwaysLeader(t *testing.T) {
	s := &Scheduler{}
	if !s.isLeader() {
		t.Fatalf("isLeader() with no lock = false, want true (backward-compat)")
	}
}

func TestSchedulerIsLeader_DefersToLock(t *testing.T) {
	lock := &fakeLeaderLock{}
	s := &Scheduler{}
	s.SetLeaderLock(lock)

	if s.isLeader() {
		t.Fatalf("isLeader() before acquire = true, want false")
	}
	lock.held.Store(true)
	if !s.isLeader() {
		t.Fatalf("isLeader() after acquire = false, want true")
	}
}

func TestSchedulerTryAcquireLeadership_TransitionReportedOnce(t *testing.T) {
	lock := &fakeLeaderLock{}
	s := &Scheduler{}
	s.SetLeaderLock(lock)

	ctx := context.Background()
	acquired, err := s.tryAcquireLeadership(ctx)
	if !acquired || err != nil {
		t.Fatalf("first tryAcquireLeadership = (%v, %v), want (true, nil)", acquired, err)
	}
	acquired, err = s.tryAcquireLeadership(ctx)
	if acquired || err != nil {
		t.Fatalf("second tryAcquireLeadership while held = (%v, %v), want (false, nil)", acquired, err)
	}
	if n := lock.acquires.Load(); n != 1 {
		t.Errorf("Lock.TryAcquire called %d times, want 1 (no retry while held)", n)
	}
}

func TestSchedulerTryAcquireLeadership_HeldByPeerStaysFollower(t *testing.T) {
	lock := &fakeLeaderLock{acquireErr: ErrLeaderHeld}
	s := &Scheduler{}
	s.SetLeaderLock(lock)

	acquired, err := s.tryAcquireLeadership(context.Background())
	if acquired {
		t.Fatalf("tryAcquireLeadership with ErrLeaderHeld = true, want false")
	}
	if !errors.Is(err, ErrLeaderHeld) {
		t.Fatalf("reason = %v, want ErrLeaderHeld (so bootAcquire can log the right follower message)", err)
	}
	if s.isLeader() {
		t.Fatalf("isLeader() after ErrLeaderHeld = true, want false")
	}
}

func TestSchedulerTryAcquireLeadership_NoLockIsNoOp(t *testing.T) {
	s := &Scheduler{}
	acquired, err := s.tryAcquireLeadership(context.Background())
	if acquired || err != nil {
		t.Fatalf("tryAcquireLeadership with no lock = (%v, %v), want (false, nil)", acquired, err)
	}
}

func TestSchedulerTryAcquireLeadership_TransportErrorBubblesAsFollower(t *testing.T) {
	// A non-ErrLeaderHeld error (transport failure) should leave the
	// scheduler as a follower — it does not poison-acquire and pretend
	// to be a leader. The reason is returned so bootAcquire can log a
	// "lock unavailable" follower message distinct from the steady-state
	// peer-leader case.
	boom := errors.New("boom")
	lock := &fakeLeaderLock{acquireErr: boom}
	s := &Scheduler{}
	s.SetLeaderLock(lock)

	acquired, err := s.tryAcquireLeadership(context.Background())
	if acquired {
		t.Fatalf("tryAcquireLeadership with transport error = true, want false")
	}
	if !errors.Is(err, boom) {
		t.Errorf("reason = %v, want %v wrapped", err, boom)
	}
	if s.isLeader() {
		t.Fatalf("isLeader() after transport error = true, want false")
	}
}

func TestSchedulerPingLeadership_NoLockIsNoOp(t *testing.T) {
	// Pinging when no lock is configured must not panic and must not
	// reach into Held().
	s := &Scheduler{}
	s.pingLeadership(context.Background()) // no panic = pass
}

func TestSchedulerPingLeadership_FollowerSkipsPing(t *testing.T) {
	// A follower (lock configured but not held) should not waste a
	// round-trip pinging — the next retry tick will TryAcquire instead.
	lock := &fakeLeaderLock{}
	s := &Scheduler{}
	s.SetLeaderLock(lock)

	s.pingLeadership(context.Background())
	if n := lock.pings.Load(); n != 0 {
		t.Errorf("Ping called %d times while follower, want 0", n)
	}
}

func TestSchedulerPingLeadership_HeldHappyPath(t *testing.T) {
	lock := &fakeLeaderLock{}
	lock.held.Store(true)
	s := &Scheduler{}
	s.SetLeaderLock(lock)

	s.pingLeadership(context.Background())
	if n := lock.pings.Load(); n != 1 {
		t.Errorf("Ping called %d times, want 1", n)
	}
	if !s.isLeader() {
		t.Errorf("isLeader() after successful Ping = false, want true")
	}
}

func TestSchedulerPingLeadership_LostLockDowngrades(t *testing.T) {
	// MySQL conn dies → Ping returns ErrLeaderHeld → fake flips held=false
	// to mirror the production contract. The scheduler must now report
	// follower AND the next retry tick must be able to re-acquire — the
	// downgrade should not wedge state that makes TryAcquire a no-op.
	lock := &fakeLeaderLock{pingErr: ErrLeaderHeld}
	lock.held.Store(true)
	s := &Scheduler{}
	s.SetLeaderLock(lock)

	s.pingLeadership(context.Background())
	if s.isLeader() {
		t.Fatalf("isLeader() after lost-lock Ping = true, want false")
	}

	// Clear the simulated ping failure so the next TryAcquire succeeds
	// (the peer that took the lock has released it, or our conn is
	// healthy again on a fresh open).
	lock.pingErr = nil
	acquired, err := s.tryAcquireLeadership(context.Background())
	if !acquired || err != nil {
		t.Fatalf("re-acquire after downgrade = (%v, %v), want (true, nil) — downgrade must not wedge state",
			acquired, err)
	}
	if !s.isLeader() {
		t.Fatalf("isLeader() after re-acquire = false, want true")
	}
}

func TestSchedulerPingLeadership_TransportErrorDowngrades(t *testing.T) {
	// A non-ErrLeaderHeld Ping failure (Warn-logged path) must downgrade
	// us to follower the same way ErrLeaderHeld does. Otherwise a process
	// could keep ticking on a lock it can no longer verify.
	lock := &fakeLeaderLock{pingErr: errors.New("transport boom")}
	lock.held.Store(true)
	s := &Scheduler{}
	s.SetLeaderLock(lock)

	s.pingLeadership(context.Background())
	if s.isLeader() {
		t.Fatalf("isLeader() after transport-error Ping = true, want false")
	}
}

func TestSchedulerSetLeaderLock_ReplaceReleasesOldLock(t *testing.T) {
	// Setting a new lock while one is already configured should release
	// the displaced lock — otherwise the file descriptor / MySQL conn
	// leaks for the rest of the process's life.
	oldLock := &fakeLeaderLock{}
	oldLock.held.Store(true)
	newLock := &fakeLeaderLock{}

	s := &Scheduler{}
	s.SetLeaderLock(oldLock)
	s.SetLeaderLock(newLock)

	if n := oldLock.releases.Load(); n != 1 {
		t.Errorf("old lock Release called %d times, want 1", n)
	}
	if n := newLock.releases.Load(); n != 0 {
		t.Errorf("new lock Release called %d times, want 0 (not replaced)", n)
	}
}

func TestSchedulerSetLeaderLock_SameRefIsNoOp(t *testing.T) {
	// Re-setting the same lock reference must not release it; that would
	// be a self-foot-shot for callers that re-wire after construction.
	lock := &fakeLeaderLock{}
	lock.held.Store(true)
	s := &Scheduler{}
	s.SetLeaderLock(lock)
	s.SetLeaderLock(lock)

	if n := lock.releases.Load(); n != 0 {
		t.Errorf("Release called %d times on same-ref SetLeaderLock, want 0", n)
	}
}

// withTransitionCounter installs a counter hook on s and returns it. Use
// to verify whether onLeaderTransition fired in a test without needing
// a real *service.
func withTransitionCounter(s *Scheduler) *atomic.Int32 {
	var n atomic.Int32
	s.transitionHook = func(context.Context) { n.Add(1) }
	return &n
}

func TestSchedulerBootAcquire_LeaderRunsTransition(t *testing.T) {
	lock := &fakeLeaderLock{}
	s := &Scheduler{}
	transitions := withTransitionCounter(s)
	s.SetLeaderLock(lock)

	s.bootAcquire(context.Background())
	if !s.isLeader() {
		t.Fatalf("isLeader() after bootAcquire = false, want true")
	}
	if n := transitions.Load(); n != 1 {
		t.Errorf("onLeaderTransition fired %d times, want 1", n)
	}
}

func TestSchedulerBootAcquire_PeerHeldNoTransition(t *testing.T) {
	lock := &fakeLeaderLock{acquireErr: ErrLeaderHeld}
	s := &Scheduler{}
	transitions := withTransitionCounter(s)
	s.SetLeaderLock(lock)

	s.bootAcquire(context.Background())
	if s.isLeader() {
		t.Fatalf("isLeader() with peer-held lock = true, want false")
	}
	if n := transitions.Load(); n != 0 {
		t.Errorf("onLeaderTransition fired %d times, want 0", n)
	}
}

func TestSchedulerBootAcquire_TransportErrorNoTransition(t *testing.T) {
	lock := &fakeLeaderLock{acquireErr: errors.New("transport boom")}
	s := &Scheduler{}
	transitions := withTransitionCounter(s)
	s.SetLeaderLock(lock)

	s.bootAcquire(context.Background())
	if s.isLeader() {
		t.Fatalf("isLeader() on transport error = true, want false")
	}
	if n := transitions.Load(); n != 0 {
		t.Errorf("onLeaderTransition fired %d times on transport error, want 0", n)
	}
}

func TestSchedulerBootAcquire_NilLockIsNoOp(t *testing.T) {
	s := &Scheduler{}
	transitions := withTransitionCounter(s)
	s.bootAcquire(context.Background())
	if n := transitions.Load(); n != 0 {
		t.Errorf("onLeaderTransition fired %d times without a lock, want 0", n)
	}
}

func TestSchedulerBootAcquire_PreAcquiredLockTransitions(t *testing.T) {
	// Latent path: lock already Held() before bootAcquire runs. The
	// scheduler must still call onLeaderTransition so ClearStaleFiring
	// gets a chance at any rows the predecessor left behind.
	lock := &fakeLeaderLock{}
	lock.held.Store(true)
	s := &Scheduler{}
	transitions := withTransitionCounter(s)
	s.SetLeaderLock(lock)

	s.bootAcquire(context.Background())
	if !s.isLeader() {
		t.Fatalf("isLeader() with pre-acquired lock = false, want true")
	}
	if n := transitions.Load(); n != 1 {
		t.Errorf("onLeaderTransition fired %d times on pre-acquired lock, want 1", n)
	}
	if n := lock.acquires.Load(); n != 0 {
		t.Errorf("TryAcquire called %d times on pre-acquired lock, want 0", n)
	}
}

func TestSchedulerStop_DoubleStopReleasesOnce(t *testing.T) {
	// ForceShutdown + signal-handler Shutdown can call Stop twice; the
	// lock must only be released once.
	lock := &fakeLeaderLock{}
	lock.held.Store(true)
	s := &Scheduler{}
	s.SetLeaderLock(lock)

	s.Stop()
	s.Stop()
	if n := lock.releases.Load(); n != 1 {
		t.Errorf("Release called %d times across two Stops, want 1", n)
	}
}

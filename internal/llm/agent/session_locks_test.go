package agent

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestSessionSlots_CrossInstanceExclusion pins the process-global
// one-Run-per-session invariant: a slot held through one agent instance is
// visible — and unacquirable — through every other instance (GENAI-239).
func TestSessionSlots_CrossInstanceExclusion(t *testing.T) {
	a1, a2 := &agent{}, &agent{}
	const sid = "slot-test-cross-instance"

	if !a1.TryLockSession(sid) {
		t.Fatal("first acquire failed on a free slot")
	}
	t.Cleanup(func() { releaseSessionSlot(sid) })

	if a2.TryLockSession(sid) {
		t.Fatal("second instance acquired a slot already held by the first")
	}
	if !a2.IsSessionBusy(sid) {
		t.Fatal("busy state not visible from another instance")
	}
	if !SessionBusy(sid) {
		t.Fatal("package-level SessionBusy() = false while the slot is held")
	}
	// Cancel must skip the cron sentinel — it is not a CancelFunc.
	a2.Cancel(sid)
	if !SessionBusy(sid) {
		t.Fatal("Cancel released a cron-held slot")
	}

	// Unlock through a DIFFERENT instance releases the cron sentinel (the
	// ledger is global; instances are irrelevant to ownership of sentinels).
	a2.UnlockSession(sid)
	if SessionBusy(sid) {
		t.Fatal("UnlockSession did not release the cron sentinel")
	}
	if !a2.TryLockSession(sid) {
		t.Fatal("slot not acquirable after release")
	}
	a2.UnlockSession(sid)
}

// TestUnlockSession_DoesNotReleaseRunHolder pins that the cron unlock path
// can never strip a live Run's slot: only cronLock-typed holders release.
func TestUnlockSession_DoesNotReleaseRunHolder(t *testing.T) {
	const sid = "slot-test-run-holder"
	if !acquireSessionSlot(sid, context.CancelFunc(func() {})) {
		t.Fatal("acquire failed on a free slot")
	}
	t.Cleanup(func() { releaseSessionSlot(sid) })

	(&agent{}).UnlockSession(sid)
	if !SessionBusy(sid) {
		t.Fatal("UnlockSession released a live Run's slot")
	}
}

// TestCancel_CrossInstanceHolder pins the abort path for sessions run by a
// different agent instance: Cancel falls back to the global slot's holder
// and fires its CancelFunc WITHOUT releasing the slot — the owning
// goroutine's deferred cleanup does that.
func TestCancel_CrossInstanceHolder(t *testing.T) {
	const sid = "slot-test-cancel-cross"
	var cancelled atomic.Bool
	if !acquireSessionSlot(sid, context.CancelFunc(func() { cancelled.Store(true) })) {
		t.Fatal("acquire failed on a free slot")
	}
	t.Cleanup(func() { releaseSessionSlot(sid) })

	// This instance's own activeRequests map has no entry for sid, so the
	// global fallback must fire the holder.
	(&agent{}).Cancel(sid)
	if !cancelled.Load() {
		t.Fatal("cross-instance Cancel did not fire the slot holder's CancelFunc")
	}
	if !SessionBusy(sid) {
		t.Fatal("Cancel must not release the slot; the owner's defer does")
	}
}

// TestAcquireSessionSlot_Atomicity pins that acquire is a single atomic
// LoadOrStore — exactly one of N concurrent claimants wins.
func TestAcquireSessionSlot_Atomicity(t *testing.T) {
	const sid = "slot-test-atomic"
	t.Cleanup(func() { releaseSessionSlot(sid) })

	const n = 32
	var wins atomic.Int32
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			if acquireSessionSlot(sid, context.CancelFunc(func() {})) {
				wins.Add(1)
			}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	if got := wins.Load(); got != 1 {
		t.Fatalf("concurrent acquire wins = %d, want exactly 1", got)
	}
}

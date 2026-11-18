package task

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func assertSessionWake(t *testing.T, ch <-chan struct{}, want bool) {
	t.Helper()
	select {
	case <-ch:
		if !want {
			t.Fatal("unexpected session wake")
		}
	default:
		if want {
			t.Fatal("missing session wake")
		}
	}
}

func TestRegistry_SessionBroadcast(t *testing.T) {
	// Both methods must work with lazily initialized registry state.
	r := &registry{}
	r.NotifySession("unwatched")
	assertSessionWake(t, r.WatchSession("unwatched"), false)
	first := r.WatchSession("s1")
	second := r.WatchSession("s1")
	other := r.WatchSession("s2")
	if first != second {
		t.Fatal("listeners must share the current broadcast channel")
	}
	assertSessionWake(t, first, false)
	r.NotifySession("s1")
	assertSessionWake(t, first, true)
	assertSessionWake(t, second, true)
	assertSessionWake(t, other, false)
	next := r.WatchSession("s1")
	if next == first {
		t.Fatal("notification did not replace the channel")
	}
	assertSessionWake(t, next, false)
	r.NotifySession("s1")
	assertSessionWake(t, next, true)
}

func TestRegistry_SessionBroadcastConcurrent(t *testing.T) {
	r := &registry{}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				ch := r.WatchSession("s1")
				r.NotifySession("s1")
				// Our own notification or an intervening one closed ch.
				<-ch
			}
		}()
	}
	wg.Wait()
	assertSessionWake(t, r.WatchSession("s1"), false)
}

func TestEnqueue_SessionWake(t *testing.T) {
	for _, status := range []Status{StatusMonitorEvent, StatusCompleted, StatusFailed, StatusKilled} {
		t.Run(string(status), func(t *testing.T) {
			deps, reg, tk, cleanup := setupTaskFixture(t, true)
			defer cleanup()
			tk.Kind = KindMonitor
			tk.ParentSessionID = "parent"
			tk.FlowOwned = true
			// Inspect at the notification boundary, so a premature broadcast
			// cannot pass merely because Enqueue finishes before assertions run.
			ResetGlobalRegistry()
			SetGlobalRegistry(wakeCheckingRegistry{Registry: reg, beforeNotify: func(sessionID string) {
				if sessionID != tk.SessionID || len(deps.snapshot()) != 1 {
					t.Fatal("notification must follow the durable write to the exact session")
				}
				if status.IsTerminal() && tk.State() != stateFromStatus(status) {
					t.Fatal("notification preceded MarkFinished")
				}
			}})
			wake := reg.WatchSession(tk.SessionID)
			parent := reg.WatchSession("parent")
			sibling := reg.WatchSession("sibling")
			// Subscribe before reading durable history/pending state. An event
			// arriving after these reads must still wake the saved channel.
			if len(deps.snapshot()) != 0 || len(reg.PendingForSession(tk.SessionID, nil)) != 1 {
				t.Fatal("unexpected initial history or pending state")
			}
			in := CompletionInput{
				SessionID: tk.SessionID, TaskID: tk.ID, Kind: KindMonitor,
				OriginatingToolName: "monitor", Status: status,
				Content: "event", SuppressIfNotified: true,
			}
			if err := EnqueueTaskCompletion(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			assertSessionWake(t, wake, true)
			assertSessionWake(t, parent, false)
			assertSessionWake(t, sibling, false)
			if len(deps.snapshot()) != 1 {
				t.Fatal("wake without a durable pair")
			}
			if status.IsTerminal() {
				if tk.State() != stateFromStatus(status) || len(reg.PendingForSession(tk.SessionID, nil)) != 0 {
					t.Fatal("terminal wake without terminal state")
				}
				assertSessionWake(t, tk.Done(), true)
			} else {
				if tk.State() != StateRunning || len(reg.PendingForSession(tk.SessionID, nil)) != 1 {
					t.Fatal("monitor event ended the task")
				}
				assertSessionWake(t, tk.Done(), false)
			}
			next := reg.WatchSession(tk.SessionID)
			assertSessionWake(t, next, false)
			ResetGlobalRegistry()
			SetGlobalRegistry(reg)
			if err := EnqueueTaskCompletion(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			// Duplicate terminal writes are suppressed; monitor events repeat.
			assertSessionWake(t, next, !status.IsTerminal())
		})
	}
}

type wakeCheckingRegistry struct {
	Registry
	beforeNotify func(string)
}

func (r wakeCheckingRegistry) NotifySession(sessionID string) {
	r.beforeNotify(sessionID)
	r.Registry.NotifySession(sessionID)
}

type failingWakeDeps struct {
	*fakeDeps
	err error
}

func (d failingWakeDeps) WritePair(context.Context, string, SyntheticPair) error {
	return d.err
}

func TestEnqueue_FailedWriteDoesNotWake(t *testing.T) {
	for _, status := range []Status{StatusMonitorEvent, StatusCompleted} {
		t.Run(string(status), func(t *testing.T) {
			deps, reg, tk, cleanup := setupTaskFixture(t, false)
			defer cleanup()
			denied := errors.New("write failed")
			restore := SetDeps(failingWakeDeps{fakeDeps: deps, err: denied})
			defer restore()
			wake := reg.WatchSession(tk.SessionID)
			err := EnqueueTaskCompletion(context.Background(), CompletionInput{
				SessionID: tk.SessionID, TaskID: tk.ID, OriginatingToolName: "monitor",
				Status: status, SuppressIfNotified: true,
			})
			if !errors.Is(err, denied) {
				t.Fatalf("enqueue error = %v, want %v", err, denied)
			}
			assertSessionWake(t, wake, false)
			if reg.WatchSession(tk.SessionID) != wake {
				t.Fatal("failed write replaced the broadcast channel")
			}
			if tk.State() != StateRunning || len(deps.snapshot()) != 0 || deps.resumeCalls.Load() != 0 {
				t.Fatal("failed write transitioned, persisted, or resumed")
			}
		})
	}
}

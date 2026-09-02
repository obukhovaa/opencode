package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/task"
)

func newDrainRegistry(t *testing.T) task.Registry {
	t.Helper()
	dir := t.TempDir()
	return task.NewRegistry(func() string { return dir })
}

func registerDrainTask(t *testing.T, reg task.Registry, sessionID string) string {
	t.Helper()
	id := task.NewTaskID(task.KindTask)
	if err := reg.Register(&task.Task{ID: id, SessionID: sessionID, Kind: task.KindTask}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return id
}

// TestDrainSessionTasks_TwoWaves pins the drain-to-empty contract: a second
// wave registered AFTER the first wait's snapshot is still waited on — the
// drain returns only when the session has zero pending tasks.
func TestDrainSessionTasks_TwoWaves(t *testing.T) {
	reg := newDrainRegistry(t)
	const sess = "S"

	wave1 := registerDrainTask(t, reg, sess)

	var wave2 string
	wave2Registered := make(chan struct{})
	go func() {
		// Let the drain's first wait snapshot only wave1, then register
		// wave2 BEFORE finishing wave1 so the wait's clean return races
		// against a non-empty pending set.
		time.Sleep(100 * time.Millisecond)
		wave2 = registerDrainTask(t, reg, sess)
		close(wave2Registered)
		reg.MarkFinished(wave1, task.StateCompleted, nil)
		// Finish wave2 a bit later — the drain must still be blocking.
		time.Sleep(150 * time.Millisecond)
		reg.MarkFinished(wave2, task.StateCompleted, nil)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	if err := drainSessionTasks(ctx, reg, sess); err != nil {
		t.Fatalf("drain returned error: %v", err)
	}
	elapsed := time.Since(start)

	<-wave2Registered
	if pending := reg.PendingForSession(sess, nil); len(pending) != 0 {
		t.Fatalf("drain returned with %d task(s) still pending", len(pending))
	}
	// Must have waited for wave2 (registered at ~100ms, finished at ~250ms),
	// not returned after wave1 alone (~100ms).
	if elapsed < 200*time.Millisecond {
		t.Errorf("drain returned after %v — did not wait for the second wave", elapsed)
	}
}

// TestDrainSessionTasks_EmptySession returns immediately when nothing is
// pending.
func TestDrainSessionTasks_EmptySession(t *testing.T) {
	reg := newDrainRegistry(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := drainSessionTasks(ctx, reg, "EMPTY"); err != nil {
		t.Fatalf("drain on empty session: %v", err)
	}
}

// TestDrainSessionTasks_CtxCancelPropagates surfaces ctx.Err() when the
// deadline elapses with tasks still running (the injectWaitTimeoutNote
// path in processGeneration keys off this).
func TestDrainSessionTasks_CtxCancelPropagates(t *testing.T) {
	reg := newDrainRegistry(t)
	const sess = "HANG"
	id := registerDrainTask(t, reg, sess)
	defer reg.MarkFinished(id, task.StateKilled, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := drainSessionTasks(ctx, reg, sess)
	if err == nil {
		t.Fatal("expected ctx error, got nil")
	}
	if ctx.Err() == nil {
		t.Fatal("test invariant: ctx should be done")
	}
}

// TestDrainSessionTasks_IncludesMonitors pins that the end-of-turn drain
// (unlike the bash anti-spin redirect) DOES wait on monitor tasks.
func TestDrainSessionTasks_IncludesMonitors(t *testing.T) {
	reg := newDrainRegistry(t)
	const sess = "MON"
	id := task.NewTaskID(task.KindMonitor)
	if err := reg.Register(&task.Task{ID: id, SessionID: sess, Kind: task.KindMonitor}); err != nil {
		t.Fatalf("register: %v", err)
	}
	go func() {
		time.Sleep(120 * time.Millisecond)
		reg.MarkFinished(id, task.StateCompleted, nil)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := drainSessionTasks(ctx, reg, sess); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("drain returned after %v — monitor was not waited on", elapsed)
	}
}

// syncBuffer is a mutex-guarded io.Writer: the drain logs from its waiter
// goroutine while the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// captureLogs redirects the default slog logger into a buffer for one test.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	sb := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(sb, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return sb
}

func withDrainProgressInterval(t *testing.T, d time.Duration) {
	t.Helper()
	prev := drainProgressInterval
	drainProgressInterval = d
	t.Cleanup(func() { drainProgressInterval = prev })
}

// TestDrainProgressLog covers the periodic still-waiting report. It exists
// because the drain otherwise logs once on entry and then nothing, so a step
// legitimately held open by a long task looked identical in the log to a hung
// process — a production wedge once produced 1h50m of unbroken silence.
func TestDrainProgressLog(t *testing.T) {
	t.Run("reports the tasks it is still waiting on", func(t *testing.T) {
		withDrainProgressInterval(t, 20*time.Millisecond)
		logs := captureLogs(t)
		reg := newDrainRegistry(t)
		const sess = "S"
		id := registerDrainTask(t, reg, sess)

		go func() {
			time.Sleep(120 * time.Millisecond) // several intervals
			reg.MarkFinished(id, task.StateCompleted, nil)
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := drainSessionTasks(ctx, reg, sess); err != nil {
			t.Fatalf("drain returned error: %v", err)
		}

		out := logs.String()
		for _, want := range []string{"still waiting on background tasks", id, "age="} {
			if !strings.Contains(out, want) {
				t.Errorf("progress log missing %q; got:\n%s", want, out)
			}
		}
	})

	t.Run("logs nothing when the drain does not wait", func(t *testing.T) {
		withDrainProgressInterval(t, 20*time.Millisecond)
		logs := captureLogs(t)
		reg := newDrainRegistry(t)

		if err := drainSessionTasks(context.Background(), reg, "empty"); err != nil {
			t.Fatalf("drain returned error: %v", err)
		}
		if strings.Contains(logs.String(), "still waiting on background tasks") {
			t.Errorf("progress logged for an empty drain:\n%s", logs.String())
		}
	})

	// The ticker is observability only. Per the background-tasks spec the
	// surrounding ctx is the sole deadline source, so many elapsed intervals
	// must NOT end the wait.
	t.Run("progress does not bound the wait", func(t *testing.T) {
		withDrainProgressInterval(t, 10*time.Millisecond)
		captureLogs(t)
		reg := newDrainRegistry(t)
		const sess = "S"
		registerDrainTask(t, reg, sess) // never finished

		ctx, cancel := context.WithCancel(context.Background()) // no deadline
		done := make(chan error, 1)
		go func() { done <- drainSessionTasks(ctx, reg, sess) }()

		select {
		case err := <-done:
			t.Fatalf("drain returned after progress intervals elapsed (err=%v); the ticker must not bound the wait", err)
		case <-time.After(150 * time.Millisecond): // ~15 intervals
		}

		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("drain err = %v, want context.Canceled", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("drain did not return after ctx cancellation")
		}
	})
}

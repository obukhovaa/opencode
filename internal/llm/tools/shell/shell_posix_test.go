//go:build !windows

package shell

import (
	"context"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestShellRunsInItsOwnSession is the direct assertion of the isolation
// invariant: a different session id means no controlling terminal, which is
// what stops a descendant from writing onto the terminal opencode renders on.
// Asserting the session id rather than a failed /dev/tty open makes the test
// meaningful even when the test process itself has no terminal.
func TestShellRunsInItsOwnSession(t *testing.T) {
	sh := newTestShell(t)

	ours, err := syscall.Getsid(0)
	if err != nil {
		t.Fatalf("Getsid(self): %v", err)
	}
	theirs, err := syscall.Getsid(sh.cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Getsid(shell): %v", err)
	}
	if theirs == ours {
		t.Fatalf("shell shares our session (%d); it must be detached so /dev/tty cannot resolve", ours)
	}
	if theirs != sh.cmd.Process.Pid {
		t.Errorf("shell session id = %d, want it to be the session leader (%d)", theirs, sh.cmd.Process.Pid)
	}
}

// TestSignalKilledShellReportsAShellStyleStatus: os.ProcessState.ExitCode
// reports -1 for a signal-terminated process, and "Exit code -1" is meaningless
// to whoever reads it. Shells report 128+N, which is the number a user
// comparing against their own terminal expects.
func TestSignalKilledShellReportsAShellStyleStatus(t *testing.T) {
	sh := newTestShell(t)

	_, _, exitCode, interrupted, err := sh.Exec(context.Background(), "kill -TERM $$", 10_000)
	if err != nil {
		t.Fatalf("Exec returned error: %v", err)
	}
	if interrupted {
		t.Error("a self-signalled shell was reported as interrupted")
	}
	if want := 128 + int(syscall.SIGTERM); exitCode != want {
		t.Errorf("exit code = %d, want %d (128+SIGTERM)", exitCode, want)
	}
}

// TestConcurrentExecSurvivesShellDeath is the regression test for a
// process-killing panic: Exec checked isAlive and then sent on the command
// queue, while the shell's death closed that same queue. Sending on a closed
// channel panics the sender's goroutine, and in production nothing recovers it
// — the whole of opencode goes down.
//
// It became reachable when shell death turned from an accident into a routine,
// designed event (`!exit`), with several concurrent Exec sources sharing one
// shell per working directory: the agent's parallel bash calls, the TUI's `!`
// mode, and the interactive handoff's cwd resync.
func TestConcurrentExecSurvivesShellDeath(t *testing.T) {
	sh := newTestShell(t)

	var panics atomic.Int32
	var wg sync.WaitGroup

	// Occupy the processor, then queue a command that ends the session behind
	// it, so the queue is still backed up when the shell dies.
	go func() { _, _, _, _, _ = sh.Exec(context.Background(), "sleep 2", 30_000) }()
	time.Sleep(200 * time.Millisecond)
	go func() { _, _, _, _, _ = sh.Exec(context.Background(), "exit 7", 30_000) }()
	time.Sleep(50 * time.Millisecond)

	// More callers than the queue's capacity, so most block inside the send.
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panics.Add(1)
				}
			}()
			_, _, _, _, _ = sh.Exec(context.Background(), "echo hi", 30_000)
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Exec callers hung after the shell died")
	}

	if n := panics.Load(); n != 0 {
		t.Errorf("%d Exec callers panicked; sending on the command queue must always be safe", n)
	}
}

// TestExecOnADeadShellReturnsPromptly: every caller must get an answer, whether
// its command was mid-flight, merely queued, or never accepted.
func TestExecOnADeadShellReturnsPromptly(t *testing.T) {
	sh := newTestShell(t)
	if _, _, _, _, err := sh.Exec(context.Background(), "exit 0", 30_000); err != nil {
		t.Fatalf("Exec returned error: %v", err)
	}
	if sh.isAlive() {
		t.Fatal("the shell survived an exit; this test proves nothing")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, stderr, _, _, err := sh.Exec(context.Background(), "echo hi", 30_000)
		if err == nil {
			t.Errorf("Exec on a dead shell returned no error (stderr=%q)", stderr)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Exec on a dead shell never returned")
	}
}

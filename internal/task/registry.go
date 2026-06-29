package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrTaskExists is returned by Registry.Register when a Task with the same
// ID is already present. Callers should treat this as a programming error —
// task IDs are 128-bit random and collisions are astronomically unlikely.
var ErrTaskExists = errors.New("task: duplicate task id")

// ErrTaskNotFound is returned by Registry.Get / Kill when the task ID is
// unknown.
var ErrTaskNotFound = errors.New("task: not found")

// ErrTaskTerminal is returned by Registry.Kill when the task is already in a
// terminal state. The kill is a no-op in this case.
var ErrTaskTerminal = errors.New("task: already terminal")

// Registry is the per-process source of truth for in-flight background tasks.
// All methods are safe for concurrent use.
type Registry interface {
	Register(t *Task) error
	Get(taskID string) (*Task, bool)
	ListBySession(sessionID string) []*Task
	// PendingForSession returns a snapshot of currently-running tasks for
	// the session, optionally filtered. Pass nil filter to include every
	// running task. Tasks that have transitioned to a terminal state are
	// excluded regardless of filter.
	PendingForSession(sessionID string, filter func(*Task) bool) []*Task
	// WaitForActiveTasks blocks until every pending task in the
	// snapshot-at-call-start transitions to terminal state, or until ctx
	// is cancelled. Returns ctx.Err() on cancellation, nil on clean
	// completion of all snapshotted tasks. Tasks registered AFTER the
	// call begins are NOT included (snapshot-at-start semantics).
	WaitForActiveTasks(ctx context.Context, sessionID string, opts WaitOptions) error
	Kill(taskID string) error
	MarkFinished(taskID string, s State, exitCode *int)
	PrepareOutputFile(taskID string) (path string, f *os.File, err error)
	SweepOrphans(dataDir string)
}

// WaitOptions configures Registry.WaitForActiveTasks.
type WaitOptions struct {
	// IncludeMonitor: include KindMonitor tasks in the wait set. The
	// zero value is false (Go default). Non-interactive callers in
	// agent.processGeneration MUST set this to true — monitor lifetimes
	// in flow steps are bounded by max_events / taskstop / finite cmd,
	// not by the runtime. When false, monitor tasks are filtered out of
	// the wait set and the function may return before they reach a
	// terminal state.
	IncludeMonitor bool
}

type registry struct {
	mu      sync.RWMutex
	tasks   map[string]*Task
	dataDir func() string
}

// NewRegistry returns a Registry whose output files live under
// `dataDirFn()/tasks/`. dataDirFn is called lazily so it can pick up the
// loaded config after boot ordering completes.
func NewRegistry(dataDirFn func() string) Registry {
	return &registry{
		tasks:   make(map[string]*Task),
		dataDir: dataDirFn,
	}
}

func (r *registry) Register(t *Task) error {
	if t == nil || t.ID == "" {
		return errors.New("task: nil or empty-id task")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tasks[t.ID]; dup {
		return fmt.Errorf("%w: %s", ErrTaskExists, t.ID)
	}
	if t.StartedAt.IsZero() {
		t.StartedAt = time.Now()
	}
	t.state.Store(int32(StateRunning))
	if t.done == nil {
		t.done = make(chan struct{})
	}
	r.tasks[t.ID] = t
	return nil
}

func (r *registry) Get(taskID string) (*Task, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tasks[taskID]
	return t, ok
}

// PendingForSession returns a snapshot of running tasks for the session
// that pass the optional filter. Terminal tasks are never included.
func (r *registry) PendingForSession(sessionID string, filter func(*Task) bool) []*Task {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Task, 0)
	for _, t := range r.tasks {
		if t.SessionID != sessionID {
			continue
		}
		if t.State() != StateRunning {
			continue
		}
		if filter != nil && !filter(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// WaitForActiveTasks blocks until every pending task in the
// snapshot-at-call-start transitions to terminal state, or until ctx is
// cancelled. Snapshot is taken once at call entry; tasks registered after
// the wait begins are not observed.
func (r *registry) WaitForActiveTasks(ctx context.Context, sessionID string, opts WaitOptions) error {
	filter := func(t *Task) bool {
		if t.Kind == KindMonitor && !opts.IncludeMonitor {
			return false
		}
		return true
	}
	snapshot := r.PendingForSession(sessionID, filter)
	if len(snapshot) == 0 {
		return nil
	}
	for _, t := range snapshot {
		if t.State() != StateRunning {
			continue
		}
		select {
		case <-t.Done():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (r *registry) ListBySession(sessionID string) []*Task {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Task, 0)
	for _, t := range r.tasks {
		if t.SessionID == sessionID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

// MarkFinished transitions a task to a terminal state and records the exit
// code if provided. Idempotent — a task already terminal stays at its
// recorded state (we do not overwrite e.g. a Killed with a Failed).
func (r *registry) MarkFinished(taskID string, s State, exitCode *int) {
	r.mu.RLock()
	t, ok := r.tasks[taskID]
	r.mu.RUnlock()
	if !ok {
		return
	}
	cur := t.State()
	if cur != StateRunning {
		return
	}
	t.state.Store(int32(s))
	t.finishedAtNanos.Store(time.Now().UnixNano())
	if exitCode != nil {
		t.exitCode.Store(int32(*exitCode))
		t.hasExit.Store(true)
	}
	t.signalDone()
}

// Kill transitions the task to StateKilled and signals/cancels its
// underlying work. For subprocesses (Bash/Monitor) it sends SIGTERM to the
// task's process group — spawn paths set Setpgid:true so the leaf and its
// children (e.g. a wrapping bash and the test binaries it launched) all
// receive the signal together. For subagents (Task) it calls the stored
// CancelFunc. The kill is best-effort — the originating tool's monitor
// goroutine is what eventually fires the terminal completion notification
// via EnqueueTaskCompletion.
func (r *registry) Kill(taskID string) error {
	r.mu.RLock()
	t, ok := r.tasks[taskID]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	if t.State() != StateRunning {
		return ErrTaskTerminal
	}
	// Mark the state up front so concurrent observers see the killed status
	// even if the underlying signal hasn't been delivered yet.
	t.state.Store(int32(StateKilled))
	t.finishedAtNanos.Store(time.Now().UnixNano())
	t.signalDone()
	switch {
	case t.Proc != nil:
		SignalProcessGroup(t.Proc, terminateSignal())
	case t.Cancel != nil:
		t.Cancel()
	}
	return nil
}

// PrepareOutputFile creates `<dataDir>/tasks/<task_id>.out` with mode 0o600
// after MkdirAll on the parent (mode 0o700). The file is opened with
// O_CREATE|O_EXCL|O_WRONLY so two tasks with the same ID cannot share a
// file. The caller is responsible for closing the returned *os.File.
func (r *registry) PrepareOutputFile(taskID string) (string, *os.File, error) {
	dir := filepath.Join(r.dataDir(), "tasks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("task: prepare tasks dir: %w", err)
	}
	path := filepath.Join(dir, taskID+".out")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", nil, fmt.Errorf("task: create output file: %w", err)
	}
	return path, f, nil
}

// SweepOrphans deletes every `*.out` under `<dataDir>/tasks/` that does not
// correspond to a live registry entry. The registry is in-memory only, so
// after restart this removes every file in the directory. K8s pods reset
// the whole tree between runs anyway; the sweep matters only for long-lived
// dev sessions.
func (r *registry) SweepOrphans(dataDir string) {
	if dataDir == "" {
		dataDir = r.dataDir()
	}
	dir := filepath.Join(dataDir, "tasks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Missing directory is fine — the first PrepareOutputFile creates it.
		return
	}
	r.mu.RLock()
	live := make(map[string]struct{}, len(r.tasks))
	for id := range r.tasks {
		live[id] = struct{}{}
	}
	r.mu.RUnlock()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".out") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".out")
		if _, alive := live[id]; alive {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

// global is the process-wide singleton. Initialized lazily via
// SetGlobalRegistry from the app boot path so dataDir is available.
var (
	globalMu sync.RWMutex
	global   Registry
)

// SetGlobalRegistry installs the process-wide Registry. Called once at app
// boot (see internal/app/). A second call panics — this is a programmer
// error.
func SetGlobalRegistry(r Registry) {
	globalMu.Lock()
	defer globalMu.Unlock()
	if global != nil {
		panic("task: SetGlobalRegistry called twice")
	}
	global = r
}

// GlobalRegistry returns the installed global Registry. If none has been
// installed, returns nil — callers should fail gracefully (e.g. background
// modes return an error to the agent).
func GlobalRegistry() Registry {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}

// ResetGlobalRegistry is intended for tests only. It clears the singleton
// so a subsequent SetGlobalRegistry call succeeds.
func ResetGlobalRegistry() {
	globalMu.Lock()
	defer globalMu.Unlock()
	global = nil
}

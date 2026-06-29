package cron

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/task"
)

// AgentBusyChecker checks whether the active session's agent is busy and lets
// the cron scheduler hold the session-busy slot for the brief window in which
// it commits its synthetic tool_call/tool_result pair.
//
// TryLockSession returns true if the slot was free and is now held by the
// caller. UnlockSession releases it. Sharing this primitive with the agent's
// per-session busy state guarantees that the agent cannot start a Run that
// would interleave its messages with the cron's atomic pair.
type AgentBusyChecker interface {
	IsSessionBusy(sessionID string) bool
	TryLockSession(sessionID string) bool
	UnlockSession(sessionID string)
}

// ActiveSessionProvider returns the current active session ID.
type ActiveSessionProvider interface {
	ActiveSessionID() string
}

// PermissionResolverChecker reports whether a session has an out-of-band
// permission resolver attached (e.g. the chat bridge's PermissionRouter
// for bridge-bound sessions). When this returns true for a job's session,
// the scheduler will call permissions.Request() instead of deferring on
// the active-session gate — the resolver will answer the request quickly,
// so there is no risk of a permission dialog hanging in a session that no
// human is actively watching in the TUI.
//
// The scheduler treats a nil checker as "no out-of-band resolvers" and
// keeps the legacy active-session-only behaviour (which the standalone
// TUI deployment depends on).
type PermissionResolverChecker interface {
	HasPermissionResolver(ctx context.Context, sessionID string) bool
}

// TaskRunner executes a task via the task tool.
type TaskRunner interface {
	RunTask(ctx context.Context, call tools.ToolCall) (tools.ToolResponse, error)
}

// Scheduler runs cron jobs on a 1-second tick.
type Scheduler struct {
	svc         *service
	messages    message.Service
	sessions    session.Service
	permissions permission.Service
	busyChecker AgentBusyChecker
	taskRunner  TaskRunner

	provMu            sync.RWMutex
	activeSessionProv ActiveSessionProvider

	resolverMu      sync.RWMutex
	permResolverChk PermissionResolverChecker

	leaderMu sync.RWMutex
	leader   LeaderLock

	// transitionHook overrides onLeaderTransition's default body when set.
	// Test-only; production wiring leaves it nil so ClearStaleFiring runs.
	transitionHook func(context.Context)

	stopOnce sync.Once

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	mu        sync.Mutex // guards sessionMutexes
	sessionMu map[string]*sync.Mutex
}

func NewScheduler(
	svc Service,
	messages message.Service,
	sessions session.Service,
	permissions permission.Service,
	busyChecker AgentBusyChecker,
	activeSessionProv ActiveSessionProvider,
	taskRunner TaskRunner,
) *Scheduler {
	return &Scheduler{
		svc:               svc.(*service),
		messages:          messages,
		sessions:          sessions,
		permissions:       permissions,
		busyChecker:       busyChecker,
		activeSessionProv: activeSessionProv,
		taskRunner:        taskRunner,
		sessionMu:         make(map[string]*sync.Mutex),
	}
}

// SetActiveSessionProvider injects the active-session provider after construction.
// The TUI calls this once it has wired up its session-tracking state.
func (s *Scheduler) SetActiveSessionProvider(prov ActiveSessionProvider) {
	s.provMu.Lock()
	defer s.provMu.Unlock()
	s.activeSessionProv = prov
}

// SetPermissionResolverChecker injects the resolver checker after
// construction. The chat bridge wires itself here once Service.Start
// finishes (the bridge is constructed AFTER the scheduler in app.New /
// serve.go) so the cron scheduler can recognise bridge-bound sessions
// as "watched" and proceed to permissions.Request() instead of
// deferring 60s/tick forever on the active-session gate.
func (s *Scheduler) SetPermissionResolverChecker(c PermissionResolverChecker) {
	s.resolverMu.Lock()
	defer s.resolverMu.Unlock()
	s.permResolverChk = c
}

func (s *Scheduler) activeSessionID() string {
	s.provMu.RLock()
	prov := s.activeSessionProv
	s.provMu.RUnlock()
	if prov == nil {
		return ""
	}
	return prov.ActiveSessionID()
}

func (s *Scheduler) hasPermissionResolver(ctx context.Context, sessionID string) bool {
	s.resolverMu.RLock()
	chk := s.permResolverChk
	s.resolverMu.RUnlock()
	if chk == nil {
		return false
	}
	return chk.HasPermissionResolver(ctx, sessionID)
}

// SetLeaderLock wires the cross-process leader lock that pins cron
// scheduling to a single opencode process per DB. When unset (nil) every
// scheduler ticks freely — that is the right default for unit tests and
// for deployments that never run more than one opencode process. The
// caller is responsible for constructing the lock with the correct
// provider (SQLite file lock or MySQL GET_LOCK); see NewLeaderLock.
//
// If a prior lock was set, replacing it with a different reference (or
// with nil) releases the displaced lock. Otherwise a runtime swap would
// leak the file descriptor / MySQL conn the old lock holds. Release
// errors are logged at Warn and otherwise swallowed — they cannot
// propagate through the setter, and the new lock should take over
// regardless.
func (s *Scheduler) SetLeaderLock(l LeaderLock) {
	s.leaderMu.Lock()
	prev := s.leader
	s.leader = l
	s.leaderMu.Unlock()
	if prev != nil && prev != l {
		if err := prev.Release(); err != nil {
			logging.Warn("Cron leader lock release on replace failed", "error", err)
		}
	}
}

// isLeader reports whether this process currently owns the lock. With no
// lock configured, every process is a leader (backward-compatible
// single-process behaviour).
func (s *Scheduler) isLeader() bool {
	s.leaderMu.RLock()
	l := s.leader
	s.leaderMu.RUnlock()
	if l == nil {
		return true
	}
	return l.Held()
}

// tryAcquireLeadership attempts to grab the leader lock. Returns
// (newlyAcquired, reason):
//
//   - (true, nil)             — just transitioned to leader; caller should
//     run once-per-transition setup (ClearStaleFiring).
//   - (false, nil)            — no lock configured OR already held.
//   - (false, ErrLeaderHeld)  — a peer process is the current leader
//     (expected steady state, silent).
//   - (false, other err)      — transport failure (already logged at Warn
//     by this method; caller may inspect for boot-vs-retry messaging).
func (s *Scheduler) tryAcquireLeadership(ctx context.Context) (bool, error) {
	s.leaderMu.RLock()
	l := s.leader
	s.leaderMu.RUnlock()
	if l == nil {
		return false, nil
	}
	if l.Held() {
		return false, nil
	}
	if err := l.TryAcquire(ctx); err != nil {
		if errors.Is(err, ErrLeaderHeld) {
			// Expected when another opencode process is the leader.
			// Silent on purpose — the 5s retry would otherwise spam
			// the log in a stable two-process deployment.
			return false, err
		}
		logging.Warn("Cron leader lock acquire failed", "error", err)
		return false, err
	}
	logging.Info("Cron scheduler became leader")
	return true, nil
}

// onLeaderTransition runs the once-per-transition cleanup. Stale firing=true
// rows from a crashed predecessor would otherwise be invisible to
// ListDueCronJobs forever (it filters out firing=true). Cheap single
// UPDATE; safe to re-run on every transition.
//
// Tests in this package may override transitionHook to observe transitions
// without needing a real *service to wire up.
func (s *Scheduler) onLeaderTransition(ctx context.Context) {
	if s.transitionHook != nil {
		s.transitionHook(ctx)
		return
	}
	if err := s.svc.ClearStaleFiring(ctx); err != nil {
		logging.Error("Cron leader transition: clear stale firing failed", "error", err)
	}
}

// pingLeadership verifies the lock is still ours and downgrades to
// follower on connection loss. For MySQL this is the load-bearing call —
// without it a killed *sql.Conn would silently release the server-side
// GET_LOCK while Held() kept reporting true, letting this process race
// the new leader on ClaimForFiring. Runs every 30s from the run loop.
func (s *Scheduler) pingLeadership(ctx context.Context) {
	s.leaderMu.RLock()
	l := s.leader
	s.leaderMu.RUnlock()
	if l == nil || !l.Held() {
		return
	}
	if err := l.Ping(ctx); err != nil {
		if errors.Is(err, ErrLeaderHeld) {
			logging.Info("Cron leader lock lost; downgrading to follower (peer will take over)")
			return
		}
		logging.Warn("Cron leader lock ping failed", "error", err)
	}
}

// Start initializes cron jobs from DB and starts the scheduler goroutine.
func (s *Scheduler) Start(ctx context.Context) {
	// Handle startup: clear stale firing, recompute next_run_at, surface missed one-shots
	s.svc.InitStartup(ctx)

	ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)
	go func() {
		defer logging.RecoverPanic("cron-scheduler", nil)
		defer s.wg.Done()
		s.run(ctx)
	}()
	logging.Info("Cron scheduler started")
}

// Stop stops the scheduler goroutine and releases the leader lock so a
// peer process can take over within its next retry tick. Guarded by
// sync.Once so a Shutdown + ForceShutdown sequence (or a duplicate
// signal handler) cannot double-release the lock.
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
			s.wg.Wait()
		}
		s.leaderMu.RLock()
		l := s.leader
		s.leaderMu.RUnlock()
		if l != nil {
			if err := l.Release(); err != nil {
				logging.Warn("Cron leader lock release failed", "error", err)
			}
		}
		logging.Info("Cron scheduler stopped")
	})
}

func (s *Scheduler) run(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Followers retry leadership every 5s so a peer's crash hands over
	// scheduling within a bounded delay. The retry is independent of the
	// per-second tick so the loss-of-cache cost stays bounded even if the
	// DB call is slow.
	retryTicker := time.NewTicker(5 * time.Second)
	defer retryTicker.Stop()

	// Pinging the lock guards against the MySQL "conn dropped, server
	// released GET_LOCK, peer grabbed it" split-brain. 30s matches the
	// bridge's ping cadence — short enough that a misowned leader stops
	// claiming jobs within a manageable window, long enough that the
	// DB doesn't see meaningful extra load.
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	// Boot acquire. If a lock is configured and we don't immediately
	// become leader, surface a single INFO line so operators can tell
	// from the log whether this process is the scheduler or a follower.
	s.bootAcquire(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !s.isLeader() {
				continue
			}
			s.tick(ctx)
		case <-retryTicker.C:
			if s.isLeader() {
				continue
			}
			if acquired, _ := s.tryAcquireLeadership(ctx); acquired {
				s.onLeaderTransition(ctx)
			}
		case <-pingTicker.C:
			s.pingLeadership(ctx)
		}
	}
}

// bootAcquire runs once at scheduler start. With no lock configured it
// is a no-op. With a lock it either acquires immediately (this process
// becomes leader) or logs a single INFO line announcing follower state.
// The follower message distinguishes "peer holds it" (steady-state
// two-process deployment, expected) from "lock unavailable" (DB unhealthy
// at startup, retry will eventually succeed) so the role of this process
// is unambiguous in the logs.
func (s *Scheduler) bootAcquire(ctx context.Context) {
	s.leaderMu.RLock()
	l := s.leader
	s.leaderMu.RUnlock()
	if l == nil {
		return
	}
	acquired, reason := s.tryAcquireLeadership(ctx)
	if acquired {
		s.onLeaderTransition(ctx)
		return
	}
	// (false, nil) covers two cases: lock not configured (handled above)
	// or l.Held() was already true before we called tryAcquireLeadership.
	// The second case is reachable if a caller pre-acquires the lock
	// before Start — today nobody does, but treat it as a transition so
	// onLeaderTransition runs (ClearStaleFiring would otherwise be
	// skipped despite this process effectively being the new leader).
	if reason == nil && l.Held() {
		logging.Info("Cron scheduler started as leader (lock pre-acquired)")
		s.onLeaderTransition(ctx)
		return
	}
	switch {
	case errors.Is(reason, ErrLeaderHeld):
		logging.Info("Cron scheduler started as follower (peer process holds the lock; will retry every 5s)")
	case reason != nil:
		// tryAcquireLeadership already logged a Warn with the underlying
		// error; this Info just makes the follower state explicit.
		logging.Info("Cron scheduler started as follower (lock unavailable; will retry every 5s)")
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	now := time.Now()
	dueJobs, err := s.svc.ListDue(ctx, now)
	if err != nil {
		logging.Error("Failed to list due cron jobs", "error", err)
		return
	}

	// Group by session for serialized execution
	bySession := make(map[string][]CronJob)
	for _, job := range dueJobs {
		bySession[job.SessionID] = append(bySession[job.SessionID], job)
	}

	for sessionID, jobs := range bySession {
		sessionID := sessionID
		jobs := jobs

		// Skip and retry next tick if an agent.Run is already in flight
		// on this session — interleaving a synthetic tool_call/result pair
		// with a live Run would split that Run's message stream. The check
		// applies to any session (TUI active OR bridge-bound), because
		// either surface can hold the agent busy-lock.
		if s.busyChecker != nil && s.busyChecker.IsSessionBusy(sessionID) {
			continue
		}

		// Fire jobs for this session serially in a goroutine
		s.wg.Add(1)
		go func() {
			defer logging.RecoverPanic("cron-fire", nil)
			defer s.wg.Done()

			mu := s.getSessionMutex(sessionID)
			mu.Lock()
			defer mu.Unlock()

			for _, job := range jobs {
				if ctx.Err() != nil {
					return
				}
				s.fireJob(ctx, job)
			}
		}()
	}
}

func (s *Scheduler) fireJob(ctx context.Context, job CronJob) {
	// Atomically claim the row. If another tick already started executing this
	// job (or the row was advanced by InitStartup), skip without touching it.
	claimed, err := s.svc.ClaimForFiring(ctx, job.ID, time.Now())
	if err != nil {
		logging.Error("Failed to claim cron job for firing", "id", job.ID, "error", err)
		return
	}
	if !claimed {
		return
	}

	// Permission check: keyed on "cron:<job_id>"
	if s.permissions != nil && !s.permissions.IsAutoApproveSession(job.SessionID) {
		activeSessionID := s.activeSessionID()
		// For inactive sessions with no out-of-band resolver, defer until
		// the session becomes active. A bridge-bound session counts as
		// "has a resolver" — the bridge's PermissionRouter answers
		// permission requests synchronously per cfg.Router.PermissionMode,
		// so a chat user's crons would otherwise never fire. Without
		// advancing next_run_at the row would stay due and pulse
		// firing=true/false every tick (1 DB write/sec/job); push the
		// next attempt out by 60s so churn stays bounded.
		if job.SessionID != activeSessionID && !s.hasPermissionResolver(ctx, job.SessionID) {
			logging.Debug("Cron job on unwatched session, deferring permission check", "id", job.ID)
			s.deferAndClear(ctx, job, time.Now().Add(60*time.Second))
			return
		}

		permPath := fmt.Sprintf("cron:%s", job.ID)
		granted := s.permissions.Request(ctx, permission.CreatePermissionRequest{
			SessionID:   job.SessionID,
			ToolName:    "cron",
			Action:      "execute",
			Path:        permPath,
			Description: fmt.Sprintf("⏲ Cron job %s: %s (%s)", job.ID, job.TaskTitle, job.Schedule),
		})
		if !granted {
			logging.Info("Cron job permission denied", "id", job.ID)
			// Advance to the next scheduled fire so a recurring job does not
			// re-prompt every tick. One-shots are marked done.
			s.deferOnDenial(ctx, job)
			return
		}
	}

	logging.Info("Cron job firing", "id", job.ID, "schedule", job.Schedule, "title", job.TaskTitle)

	// Generate unique call_id
	callID := generateCallID()

	// Build task input
	input := map[string]string{
		"prompt":        job.Prompt,
		"subagent_type": job.SubagentType,
		"task_id":       job.TaskID,
		"task_title":    "⏲ " + job.TaskTitle,
	}
	inputJSON, _ := json.Marshal(input)

	// Build context with session ID and sentinel message ID
	taskCtx := context.WithValue(ctx, tools.SessionIDContextKey, job.SessionID)
	taskCtx = context.WithValue(taskCtx, tools.MessageIDContextKey, fmt.Sprintf("cron:%s:%d", job.ID, job.RunCount))

	// Execute the task
	var result tools.ToolResponse
	var runErr error
	if s.taskRunner != nil {
		result, runErr = s.taskRunner.RunTask(taskCtx, tools.ToolCall{
			ID:    callID,
			Name:  "task",
			Input: string(inputJSON),
		})
	} else {
		runErr = fmt.Errorf("no task runner configured")
	}

	if runErr != nil {
		logging.Error("Cron job execution failed", "id", job.ID, "error", runErr)
		if updateErr := s.svc.UpdateError(ctx, job, runErr.Error()); updateErr != nil {
			logging.Error("Failed to update cron job error", "id", job.ID, "error", updateErr)
		}
		return
	}

	resultContent := result.Content

	// Always advance next_run_at after a successful execution — even if we
	// can't commit synthetic messages below — so the task does not re-fire
	// on the next tick. UpdateAfterRun also clears firing and stores
	// last_result so the user can see the output via the cron jobs page.
	if _, err := s.svc.UpdateAfterRun(ctx, job, resultContent); err != nil {
		logging.Error("Failed to update cron job after run", "id", job.ID, "error", err)
	}

	// Try to commit synthetic messages into the parent session atomically.
	// Hold the session-busy slot while the transaction commits so the agent
	// cannot start a Run that would insert messages between our tool_call
	// and its tool_result. If the slot is already held (e.g. user just sent
	// a message and the agent grabbed it after our IsSessionBusy check),
	// skip the synthetic write — the task output is preserved on the cron
	// row and visible via the cron jobs page.
	if s.busyChecker != nil {
		if !s.busyChecker.TryLockSession(job.SessionID) {
			logging.Warn("Cron job: session became busy after task ran, skipping synthetic write", "id", job.ID)
			return
		}
		defer s.busyChecker.UnlockSession(job.SessionID)
	}

	if err := s.writeSyntheticMessages(ctx, job, callID, string(inputJSON), resultContent); err != nil {
		logging.Error("Failed to write synthetic messages", "id", job.ID, "error", err)
	}

	logging.Info("Cron job completed", "id", job.ID, "schedule", job.Schedule)
}

// deferAndClear pushes next_run_at out and clears the firing flag. Used for
// the inactive-session deferral path to avoid per-tick churn.
func (s *Scheduler) deferAndClear(ctx context.Context, job CronJob, next time.Time) {
	if err := s.svc.RescheduleAndClear(ctx, job.ID, next); err != nil {
		logging.Error("Failed to defer cron job", "id", job.ID, "error", err)
	}
}

// deferOnDenial advances next_run_at to the next scheduled fire after a
// permission denial. Recurring jobs are pushed to schedule.Next(now);
// one-shots are marked done so they never re-prompt.
func (s *Scheduler) deferOnDenial(ctx context.Context, job CronJob) {
	if !job.IsRecurring {
		if err := s.svc.MarkDone(ctx, job.ID); err != nil {
			logging.Error("Failed to mark one-shot done after denial", "id", job.ID, "error", err)
		}
		return
	}
	next, err := ComputeNextFire(job.Schedule, time.Now())
	if err != nil {
		logging.Error("Failed to compute next fire after denial", "id", job.ID, "error", err)
		// Fallback: nudge by a minute so we don't re-prompt every tick.
		next = time.Now().Add(time.Minute)
	}
	if err := s.svc.RescheduleAndClear(ctx, job.ID, next); err != nil {
		logging.Error("Failed to reschedule cron job after denial", "id", job.ID, "error", err)
	}
}

func (s *Scheduler) writeSyntheticMessages(ctx context.Context, job CronJob, callID, inputJSON, resultContent string) error {
	// Delegates to the shared task-notifications primitive. The end-state
	// pair shape is identical to the previous hand-written CreatePair call,
	// with two additions: (a) the Assistant message carries `synthetic: true`
	// so the bridge filter suppresses its tool-update indicator emission,
	// and (b) any other synthetic background-task code path observes the
	// same write semantics. Cron retains its own session-busy lock (held by
	// the caller) so SuppressIfNotified is false — the dedupe gate is not
	// relevant to cron.
	return task.EnqueueTaskCompletion(ctx, task.CompletionInput{
		SessionID:             job.SessionID,
		OriginatingToolCallID: callID,
		OriginatingToolName:   "task",
		TaskID:                job.ID,
		Kind:                  task.KindCron,
		Status:                task.StatusCompleted,
		Input:                 inputJSON,
		Content:               resultContent,
		SuppressIfNotified:    false,
	})
}

func (s *Scheduler) getSessionMutex(sessionID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessionMu[sessionID]; !ok {
		s.sessionMu[sessionID] = &sync.Mutex{}
	}
	return s.sessionMu[sessionID]
}

// CleanupSession removes in-memory state for a deleted session.
func (s *Scheduler) CleanupSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessionMu, sessionID)
}

func generateCallID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("toolu_%d", time.Now().UnixNano())
	}
	return "toolu_" + hex.EncodeToString(b)
}

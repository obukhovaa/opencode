package cron

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/session"
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

func (s *Scheduler) activeSessionID() string {
	s.provMu.RLock()
	prov := s.activeSessionProv
	s.provMu.RUnlock()
	if prov == nil {
		return ""
	}
	return prov.ActiveSessionID()
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

// Stop stops the scheduler goroutine.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
		s.wg.Wait()
	}
	logging.Info("Cron scheduler stopped")
}

func (s *Scheduler) run(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
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

	activeSessionID := s.activeSessionID()

	for sessionID, jobs := range bySession {
		sessionID := sessionID
		jobs := jobs

		// If this is the active session and the agent is busy, skip and retry next tick.
		if sessionID == activeSessionID && s.busyChecker != nil && s.busyChecker.IsSessionBusy(sessionID) {
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
		// For inactive sessions with no prior grant, skip and retry when session becomes active
		if job.SessionID != activeSessionID {
			logging.Debug("Cron job on inactive session, deferring permission check", "id", job.ID)
			if err := s.svc.MarkFiring(ctx, job.ID, false); err != nil {
				logging.Error("Failed to clear firing flag", "id", job.ID, "error", err)
			}
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
			if err := s.svc.MarkFiring(ctx, job.ID, false); err != nil {
				logging.Error("Failed to clear firing flag after denial", "id", job.ID, "error", err)
			}
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

	// Write synthetic messages into parent session atomically. Hold the
	// session-busy slot while the transaction commits so the agent cannot
	// start a Run that would insert messages between our tool_call and its
	// tool_result. If the slot is already held (e.g. user just sent a message
	// and the agent grabbed it after our IsSessionBusy check), defer to the
	// next tick — task output is already preserved on the cron row.
	if s.busyChecker != nil {
		if !s.busyChecker.TryLockSession(job.SessionID) {
			logging.Debug("Cron job: session became busy after task ran, deferring synthetic write", "id", job.ID)
			if err := s.svc.MarkFiring(ctx, job.ID, false); err != nil {
				logging.Error("Failed to clear firing flag after defer", "id", job.ID, "error", err)
			}
			return
		}
		defer s.busyChecker.UnlockSession(job.SessionID)
	}

	if err := s.writeSyntheticMessages(ctx, job, callID, string(inputJSON), resultContent); err != nil {
		logging.Error("Failed to write synthetic messages", "id", job.ID, "error", err)
	}

	// Update job after run
	if _, err := s.svc.UpdateAfterRun(ctx, job, resultContent); err != nil {
		logging.Error("Failed to update cron job after run", "id", job.ID, "error", err)
	}

	logging.Info("Cron job completed", "id", job.ID, "schedule", job.Schedule)
}

func (s *Scheduler) writeSyntheticMessages(ctx context.Context, job CronJob, callID, inputJSON, resultContent string) error {
	// Write both messages atomically in a single transaction with consecutive seq numbers.
	_, _, err := s.messages.CreatePair(ctx, job.SessionID,
		message.CreateMessageParams{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:       callID,
					Name:     "task",
					Input:    inputJSON,
					Type:     "tool_use",
					Finished: true,
				},
			},
		},
		message.CreateMessageParams{
			Role: message.Tool,
			Parts: []message.ContentPart{
				message.ToolResult{
					Type:       message.ToolResultTypeText,
					ToolCallID: callID,
					Name:       "task",
					Content:    resultContent,
				},
			},
		},
	)
	return err
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

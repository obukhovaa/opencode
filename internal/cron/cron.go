package cron

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/pubsub"
	cronparser "github.com/robfig/cron/v3"
)

const (
	StatusActive = "active"
	StatusPaused = "paused"
	StatusDone   = "done"

	SourceLoop  = "loop"
	SourceAgent = "agent"

	MaxJobsPerSession = 50
)

type MissedAction int

const (
	MissedActionRunNow MissedAction = iota
	MissedActionDiscard
	MissedActionKeep
)

type CronJob struct {
	ID           string
	SessionID    string
	Schedule     string
	Prompt       string
	SubagentType string
	TaskTitle    string
	TaskID       string
	IsRecurring  bool
	Source       string
	Status       string
	Firing       bool
	LastRunAt    int64
	NextRunAt    int64
	RunCount     int64
	LastResult   string
	Error        string
	CreatedAt    int64
	UpdatedAt    int64
}

type CreateParams struct {
	SessionID    string
	Schedule     string
	Prompt       string
	SubagentType string
	TaskTitle    string
	IsRecurring  bool
	Source       string
	// FireImmediately sets next_run_at = time.Now() so the scheduler picks
	// the job up on its very next tick. Subsequent runs follow the schedule
	// normally. Used by /loop to match user mental model: "every 5 min check
	// the deploy" should check now AND every 5 min.
	FireImmediately bool
}

type MissedOneShotsEvent struct {
	Jobs []CronJob
}

type Service interface {
	pubsub.Suscriber[CronJob]
	Create(ctx context.Context, params CreateParams) (CronJob, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, sessionID string) ([]CronJob, error)
	ListActive(ctx context.Context) ([]CronJob, error)
	CountActive(ctx context.Context, sessionID string) (int64, error)
	Get(ctx context.Context, id string) (CronJob, error)
	ResolveMissedOneShot(ctx context.Context, id string, action MissedAction) error

	// SubscribeMissed returns a channel that receives MissedOneShotsEvent on startup.
	SubscribeMissed(ctx context.Context) <-chan pubsub.Event[MissedOneShotsEvent]
}

type service struct {
	*pubsub.Broker[CronJob]
	q            db.Querier
	missedBroker *pubsub.Broker[MissedOneShotsEvent]
	createMu     sync.Mutex // serializes Create to enforce MaxJobsPerSession

	// pendingMissed holds an event collected during InitStartup that ran
	// before any subscriber existed. The first SubscribeMissed call drains
	// it and re-publishes — without this, the missed-one-shots dialog at
	// startup would never appear because pubsub.Broker drops events when
	// there are no subscribers.
	pendingMu     sync.Mutex
	pendingMissed *MissedOneShotsEvent
}

func NewService(q db.Querier) Service {
	return &service{
		Broker:       pubsub.NewBroker[CronJob](),
		q:            q,
		missedBroker: pubsub.NewBroker[MissedOneShotsEvent](),
	}
}

func (s *service) SubscribeMissed(ctx context.Context) <-chan pubsub.Event[MissedOneShotsEvent] {
	ch := s.missedBroker.Subscribe(ctx)
	s.pendingMu.Lock()
	pending := s.pendingMissed
	s.pendingMissed = nil
	s.pendingMu.Unlock()
	if pending != nil {
		// Publish asynchronously so we return ch first; the broker delivers
		// to all current subscribers including the one we just registered.
		go s.missedBroker.Publish(pubsub.CreatedEvent, *pending)
	}
	return ch
}

func (s *service) Create(ctx context.Context, params CreateParams) (CronJob, error) {
	// Validate schedule
	schedule, err := ParseSchedule(params.Schedule)
	if err != nil {
		return CronJob{}, fmt.Errorf("invalid cron expression %q: %w", params.Schedule, err)
	}

	// Verify it has a future match
	nextFire := schedule.Next(time.Now())
	if nextFire.IsZero() || nextFire.After(time.Now().Add(366*24*time.Hour)) {
		return CronJob{}, fmt.Errorf("cron expression %q does not match any calendar date in the next year", params.Schedule)
	}

	// Serialize the cap check + insert so two concurrent calls cannot both pass
	// the limit. The cap is per-process; multiple opencode processes against the
	// same DB can still race, but that is not a supported configuration today.
	s.createMu.Lock()
	defer s.createMu.Unlock()

	count, err := s.q.CountActiveCronJobsBySession(ctx, params.SessionID)
	if err != nil {
		return CronJob{}, fmt.Errorf("failed to count cron jobs: %w", err)
	}
	if count >= MaxJobsPerSession {
		return CronJob{}, fmt.Errorf("too many scheduled jobs (max %d). Cancel one first", MaxJobsPerSession)
	}

	id := generateID()
	taskID := generateTaskID()

	firstFire := nextFire
	if params.FireImmediately {
		firstFire = time.Now()
	}

	dbJob, err := s.q.CreateCronJob(ctx, db.CreateCronJobParams{
		ID:           id,
		SessionID:    params.SessionID,
		Schedule:     params.Schedule,
		Prompt:       params.Prompt,
		SubagentType: params.SubagentType,
		TaskTitle:    params.TaskTitle,
		TaskID:       taskID,
		IsRecurring:  params.IsRecurring,
		Source:       params.Source,
		Status:       StatusActive,
		NextRunAt:    sql.NullInt64{Int64: firstFire.Unix(), Valid: true},
	})
	if err != nil {
		return CronJob{}, fmt.Errorf("failed to create cron job: %w", err)
	}

	job := fromDBItem(dbJob)
	s.Publish(pubsub.CreatedEvent, job)
	return job, nil
}

func (s *service) Delete(ctx context.Context, id string) error {
	job, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.q.DeleteCronJob(ctx, id); err != nil {
		return fmt.Errorf("failed to delete cron job: %w", err)
	}
	s.Publish(pubsub.DeletedEvent, job)
	return nil
}

func (s *service) List(ctx context.Context, sessionID string) ([]CronJob, error) {
	dbJobs, err := s.q.ListCronJobsBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	jobs := make([]CronJob, len(dbJobs))
	for i, j := range dbJobs {
		jobs[i] = fromDBItem(j)
	}
	return jobs, nil
}

func (s *service) ListActive(ctx context.Context) ([]CronJob, error) {
	dbJobs, err := s.q.ListActiveCronJobs(ctx)
	if err != nil {
		return nil, err
	}
	jobs := make([]CronJob, len(dbJobs))
	for i, j := range dbJobs {
		jobs[i] = fromDBItem(j)
	}
	return jobs, nil
}

func (s *service) CountActive(ctx context.Context, sessionID string) (int64, error) {
	return s.q.CountActiveCronJobsBySession(ctx, sessionID)
}

func (s *service) Get(ctx context.Context, id string) (CronJob, error) {
	dbJob, err := s.q.GetCronJob(ctx, id)
	if err != nil {
		return CronJob{}, err
	}
	return fromDBItem(dbJob), nil
}

func (s *service) ResolveMissedOneShot(ctx context.Context, id string, action MissedAction) error {
	switch action {
	case MissedActionRunNow:
		return s.q.UpdateCronJobNextRun(ctx, db.UpdateCronJobNextRunParams{
			NextRunAt: sql.NullInt64{Int64: time.Now().Unix(), Valid: true},
			ID:        id,
		})
	case MissedActionDiscard:
		return s.q.UpdateCronJobStatus(ctx, db.UpdateCronJobStatusParams{
			Status: StatusDone,
			ID:     id,
		})
	case MissedActionKeep:
		return nil
	default:
		return fmt.Errorf("unknown missed action: %d", action)
	}
}

// InitStartup handles the startup sequence: clear stale firing, recompute next_run_at
// for recurring jobs, and collect missed one-shots.
func (s *service) InitStartup(ctx context.Context) {
	// Clear stale firing rows from prior crash
	if err := s.q.ClearStaleFiring(ctx); err != nil {
		logging.Error("Failed to clear stale firing rows", "error", err)
	}

	// Load all active jobs
	activeJobs, err := s.q.ListActiveCronJobs(ctx)
	if err != nil {
		logging.Error("Failed to load active cron jobs on startup", "error", err)
		return
	}

	now := time.Now()
	for _, dbJob := range activeJobs {
		if dbJob.IsRecurring {
			// Recurring: reschedule from now (drop missed windows)
			schedule, err := ParseSchedule(dbJob.Schedule)
			if err != nil {
				logging.Error("Failed to parse schedule on startup", "id", dbJob.ID, "error", err)
				continue
			}
			nextFire := schedule.Next(now)
			if err := s.q.UpdateCronJobNextRun(ctx, db.UpdateCronJobNextRunParams{
				NextRunAt: sql.NullInt64{Int64: nextFire.Unix(), Valid: true},
				ID:        dbJob.ID,
			}); err != nil {
				logging.Error("Failed to update next_run_at on startup", "id", dbJob.ID, "error", err)
			}
		}
	}

	// Collect missed one-shots
	missedJobs, err := s.q.ListMissedOneShots(ctx, sql.NullInt64{Int64: now.Unix(), Valid: true})
	if err != nil {
		logging.Error("Failed to list missed one-shots", "error", err)
		return
	}
	if len(missedJobs) > 0 {
		missed := make([]CronJob, len(missedJobs))
		for i, j := range missedJobs {
			missed[i] = fromDBItem(j)
		}
		event := MissedOneShotsEvent{Jobs: missed}
		// InitStartup runs synchronously inside app.New, before the TUI
		// has wired up its subscription. Buffer the event so the first
		// SubscribeMissed call can drain and re-publish it; otherwise
		// pubsub.Broker would drop it (no subscribers).
		if s.missedBroker.GetSubscriberCount() == 0 {
			s.pendingMu.Lock()
			s.pendingMissed = &event
			s.pendingMu.Unlock()
		} else {
			s.missedBroker.Publish(pubsub.CreatedEvent, event)
		}
	}
}

// ListDue returns jobs that are due for execution.
func (s *service) ListDue(ctx context.Context, now time.Time) ([]CronJob, error) {
	dbJobs, err := s.q.ListDueCronJobs(ctx, sql.NullInt64{Int64: now.Unix(), Valid: true})
	if err != nil {
		return nil, err
	}
	jobs := make([]CronJob, len(dbJobs))
	for i, j := range dbJobs {
		jobs[i] = fromDBItem(j)
	}
	return jobs, nil
}

// MarkFiring sets the firing flag on a cron job.
func (s *service) MarkFiring(ctx context.Context, id string, firing bool) error {
	return s.q.SetCronJobFiring(ctx, db.SetCronJobFiringParams{
		Firing: firing,
		ID:     id,
	})
}

// RescheduleAndClear advances next_run_at and clears the firing flag in a
// single transaction. Used when a job's permission was denied or its session
// is inactive — the row needs to fall out of the due set so it does not
// re-fire on the next tick.
func (s *service) RescheduleAndClear(ctx context.Context, id string, next time.Time) error {
	if err := s.q.UpdateCronJobNextRun(ctx, db.UpdateCronJobNextRunParams{
		NextRunAt: sql.NullInt64{Int64: next.Unix(), Valid: true},
		ID:        id,
	}); err != nil {
		return err
	}
	return s.q.SetCronJobFiring(ctx, db.SetCronJobFiringParams{
		Firing: false,
		ID:     id,
	})
}

// MarkDone marks a one-shot job as done and clears the firing flag.
func (s *service) MarkDone(ctx context.Context, id string) error {
	if err := s.q.UpdateCronJobStatus(ctx, db.UpdateCronJobStatusParams{
		Status: StatusDone,
		ID:     id,
	}); err != nil {
		return err
	}
	return s.q.SetCronJobFiring(ctx, db.SetCronJobFiringParams{
		Firing: false,
		ID:     id,
	})
}

// ClaimForFiring atomically marks the row firing only if it is still due
// (status=active, firing=false, next_run_at<=now). Returns true on success
// and false if the row was already claimed/advanced by another worker.
func (s *service) ClaimForFiring(ctx context.Context, id string, now time.Time) (bool, error) {
	rows, err := s.q.ClaimCronJobForFiring(ctx, db.ClaimCronJobForFiringParams{
		ID:        id,
		NextRunAt: sql.NullInt64{Int64: now.Unix(), Valid: true},
	})
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// UpdateAfterRun updates the cron job after a successful execution.
func (s *service) UpdateAfterRun(ctx context.Context, job CronJob, result string) (CronJob, error) {
	now := time.Now()

	status := StatusActive
	var nextRunAt sql.NullInt64

	if !job.IsRecurring {
		status = StatusDone
	} else {
		schedule, err := ParseSchedule(job.Schedule)
		if err != nil {
			return CronJob{}, err
		}
		next := schedule.Next(now)
		nextRunAt = sql.NullInt64{Int64: next.Unix(), Valid: true}
	}

	// Truncate result
	if len(result) > 10000 {
		result = result[:10000] + "\n[truncated]"
	}

	dbJob, err := s.q.UpdateCronJobAfterRun(ctx, db.UpdateCronJobAfterRunParams{
		LastRunAt:  sql.NullInt64{Int64: now.Unix(), Valid: true},
		LastResult: sql.NullString{String: result, Valid: result != ""},
		NextRunAt:  nextRunAt,
		Status:     status,
		ID:         job.ID,
	})
	if err != nil {
		return CronJob{}, err
	}

	updated := fromDBItem(dbJob)
	s.Publish(pubsub.UpdatedEvent, updated)
	return updated, nil
}

// UpdateError records an error on a cron job, clears the firing flag, and
// advances next_run_at so the row falls out of the due set. Without advancing
// next_run_at a deterministic failure (bad subagent, persistent permission
// denial, etc.) would re-fire every scheduler tick. For recurring jobs the
// next run is rescheduled forward from now; one-shots are marked done.
func (s *service) UpdateError(ctx context.Context, job CronJob, errMsg string) error {
	if err := s.q.UpdateCronJobError(ctx, db.UpdateCronJobErrorParams{
		Error: sql.NullString{String: errMsg, Valid: errMsg != ""},
		ID:    job.ID,
	}); err != nil {
		return err
	}

	if !job.IsRecurring {
		return s.q.UpdateCronJobStatus(ctx, db.UpdateCronJobStatusParams{
			Status: StatusDone,
			ID:     job.ID,
		})
	}

	schedule, err := ParseSchedule(job.Schedule)
	if err != nil {
		// Schedule is unparseable — pause so the user can investigate
		// rather than letting the row sit due forever.
		return s.q.UpdateCronJobStatus(ctx, db.UpdateCronJobStatusParams{
			Status: StatusPaused,
			ID:     job.ID,
		})
	}
	next := schedule.Next(time.Now())
	return s.q.UpdateCronJobNextRun(ctx, db.UpdateCronJobNextRunParams{
		NextRunAt: sql.NullInt64{Int64: next.Unix(), Valid: true},
		ID:        job.ID,
	})
}

// ParseSchedule parses a 5-field cron expression.
func ParseSchedule(expr string) (cronparser.Schedule, error) {
	return cronparser.NewParser(
		cronparser.Minute | cronparser.Hour | cronparser.Dom | cronparser.Month | cronparser.Dow,
	).Parse(expr)
}

// ComputeNextFire returns the next fire time after 'from'.
func ComputeNextFire(schedule string, from time.Time) (time.Time, error) {
	s, err := ParseSchedule(schedule)
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(from), nil
}

// CronToHuman converts a cron expression to a human-readable string.
func CronToHuman(expr string) string {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return expr
	}
	min, hour, dom, month, dow := parts[0], parts[1], parts[2], parts[3], parts[4]

	// Every N minutes (also matches the bare "*" form for every minute)
	if (min == "*" || strings.HasPrefix(min, "*/")) && hour == "*" && dom == "*" && month == "*" && dow == "*" {
		n := "1"
		if strings.HasPrefix(min, "*/") {
			n = min[2:]
		}
		if n == "1" {
			return "every minute"
		}
		return "every " + n + " minutes"
	}

	// Every N hours at specific minute
	if !strings.Contains(min, "*") && strings.HasPrefix(hour, "*/") && dom == "*" && month == "*" && dow == "*" {
		n := hour[2:]
		if n == "1" {
			return "every hour at :" + padMinute(min)
		}
		return "every " + n + " hours"
	}

	// Every hour at specific minute
	if !strings.Contains(min, "*") && hour == "*" && dom == "*" && month == "*" && dow == "*" {
		return "every hour at :" + padMinute(min)
	}

	// Daily at specific time
	if !strings.Contains(min, "*") && !strings.Contains(hour, "*") && dom == "*" && month == "*" && dow == "*" {
		return "daily at " + formatTime(hour, min)
	}

	// Weekdays at specific time
	if !strings.Contains(min, "*") && !strings.Contains(hour, "*") && dom == "*" && month == "*" && dow == "1-5" {
		return "weekdays at " + formatTime(hour, min)
	}

	// Specific day of week
	if !strings.Contains(min, "*") && !strings.Contains(hour, "*") && dom == "*" && month == "*" && !strings.Contains(dow, "*") {
		dayName := dowToName(dow)
		if dayName != "" {
			return dayName + " at " + formatTime(hour, min)
		}
	}

	return expr
}

// DurationToCron converts a Go duration to the nearest cron expression.
func DurationToCron(d time.Duration) string {
	minutes := int(math.Ceil(d.Minutes()))
	if minutes < 1 {
		minutes = 1
	}

	switch {
	case minutes == 1:
		return "* * * * *"
	case minutes < 60:
		return fmt.Sprintf("*/%d * * * *", minutes)
	case minutes == 60:
		return "0 * * * *"
	case minutes < 1440:
		hours := minutes / 60
		return fmt.Sprintf("0 */%d * * *", hours)
	default:
		return "0 0 * * *" // daily
	}
}

func fromDBItem(item db.CronJob) CronJob {
	return CronJob{
		ID:           item.ID,
		SessionID:    item.SessionID,
		Schedule:     item.Schedule,
		Prompt:       item.Prompt,
		SubagentType: item.SubagentType,
		TaskTitle:    item.TaskTitle,
		TaskID:       item.TaskID,
		IsRecurring:  item.IsRecurring,
		Source:       item.Source,
		Status:       item.Status,
		Firing:       item.Firing,
		LastRunAt:    item.LastRunAt.Int64,
		NextRunAt:    item.NextRunAt.Int64,
		RunCount:     item.RunCount,
		LastResult:   item.LastResult.String,
		Error:        item.Error.String,
		CreatedAt:    item.CreatedAt,
		UpdatedAt:    item.UpdatedAt,
	}
}

func generateID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("cron_%d", time.Now().UnixNano())
	}
	return "cron_" + hex.EncodeToString(b)
}

func generateTaskID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("task_%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func padMinute(m string) string {
	if len(m) == 1 {
		return "0" + m
	}
	return m
}

func formatTime(hour, minute string) string {
	h := 0
	fmt.Sscanf(hour, "%d", &h)
	suffix := "am"
	displayH := h
	if h >= 12 {
		suffix = "pm"
		if h > 12 {
			displayH = h - 12
		}
	}
	if h == 0 {
		displayH = 12
	}
	return fmt.Sprintf("%d:%s%s", displayH, padMinute(minute), suffix)
}

func dowToName(dow string) string {
	switch dow {
	case "0":
		return "Sunday"
	case "1":
		return "Monday"
	case "2":
		return "Tuesday"
	case "3":
		return "Wednesday"
	case "4":
		return "Thursday"
	case "5":
		return "Friday"
	case "6":
		return "Saturday"
	case "0,6":
		return "weekends"
	case "1-5":
		return "weekdays"
	default:
		return ""
	}
}

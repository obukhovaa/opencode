package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	CronCreateToolName = "croncreate"
	CronDeleteToolName = "crondelete"
	CronListToolName   = "cronlist"
)

// CronJobInfo is a read-only view of a cron job, used to decouple the tool from the cron package.
type CronJobInfo struct {
	ID           string
	Schedule     string
	Prompt       string
	SubagentType string
	TaskTitle    string
	TaskID       string
	IsRecurring  bool
	Source       string
	Status       string
	RunCount     int64
	NextRunAt    int64
	LastRunAt    int64
	Error        string
}

// CronCreateInput holds the parameters for creating a cron job.
type CronCreateInput struct {
	SessionID    string
	Schedule     string
	Prompt       string
	SubagentType string
	TaskTitle    string
	IsRecurring  bool
	Source       string
}

// CronToolService is the interface the cron tools require. Implemented by cron.Service.
type CronToolService interface {
	Create(ctx context.Context, params CronCreateInput) (CronJobInfo, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, sessionID string) ([]CronJobInfo, error)
}

// CronScheduleHelper provides human-readable schedule descriptions.
type CronScheduleHelper interface {
	CronToHuman(expr string) string
}

// --- croncreate ---

type cronCreateTool struct {
	cronService CronToolService
	schedHelper CronScheduleHelper
}

type CronCreateParams struct {
	Schedule     string `json:"schedule"`
	Prompt       string `json:"prompt"`
	SubagentType string `json:"subagent_type"`
	TaskTitle    string `json:"task_title"`
	IsRecurring  *bool  `json:"is_recurring,omitempty"`
}

func NewCronCreateTool(cronSvc CronToolService, schedHelper CronScheduleHelper) BaseTool {
	return &cronCreateTool{cronService: cronSvc, schedHelper: schedHelper}
}

func (t *cronCreateTool) Info() ToolInfo {
	return ToolInfo{
		Name: CronCreateToolName,
		Description: `Schedule a prompt to run at a future time via a subagent. The scheduled task runs
in an isolated child session using the task tool. Each run reuses the same task_id,
so the subagent retains full conversation history from prior runs.

Uses standard 5-field cron in the user's local timezone:
minute hour day-of-month month day-of-week.
"0 9 * * *" means 9am local — no timezone conversion needed.

## One-shot tasks (is_recurring: false)

For "remind me at X" or "at <time>, do Y" — fire once then mark as done.
Pin minute/hour/day-of-month/month to specific values:
  "remind me at 2:30pm today" → schedule: "30 14 <today_dom> <today_month> *", is_recurring: false
  "tomorrow morning, run the smoke test" → schedule: "57 8 <tomorrow_dom> <tomorrow_month> *", is_recurring: false

## Recurring jobs (is_recurring: true, the default)

For "every N minutes" / "every hour" / "weekdays at 9am":
  "*/5 * * * *" (every 5 min), "0 * * * *" (hourly), "0 9 * * 1-5" (weekdays at 9am local)

## Avoid the :00 and :30 minute marks when the task allows it

When the user's request is approximate ("every morning", "hourly", "in about an hour"),
pick a minute that is NOT 0 or 30:
  "every morning around 9" → "57 8 * * *" or "3 9 * * *" (not "0 9 * * *")
  "hourly" → "7 * * * *" (not "0 * * * *")

Only use minute 0 or 30 when the user names that exact time and clearly means it.

## Subagent selection

Choose the subagent_type based on what the task needs:
  - "explorer": read-only tasks (monitoring, checking status, reviewing). Default for most scheduled tasks.
  - "workhorse": tasks that need to write files, run commands, or make changes.

Returns a job ID you can pass to crondelete.`,
		Parameters: map[string]any{
			"schedule": map[string]any{
				"type":        "string",
				"description": "Standard 5-field cron expression (minute hour day-of-month month day-of-week)",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "The task prompt for the subagent to execute on each run",
			},
			"subagent_type": map[string]any{
				"type":        "string",
				"description": "The subagent type to use (e.g., 'explorer' for read-only, 'workhorse' for write)",
			},
			"task_title": map[string]any{
				"type":        "string",
				"description": "A short description of the scheduled task (max 80 chars)",
			},
			"is_recurring": map[string]any{
				"type":        "boolean",
				"description": "Whether this is a recurring job (default: true) or a one-shot (false)",
			},
		},
		Required: []string{"schedule", "prompt", "subagent_type", "task_title"},
	}
}

func (t *cronCreateTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params CronCreateParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}
	if params.Schedule == "" {
		return NewTextErrorResponse("schedule is required"), nil
	}
	if params.Prompt == "" {
		return NewTextErrorResponse("prompt is required"), nil
	}
	if params.SubagentType == "" {
		return NewTextErrorResponse("subagent_type is required"), nil
	}
	if params.TaskTitle == "" {
		return NewTextErrorResponse("task_title is required"), nil
	}
	if utf8.RuneCountInString(params.TaskTitle) > 80 {
		params.TaskTitle = truncateRunes(params.TaskTitle, 80)
	}

	sessionID, _ := GetContextValues(ctx)
	if sessionID == "" {
		return NewTextErrorResponse("session context required"), nil
	}

	isRecurring := true
	if params.IsRecurring != nil {
		isRecurring = *params.IsRecurring
	}

	job, err := t.cronService.Create(ctx, CronCreateInput{
		SessionID:    sessionID,
		Schedule:     params.Schedule,
		Prompt:       params.Prompt,
		SubagentType: params.SubagentType,
		TaskTitle:    params.TaskTitle,
		IsRecurring:  isRecurring,
		Source:       "agent",
	})
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	humanSchedule := t.schedHelper.CronToHuman(params.Schedule)
	nextRun := time.Unix(job.NextRunAt, 0).Format("15:04")
	jobType := "recurring"
	if !isRecurring {
		jobType = "one-shot"
	}

	return NewTextResponse(fmt.Sprintf(
		"Scheduled %s cron job %s — %s (%s).\nSubagent: %s\nNext run at %s.\nUse crondelete with id=%q to cancel.",
		jobType, job.ID, humanSchedule, params.Schedule, params.SubagentType, nextRun, job.ID,
	)), nil
}

func (t *cronCreateTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
	return true
}

func (t *cronCreateTool) IsBaseline() bool { return true }

// --- crondelete ---

type cronDeleteTool struct {
	cronService CronToolService
}

type CronDeleteParams struct {
	ID string `json:"id"`
}

func NewCronDeleteTool(cronSvc CronToolService) BaseTool {
	return &cronDeleteTool{cronService: cronSvc}
}

func (t *cronDeleteTool) Info() ToolInfo {
	return ToolInfo{
		Name: CronDeleteToolName,
		Description: `Cancel a cron job previously scheduled with croncreate. Removes it from the
database. The job stops firing immediately. Pass the job ID returned by croncreate.`,
		Parameters: map[string]any{
			"id": map[string]any{
				"type":        "string",
				"description": "The cron job ID returned by croncreate",
			},
		},
		Required: []string{"id"},
	}
}

func (t *cronDeleteTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params CronDeleteParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}
	if params.ID == "" {
		return NewTextErrorResponse("id is required"), nil
	}

	if err := t.cronService.Delete(ctx, params.ID); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("failed to delete cron job: %s", err)), nil
	}

	return NewTextResponse(fmt.Sprintf("Deleted cron job %s.", params.ID)), nil
}

func (t *cronDeleteTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
	return true
}

func (t *cronDeleteTool) IsBaseline() bool { return true }

// --- cronlist ---

type cronListTool struct {
	cronService CronToolService
	schedHelper CronScheduleHelper
}

func NewCronListTool(cronSvc CronToolService, schedHelper CronScheduleHelper) BaseTool {
	return &cronListTool{cronService: cronSvc, schedHelper: schedHelper}
}

func (t *cronListTool) Info() ToolInfo {
	return ToolInfo{
		Name: CronListToolName,
		Description: `List all cron jobs scheduled via croncreate in the current session. Shows each
job's ID, cron schedule, human-readable schedule description, prompt, subagent
type, status (active/done), and run count.`,
		Parameters: map[string]any{},
	}
}

func (t *cronListTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	sessionID, _ := GetContextValues(ctx)
	if sessionID == "" {
		return NewTextErrorResponse("session context required"), nil
	}

	jobs, err := t.cronService.List(ctx, sessionID)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("failed to list cron jobs: %s", err)), nil
	}

	if len(jobs) == 0 {
		return NewTextResponse("No cron jobs scheduled in this session."), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d cron job(s):\n\n", len(jobs)))
	for _, job := range jobs {
		humanSchedule := t.schedHelper.CronToHuman(job.Schedule)
		nextRun := "—"
		if job.NextRunAt > 0 {
			nextRun = time.Unix(job.NextRunAt, 0).Format("15:04")
		}
		lastRun := "never"
		if job.LastRunAt > 0 {
			lastRun = time.Unix(job.LastRunAt, 0).Format("2006-01-02 15:04")
		}
		sb.WriteString(fmt.Sprintf(
			"- **%s** | %s (%s) | %s | runs: %d | next: %s | last: %s\n  prompt: %s\n  subagent: %s\n",
			job.ID, humanSchedule, job.Schedule, job.Status, job.RunCount, nextRun, lastRun,
			truncateStr(job.Prompt, 100), job.SubagentType,
		))
		if job.Error != "" {
			sb.WriteString(fmt.Sprintf("  error: %s\n", job.Error))
		}
	}

	return NewTextResponse(sb.String()), nil
}

func (t *cronListTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
	return true
}

func (t *cronListTool) IsBaseline() bool { return true }

func truncateStr(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	return truncateRunes(s, maxLen) + "..."
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

package cron

import (
	"context"

	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// ToolServiceAdapter adapts cron.Service to tools.CronToolService.
type ToolServiceAdapter struct {
	svc Service
}

func NewToolServiceAdapter(svc Service) *ToolServiceAdapter {
	return &ToolServiceAdapter{svc: svc}
}

func (a *ToolServiceAdapter) Create(ctx context.Context, params tools.CronCreateInput) (tools.CronJobInfo, error) {
	job, err := a.svc.Create(ctx, CreateParams{
		SessionID:    params.SessionID,
		Schedule:     params.Schedule,
		Prompt:       params.Prompt,
		SubagentType: params.SubagentType,
		TaskTitle:    params.TaskTitle,
		IsRecurring:  params.IsRecurring,
		Source:       params.Source,
	})
	if err != nil {
		return tools.CronJobInfo{}, err
	}
	return toToolInfo(job), nil
}

func (a *ToolServiceAdapter) Delete(ctx context.Context, id string) error {
	return a.svc.Delete(ctx, id)
}

func (a *ToolServiceAdapter) List(ctx context.Context, sessionID string) ([]tools.CronJobInfo, error) {
	jobs, err := a.svc.List(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result := make([]tools.CronJobInfo, len(jobs))
	for i, j := range jobs {
		result[i] = toToolInfo(j)
	}
	return result, nil
}

func toToolInfo(j CronJob) tools.CronJobInfo {
	return tools.CronJobInfo{
		ID:           j.ID,
		Schedule:     j.Schedule,
		Prompt:       j.Prompt,
		SubagentType: j.SubagentType,
		TaskTitle:    j.TaskTitle,
		TaskID:       j.TaskID,
		IsRecurring:  j.IsRecurring,
		Source:       j.Source,
		Status:       j.Status,
		RunCount:     j.RunCount,
		NextRunAt:    j.NextRunAt,
		LastRunAt:    j.LastRunAt,
		Error:        j.Error,
	}
}

// ScheduleHelper implements tools.CronScheduleHelper.
type ScheduleHelper struct{}

func NewScheduleHelper() *ScheduleHelper {
	return &ScheduleHelper{}
}

func (h *ScheduleHelper) CronToHuman(expr string) string {
	return CronToHuman(expr)
}

package db

import (
	"context"
	"database/sql"

	mysqldb "github.com/opencode-ai/opencode/internal/db/mysql"
)

// MySQLQuerier wraps the MySQL-generated queries and implements the Querier interface
type MySQLQuerier struct {
	*Queries
	queries *mysqldb.Queries
	db      *sql.DB
}

// NewMySQLQuerier creates a new MySQL querier wrapper
func NewMySQLQuerier(database *sql.DB) *MySQLQuerier {
	return &MySQLQuerier{
		Queries: New(database),
		queries: mysqldb.New(database),
		db:      database,
	}
}

// WithTx creates a new MySQLQuerier with a transaction
func (q *MySQLQuerier) WithTx(tx *sql.Tx) *MySQLQuerier {
	return &MySQLQuerier{
		Queries: q.Queries.WithTx(tx),
		queries: q.queries.WithTx(tx),
		db:      q.db,
	}
}

// CreateSession creates a session and returns it
func (q *MySQLQuerier) CreateSession(ctx context.Context, arg CreateSessionParams) (Session, error) {
	_, err := q.queries.CreateSession(ctx, mysqldb.CreateSessionParams{
		ID:               arg.ID,
		ProjectID:        arg.ProjectID,
		ParentSessionID:  arg.ParentSessionID,
		RootSessionID:    arg.RootSessionID,
		Title:            arg.Title,
		MessageCount:     arg.MessageCount,
		PromptTokens:     arg.PromptTokens,
		CompletionTokens: arg.CompletionTokens,
		Cost:             arg.Cost,
	})
	if err != nil {
		return Session{}, err
	}

	mysqlSession, err := q.queries.GetSessionByID(ctx, arg.ID)
	if err != nil {
		return Session{}, err
	}

	return Session{
		ID:                    mysqlSession.ID,
		ParentSessionID:       mysqlSession.ParentSessionID,
		RootSessionID:         mysqlSession.RootSessionID,
		Title:                 mysqlSession.Title,
		MessageCount:          mysqlSession.MessageCount,
		PromptTokens:          mysqlSession.PromptTokens,
		CompletionTokens:      mysqlSession.CompletionTokens,
		TotalPromptTokens:     mysqlSession.TotalPromptTokens,
		TotalCompletionTokens: mysqlSession.TotalCompletionTokens,
		Cost:                  mysqlSession.Cost,
		UpdatedAt:             mysqlSession.UpdatedAt,
		CreatedAt:             mysqlSession.CreatedAt,
		SummaryMessageID:      mysqlSession.SummaryMessageID,
		ProjectID:             mysqlSession.ProjectID,
	}, nil
}

// GetSessionByID gets a session by ID
func (q *MySQLQuerier) GetSessionByID(ctx context.Context, id string) (Session, error) {
	mysqlSession, err := q.queries.GetSessionByID(ctx, id)
	if err != nil {
		return Session{}, err
	}

	return Session{
		ID:                    mysqlSession.ID,
		ParentSessionID:       mysqlSession.ParentSessionID,
		RootSessionID:         mysqlSession.RootSessionID,
		Title:                 mysqlSession.Title,
		MessageCount:          mysqlSession.MessageCount,
		PromptTokens:          mysqlSession.PromptTokens,
		CompletionTokens:      mysqlSession.CompletionTokens,
		TotalPromptTokens:     mysqlSession.TotalPromptTokens,
		TotalCompletionTokens: mysqlSession.TotalCompletionTokens,
		Cost:                  mysqlSession.Cost,
		UpdatedAt:             mysqlSession.UpdatedAt,
		CreatedAt:             mysqlSession.CreatedAt,
		SummaryMessageID:      mysqlSession.SummaryMessageID,
		ProjectID:             mysqlSession.ProjectID,
	}, nil
}

// ListSessions lists sessions
func (q *MySQLQuerier) ListSessions(ctx context.Context, projectID sql.NullString) ([]Session, error) {
	mysqlSessions, err := q.queries.ListSessions(ctx, projectID)
	if err != nil {
		return nil, err
	}

	sessions := make([]Session, len(mysqlSessions))
	for i, s := range mysqlSessions {
		sessions[i] = Session{
			ID:                    s.ID,
			ParentSessionID:       s.ParentSessionID,
			RootSessionID:         s.RootSessionID,
			Title:                 s.Title,
			MessageCount:          s.MessageCount,
			PromptTokens:          s.PromptTokens,
			CompletionTokens:      s.CompletionTokens,
			TotalPromptTokens:     s.TotalPromptTokens,
			TotalCompletionTokens: s.TotalCompletionTokens,
			Cost:                  s.Cost,
			UpdatedAt:             s.UpdatedAt,
			CreatedAt:             s.CreatedAt,
			SummaryMessageID:      s.SummaryMessageID,
			ProjectID:             s.ProjectID,
		}
	}
	return sessions, nil
}

// ListChildSessions lists child sessions by root session ID
func (q *MySQLQuerier) ListChildSessions(ctx context.Context, rootSessionID sql.NullString) ([]Session, error) {
	mysqlSessions, err := q.queries.ListChildSessions(ctx, rootSessionID)
	if err != nil {
		return nil, err
	}

	sessions := make([]Session, len(mysqlSessions))
	for i, s := range mysqlSessions {
		sessions[i] = Session{
			ID:                    s.ID,
			ParentSessionID:       s.ParentSessionID,
			RootSessionID:         s.RootSessionID,
			Title:                 s.Title,
			MessageCount:          s.MessageCount,
			PromptTokens:          s.PromptTokens,
			CompletionTokens:      s.CompletionTokens,
			TotalPromptTokens:     s.TotalPromptTokens,
			TotalCompletionTokens: s.TotalCompletionTokens,
			Cost:                  s.Cost,
			UpdatedAt:             s.UpdatedAt,
			CreatedAt:             s.CreatedAt,
			SummaryMessageID:      s.SummaryMessageID,
			ProjectID:             s.ProjectID,
		}
	}
	return sessions, nil
}

// UpdateSession updates a session and returns it
func (q *MySQLQuerier) UpdateSession(ctx context.Context, arg UpdateSessionParams) (Session, error) {
	_, err := q.queries.UpdateSession(ctx, mysqldb.UpdateSessionParams{
		Title:                 arg.Title,
		PromptTokens:          arg.PromptTokens,
		CompletionTokens:      arg.CompletionTokens,
		TotalPromptTokens:     arg.TotalPromptTokens,
		TotalCompletionTokens: arg.TotalCompletionTokens,
		SummaryMessageID:      arg.SummaryMessageID,
		Cost:                  arg.Cost,
		ID:                    arg.ID,
	})
	if err != nil {
		return Session{}, err
	}

	return q.GetSessionByID(ctx, arg.ID)
}

// DeleteSession deletes a session
func (q *MySQLQuerier) DeleteSession(ctx context.Context, id string) error {
	return q.queries.DeleteSession(ctx, id)
}

// DeleteSessionTree deletes the session with the given root id and every
// descendant whose root_session_id points to it. Passing the same value for
// both id and root_session_id matches legacy rows where root_session_id is
// NULL via the id branch.
func (q *MySQLQuerier) DeleteSessionTree(ctx context.Context, arg DeleteSessionTreeParams) error {
	return q.queries.DeleteSessionTree(ctx, mysqldb.DeleteSessionTreeParams{
		ID:            arg.ID,
		RootSessionID: arg.RootSessionID,
	})
}

// CreateMessage creates a message and returns it
func (q *MySQLQuerier) CreateMessage(ctx context.Context, arg CreateMessageParams) (Message, error) {
	_, err := q.queries.CreateMessage(ctx, mysqldb.CreateMessageParams{
		ID:        arg.ID,
		SessionID: arg.SessionID,
		Role:      arg.Role,
		Parts:     arg.Parts,
		Model:     arg.Model,
		Seq:       arg.Seq,
	})
	if err != nil {
		return Message{}, err
	}

	mysqlMessage, err := q.queries.GetMessage(ctx, arg.ID)
	if err != nil {
		return Message{}, err
	}

	return mysqlMessageToMessage(mysqlMessage), nil
}

// GetMessage gets a message by ID
func (q *MySQLQuerier) GetMessage(ctx context.Context, id string) (Message, error) {
	mysqlMessage, err := q.queries.GetMessage(ctx, id)
	if err != nil {
		return Message{}, err
	}

	return mysqlMessageToMessage(mysqlMessage), nil
}

// ListMessagesBySession lists messages by session
func (q *MySQLQuerier) ListMessagesBySession(ctx context.Context, sessionID string) ([]Message, error) {
	mysqlMessages, err := q.queries.ListMessagesBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	messages := make([]Message, len(mysqlMessages))
	for i, m := range mysqlMessages {
		messages[i] = mysqlMessageToMessage(m)
	}
	return messages, nil
}

func mysqlMessageToMessage(m mysqldb.Message) Message {
	return Message{
		ID:         m.ID,
		SessionID:  m.SessionID,
		Role:       m.Role,
		Parts:      m.Parts,
		Model:      m.Model,
		Seq:        m.Seq,
		CreatedAt:  m.CreatedAt,
		UpdatedAt:  m.UpdatedAt,
		FinishedAt: m.FinishedAt,
	}
}

// UpdateMessage updates a message
func (q *MySQLQuerier) UpdateMessage(ctx context.Context, arg UpdateMessageParams) error {
	return q.queries.UpdateMessage(ctx, mysqldb.UpdateMessageParams{
		Parts:      arg.Parts,
		FinishedAt: arg.FinishedAt,
		ID:         arg.ID,
	})
}

// DeleteMessage deletes a message
func (q *MySQLQuerier) DeleteMessage(ctx context.Context, id string) error {
	return q.queries.DeleteMessage(ctx, id)
}

// DeleteSessionMessages deletes all messages for a session
func (q *MySQLQuerier) DeleteSessionMessages(ctx context.Context, sessionID string) error {
	return q.queries.DeleteSessionMessages(ctx, sessionID)
}

// GetMaxSeqBySession returns the maximum seq value for a session
func (q *MySQLQuerier) GetMaxSeqBySession(ctx context.Context, sessionID string) (int64, error) {
	return q.queries.GetMaxSeqBySession(ctx, sessionID)
}

// ListLatestMessagesBySession lists the latest N messages for a session
func (q *MySQLQuerier) ListLatestMessagesBySession(ctx context.Context, arg ListLatestMessagesBySessionParams) ([]Message, error) {
	mysqlMessages, err := q.queries.ListLatestMessagesBySession(ctx, mysqldb.ListLatestMessagesBySessionParams{
		SessionID: arg.SessionID,
		Limit:     int32(arg.Limit),
	})
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(mysqlMessages))
	for i, m := range mysqlMessages {
		messages[i] = mysqlMessageToMessage(m)
	}
	return messages, nil
}

// CreateFile creates a file and returns it
func (q *MySQLQuerier) CreateFile(ctx context.Context, arg CreateFileParams) (File, error) {
	_, err := q.queries.CreateFile(ctx, mysqldb.CreateFileParams{
		ID:        arg.ID,
		SessionID: arg.SessionID,
		Path:      arg.Path,
		Content:   arg.Content,
		Version:   arg.Version,
	})
	if err != nil {
		return File{}, err
	}

	mysqlFile, err := q.queries.GetFile(ctx, arg.ID)
	if err != nil {
		return File{}, err
	}

	return File{
		ID:        mysqlFile.ID,
		SessionID: mysqlFile.SessionID,
		Path:      mysqlFile.Path,
		Content:   mysqlFile.Content,
		Version:   mysqlFile.Version,
		CreatedAt: mysqlFile.CreatedAt,
		UpdatedAt: mysqlFile.UpdatedAt,
	}, nil
}

// GetFile gets a file by ID
func (q *MySQLQuerier) GetFile(ctx context.Context, id string) (File, error) {
	mysqlFile, err := q.queries.GetFile(ctx, id)
	if err != nil {
		return File{}, err
	}

	return File{
		ID:        mysqlFile.ID,
		SessionID: mysqlFile.SessionID,
		Path:      mysqlFile.Path,
		Content:   mysqlFile.Content,
		Version:   mysqlFile.Version,
		CreatedAt: mysqlFile.CreatedAt,
		UpdatedAt: mysqlFile.UpdatedAt,
	}, nil
}

// GetFileByPathAndSession gets a file by path and session
func (q *MySQLQuerier) GetFileByPathAndSession(ctx context.Context, arg GetFileByPathAndSessionParams) (File, error) {
	mysqlFile, err := q.queries.GetFileByPathAndSession(ctx, mysqldb.GetFileByPathAndSessionParams{
		Path:      arg.Path,
		SessionID: arg.SessionID,
	})
	if err != nil {
		return File{}, err
	}

	return File{
		ID:        mysqlFile.ID,
		SessionID: mysqlFile.SessionID,
		Path:      mysqlFile.Path,
		Content:   mysqlFile.Content,
		Version:   mysqlFile.Version,
		CreatedAt: mysqlFile.CreatedAt,
		UpdatedAt: mysqlFile.UpdatedAt,
	}, nil
}

// ListFilesBySession lists files by session
func (q *MySQLQuerier) ListFilesBySession(ctx context.Context, sessionID string) ([]File, error) {
	mysqlFiles, err := q.queries.ListFilesBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	files := make([]File, len(mysqlFiles))
	for i, f := range mysqlFiles {
		files[i] = File{
			ID:        f.ID,
			SessionID: f.SessionID,
			Path:      f.Path,
			Content:   f.Content,
			Version:   f.Version,
			CreatedAt: f.CreatedAt,
			UpdatedAt: f.UpdatedAt,
		}
	}
	return files, nil
}

// ListFilesByPath lists files by path
func (q *MySQLQuerier) ListFilesByPath(ctx context.Context, path string) ([]File, error) {
	mysqlFiles, err := q.queries.ListFilesByPath(ctx, path)
	if err != nil {
		return nil, err
	}

	files := make([]File, len(mysqlFiles))
	for i, f := range mysqlFiles {
		files[i] = File{
			ID:        f.ID,
			SessionID: f.SessionID,
			Path:      f.Path,
			Content:   f.Content,
			Version:   f.Version,
			CreatedAt: f.CreatedAt,
			UpdatedAt: f.UpdatedAt,
		}
	}
	return files, nil
}

// ListFilesBySessionTree lists files by session tree
func (q *MySQLQuerier) ListFilesBySessionTree(ctx context.Context, rootSessionID sql.NullString) ([]File, error) {
	mysqlFiles, err := q.queries.ListFilesBySessionTree(ctx, rootSessionID)
	if err != nil {
		return nil, err
	}

	files := make([]File, len(mysqlFiles))
	for i, f := range mysqlFiles {
		files[i] = File{
			ID:        f.ID,
			SessionID: f.SessionID,
			Path:      f.Path,
			Content:   f.Content,
			Version:   f.Version,
			CreatedAt: f.CreatedAt,
			UpdatedAt: f.UpdatedAt,
		}
	}
	return files, nil
}

// UpdateFile updates a file and returns it
func (q *MySQLQuerier) UpdateFile(ctx context.Context, arg UpdateFileParams) (File, error) {
	_, err := q.queries.UpdateFile(ctx, mysqldb.UpdateFileParams{
		Content: arg.Content,
		Version: arg.Version,
		ID:      arg.ID,
	})
	if err != nil {
		return File{}, err
	}

	return q.GetFile(ctx, arg.ID)
}

// DeleteFile deletes a file
func (q *MySQLQuerier) DeleteFile(ctx context.Context, id string) error {
	return q.queries.DeleteFile(ctx, id)
}

// DeleteSessionFiles deletes all files for a session
func (q *MySQLQuerier) DeleteSessionFiles(ctx context.Context, sessionID string) error {
	return q.queries.DeleteSessionFiles(ctx, sessionID)
}

// ListLatestSessionFiles lists the latest files for a session
func (q *MySQLQuerier) ListLatestSessionFiles(ctx context.Context, sessionID string) ([]File, error) {
	mysqlFiles, err := q.queries.ListLatestSessionFiles(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	files := make([]File, len(mysqlFiles))
	for i, f := range mysqlFiles {
		files[i] = File{
			ID:        f.ID,
			SessionID: f.SessionID,
			Path:      f.Path,
			Content:   f.Content,
			Version:   f.Version,
			CreatedAt: f.CreatedAt,
			UpdatedAt: f.UpdatedAt,
		}
	}
	return files, nil
}

// ListLatestSessionTreeFiles lists the latest files for a session tree
func (q *MySQLQuerier) ListLatestSessionTreeFiles(ctx context.Context, rootSessionID sql.NullString) ([]File, error) {
	mysqlFiles, err := q.queries.ListLatestSessionTreeFiles(ctx, rootSessionID)
	if err != nil {
		return nil, err
	}

	files := make([]File, len(mysqlFiles))
	for i, f := range mysqlFiles {
		files[i] = File{
			ID:        f.ID,
			SessionID: f.SessionID,
			Path:      f.Path,
			Content:   f.Content,
			Version:   f.Version,
			CreatedAt: f.CreatedAt,
			UpdatedAt: f.UpdatedAt,
		}
	}
	return files, nil
}

// CreateFlowState creates a flow state and returns it
func (q *MySQLQuerier) CreateFlowState(ctx context.Context, arg CreateFlowStateParams) (FlowState, error) {
	_, err := q.queries.CreateFlowState(ctx, mysqldb.CreateFlowStateParams{
		SessionID:      arg.SessionID,
		RootSessionID:  arg.RootSessionID,
		FlowID:         arg.FlowID,
		StepID:         arg.StepID,
		Status:         arg.Status,
		Args:           arg.Args,
		Output:         arg.Output,
		IsStructOutput: arg.IsStructOutput,
		Iteration:      int32(arg.Iteration),
	})
	if err != nil {
		return FlowState{}, err
	}
	return q.GetFlowState(ctx, arg.SessionID)
}

// GetFlowState gets a flow state by session ID
func (q *MySQLQuerier) GetFlowState(ctx context.Context, sessionID string) (FlowState, error) {
	fs, err := q.queries.GetFlowState(ctx, sessionID)
	if err != nil {
		return FlowState{}, err
	}
	return FlowState{
		SessionID:      fs.SessionID,
		RootSessionID:  fs.RootSessionID,
		FlowID:         fs.FlowID,
		StepID:         fs.StepID,
		Status:         fs.Status,
		Args:           fs.Args,
		Output:         fs.Output,
		IsStructOutput: fs.IsStructOutput,
		Iteration:      int64(fs.Iteration),
		CreatedAt:      fs.CreatedAt,
		UpdatedAt:      fs.UpdatedAt,
	}, nil
}

// ListFlowStatesByRootSession lists flow states by root session ID
func (q *MySQLQuerier) ListFlowStatesByRootSession(ctx context.Context, rootSessionID string) ([]FlowState, error) {
	mysqlStates, err := q.queries.ListFlowStatesByRootSession(ctx, rootSessionID)
	if err != nil {
		return nil, err
	}

	states := make([]FlowState, len(mysqlStates))
	for i, fs := range mysqlStates {
		states[i] = FlowState{
			SessionID:      fs.SessionID,
			RootSessionID:  fs.RootSessionID,
			FlowID:         fs.FlowID,
			StepID:         fs.StepID,
			Status:         fs.Status,
			Args:           fs.Args,
			Output:         fs.Output,
			IsStructOutput: fs.IsStructOutput,
			Iteration:      int64(fs.Iteration),
			CreatedAt:      fs.CreatedAt,
			UpdatedAt:      fs.UpdatedAt,
		}
	}
	return states, nil
}

// ListFlowStatesByFlowID lists flow states by flow ID
func (q *MySQLQuerier) ListFlowStatesByFlowID(ctx context.Context, flowID string) ([]FlowState, error) {
	mysqlStates, err := q.queries.ListFlowStatesByFlowID(ctx, flowID)
	if err != nil {
		return nil, err
	}

	states := make([]FlowState, len(mysqlStates))
	for i, fs := range mysqlStates {
		states[i] = FlowState{
			SessionID:      fs.SessionID,
			RootSessionID:  fs.RootSessionID,
			FlowID:         fs.FlowID,
			StepID:         fs.StepID,
			Status:         fs.Status,
			Args:           fs.Args,
			Output:         fs.Output,
			IsStructOutput: fs.IsStructOutput,
			Iteration:      int64(fs.Iteration),
			CreatedAt:      fs.CreatedAt,
			UpdatedAt:      fs.UpdatedAt,
		}
	}
	return states, nil
}

// UpdateFlowState updates a flow state and returns it
func (q *MySQLQuerier) UpdateFlowState(ctx context.Context, arg UpdateFlowStateParams) (FlowState, error) {
	_, err := q.queries.UpdateFlowState(ctx, mysqldb.UpdateFlowStateParams{
		Status:         arg.Status,
		Args:           arg.Args,
		Output:         arg.Output,
		IsStructOutput: arg.IsStructOutput,
		Iteration:      int32(arg.Iteration),
		SessionID:      arg.SessionID,
	})
	if err != nil {
		return FlowState{}, err
	}
	return q.GetFlowState(ctx, arg.SessionID)
}

// DeleteFlowStatesByRootSession deletes all flow states for a root session
func (q *MySQLQuerier) DeleteFlowStatesByRootSession(ctx context.Context, rootSessionID string) error {
	return q.queries.DeleteFlowStatesByRootSession(ctx, rootSessionID)
}

// CreateCronJob creates a cron job and returns it
func (q *MySQLQuerier) CreateCronJob(ctx context.Context, arg CreateCronJobParams) (CronJob, error) {
	_, err := q.queries.CreateCronJob(ctx, mysqldb.CreateCronJobParams{
		ID:           arg.ID,
		SessionID:    arg.SessionID,
		Schedule:     arg.Schedule,
		Prompt:       arg.Prompt,
		SubagentType: arg.SubagentType,
		TaskTitle:    arg.TaskTitle,
		TaskID:       arg.TaskID,
		IsRecurring:  arg.IsRecurring,
		Source:       arg.Source,
		Status:       arg.Status,
		NextRunAt:    arg.NextRunAt,
	})
	if err != nil {
		return CronJob{}, err
	}
	return q.GetCronJob(ctx, arg.ID)
}

// GetCronJob gets a cron job by ID
func (q *MySQLQuerier) GetCronJob(ctx context.Context, id string) (CronJob, error) {
	j, err := q.queries.GetCronJob(ctx, id)
	if err != nil {
		return CronJob{}, err
	}
	return mysqlCronJobToCronJob(j), nil
}

// ListCronJobsBySession lists cron jobs by session
func (q *MySQLQuerier) ListCronJobsBySession(ctx context.Context, sessionID string) ([]CronJob, error) {
	mysqlJobs, err := q.queries.ListCronJobsBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	jobs := make([]CronJob, len(mysqlJobs))
	for i, j := range mysqlJobs {
		jobs[i] = mysqlCronJobToCronJob(j)
	}
	return jobs, nil
}

// ListActiveCronJobs lists all active cron jobs
func (q *MySQLQuerier) ListActiveCronJobs(ctx context.Context) ([]CronJob, error) {
	mysqlJobs, err := q.queries.ListActiveCronJobs(ctx)
	if err != nil {
		return nil, err
	}
	jobs := make([]CronJob, len(mysqlJobs))
	for i, j := range mysqlJobs {
		jobs[i] = mysqlCronJobToCronJob(j)
	}
	return jobs, nil
}

// ListDueCronJobs lists cron jobs that are due for execution
func (q *MySQLQuerier) ListDueCronJobs(ctx context.Context, nextRunAt sql.NullInt64) ([]CronJob, error) {
	mysqlJobs, err := q.queries.ListDueCronJobs(ctx, nextRunAt)
	if err != nil {
		return nil, err
	}
	jobs := make([]CronJob, len(mysqlJobs))
	for i, j := range mysqlJobs {
		jobs[i] = mysqlCronJobToCronJob(j)
	}
	return jobs, nil
}

// ListMissedOneShots lists one-shot cron jobs that missed their fire time
func (q *MySQLQuerier) ListMissedOneShots(ctx context.Context, nextRunAt sql.NullInt64) ([]CronJob, error) {
	mysqlJobs, err := q.queries.ListMissedOneShots(ctx, nextRunAt)
	if err != nil {
		return nil, err
	}
	jobs := make([]CronJob, len(mysqlJobs))
	for i, j := range mysqlJobs {
		jobs[i] = mysqlCronJobToCronJob(j)
	}
	return jobs, nil
}

// CountActiveCronJobsBySession counts active cron jobs for a session
func (q *MySQLQuerier) CountActiveCronJobsBySession(ctx context.Context, sessionID string) (int64, error) {
	return q.queries.CountActiveCronJobsBySession(ctx, sessionID)
}

// SetCronJobFiring sets the firing flag on a cron job
func (q *MySQLQuerier) SetCronJobFiring(ctx context.Context, arg SetCronJobFiringParams) error {
	return q.queries.SetCronJobFiring(ctx, mysqldb.SetCronJobFiringParams{
		Firing: arg.Firing,
		ID:     arg.ID,
	})
}

// ClaimCronJobForFiring atomically marks a cron job as firing only if it is still due.
func (q *MySQLQuerier) ClaimCronJobForFiring(ctx context.Context, arg ClaimCronJobForFiringParams) (int64, error) {
	return q.queries.ClaimCronJobForFiring(ctx, mysqldb.ClaimCronJobForFiringParams{
		ID:        arg.ID,
		NextRunAt: arg.NextRunAt,
	})
}

// ClearStaleFiring resets firing flag for all stale rows
func (q *MySQLQuerier) ClearStaleFiring(ctx context.Context) error {
	return q.queries.ClearStaleFiring(ctx)
}

// UpdateCronJobAfterRun updates a cron job after execution
func (q *MySQLQuerier) UpdateCronJobAfterRun(ctx context.Context, arg UpdateCronJobAfterRunParams) (CronJob, error) {
	_, err := q.queries.UpdateCronJobAfterRun(ctx, mysqldb.UpdateCronJobAfterRunParams{
		LastRunAt:  arg.LastRunAt,
		LastResult: arg.LastResult,
		NextRunAt:  arg.NextRunAt,
		Status:     arg.Status,
		ID:         arg.ID,
	})
	if err != nil {
		return CronJob{}, err
	}
	return q.GetCronJob(ctx, arg.ID)
}

// UpdateCronJobNextRun updates the next run time for a cron job
func (q *MySQLQuerier) UpdateCronJobNextRun(ctx context.Context, arg UpdateCronJobNextRunParams) error {
	return q.queries.UpdateCronJobNextRun(ctx, mysqldb.UpdateCronJobNextRunParams{
		NextRunAt: arg.NextRunAt,
		ID:        arg.ID,
	})
}

// UpdateCronJobStatus updates the status of a cron job
func (q *MySQLQuerier) UpdateCronJobStatus(ctx context.Context, arg UpdateCronJobStatusParams) error {
	return q.queries.UpdateCronJobStatus(ctx, mysqldb.UpdateCronJobStatusParams{
		Status: arg.Status,
		ID:     arg.ID,
	})
}

// UpdateCronJobError updates the error field of a cron job
func (q *MySQLQuerier) UpdateCronJobError(ctx context.Context, arg UpdateCronJobErrorParams) error {
	return q.queries.UpdateCronJobError(ctx, mysqldb.UpdateCronJobErrorParams{
		Error: arg.Error,
		ID:    arg.ID,
	})
}

// DeleteCronJob deletes a cron job
func (q *MySQLQuerier) DeleteCronJob(ctx context.Context, id string) error {
	return q.queries.DeleteCronJob(ctx, id)
}

func mysqlCronJobToCronJob(j mysqldb.CronJob) CronJob {
	return CronJob{
		ID:           j.ID,
		SessionID:    j.SessionID,
		Schedule:     j.Schedule,
		Prompt:       j.Prompt,
		SubagentType: j.SubagentType,
		TaskTitle:    j.TaskTitle,
		TaskID:       j.TaskID,
		IsRecurring:  j.IsRecurring,
		Source:       j.Source,
		Status:       j.Status,
		Firing:       j.Firing,
		LastRunAt:    j.LastRunAt,
		NextRunAt:    j.NextRunAt,
		RunCount:     j.RunCount,
		LastResult:   j.LastResult,
		Error:        j.Error,
		CreatedAt:    j.CreatedAt,
		UpdatedAt:    j.UpdatedAt,
	}
}

// GetRecapBySessionID gets a recap by session ID
func (q *MySQLQuerier) GetRecapBySessionID(ctx context.Context, sessionID string) (SessionRecap, error) {
	r, err := q.queries.GetRecapBySessionID(ctx, sessionID)
	if err != nil {
		return SessionRecap{}, err
	}
	return SessionRecap{
		ID:           r.ID,
		SessionID:    r.SessionID,
		Content:      r.Content,
		MessageCount: r.MessageCount,
		CreatedAt:    r.CreatedAt,
	}, nil
}

// UpsertRecap creates or updates a recap and returns it
func (q *MySQLQuerier) UpsertRecap(ctx context.Context, arg UpsertRecapParams) (SessionRecap, error) {
	_, err := q.queries.UpsertRecap(ctx, mysqldb.UpsertRecapParams{
		ID:           arg.ID,
		SessionID:    arg.SessionID,
		Content:      arg.Content,
		MessageCount: arg.MessageCount,
	})
	if err != nil {
		return SessionRecap{}, err
	}
	return q.GetRecapBySessionID(ctx, arg.SessionID)
}

// DeleteRecapBySessionID deletes a recap by session ID
func (q *MySQLQuerier) DeleteRecapBySessionID(ctx context.Context, sessionID string) error {
	return q.queries.DeleteRecapBySessionID(ctx, sessionID)
}

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
		ID:               mysqlSession.ID,
		ParentSessionID:  mysqlSession.ParentSessionID,
		RootSessionID:    mysqlSession.RootSessionID,
		Title:            mysqlSession.Title,
		MessageCount:     mysqlSession.MessageCount,
		PromptTokens:     mysqlSession.PromptTokens,
		CompletionTokens: mysqlSession.CompletionTokens,
		Cost:             mysqlSession.Cost,
		UpdatedAt:        mysqlSession.UpdatedAt,
		CreatedAt:        mysqlSession.CreatedAt,
		SummaryMessageID: mysqlSession.SummaryMessageID,
		ProjectID:        mysqlSession.ProjectID,
	}, nil
}

// GetSessionByID gets a session by ID
func (q *MySQLQuerier) GetSessionByID(ctx context.Context, id string) (Session, error) {
	mysqlSession, err := q.queries.GetSessionByID(ctx, id)
	if err != nil {
		return Session{}, err
	}

	return Session{
		ID:               mysqlSession.ID,
		ParentSessionID:  mysqlSession.ParentSessionID,
		RootSessionID:    mysqlSession.RootSessionID,
		Title:            mysqlSession.Title,
		MessageCount:     mysqlSession.MessageCount,
		PromptTokens:     mysqlSession.PromptTokens,
		CompletionTokens: mysqlSession.CompletionTokens,
		Cost:             mysqlSession.Cost,
		UpdatedAt:        mysqlSession.UpdatedAt,
		CreatedAt:        mysqlSession.CreatedAt,
		SummaryMessageID: mysqlSession.SummaryMessageID,
		ProjectID:        mysqlSession.ProjectID,
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
			ID:               s.ID,
			ParentSessionID:  s.ParentSessionID,
			RootSessionID:    s.RootSessionID,
			Title:            s.Title,
			MessageCount:     s.MessageCount,
			PromptTokens:     s.PromptTokens,
			CompletionTokens: s.CompletionTokens,
			Cost:             s.Cost,
			UpdatedAt:        s.UpdatedAt,
			CreatedAt:        s.CreatedAt,
			SummaryMessageID: s.SummaryMessageID,
			ProjectID:        s.ProjectID,
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
			ID:               s.ID,
			ParentSessionID:  s.ParentSessionID,
			RootSessionID:    s.RootSessionID,
			Title:            s.Title,
			MessageCount:     s.MessageCount,
			PromptTokens:     s.PromptTokens,
			CompletionTokens: s.CompletionTokens,
			Cost:             s.Cost,
			UpdatedAt:        s.UpdatedAt,
			CreatedAt:        s.CreatedAt,
			SummaryMessageID: s.SummaryMessageID,
			ProjectID:        s.ProjectID,
		}
	}
	return sessions, nil
}

// UpdateSession updates a session and returns it
func (q *MySQLQuerier) UpdateSession(ctx context.Context, arg UpdateSessionParams) (Session, error) {
	_, err := q.queries.UpdateSession(ctx, mysqldb.UpdateSessionParams{
		Title:            arg.Title,
		PromptTokens:     arg.PromptTokens,
		CompletionTokens: arg.CompletionTokens,
		SummaryMessageID: arg.SummaryMessageID,
		Cost:             arg.Cost,
		ID:               arg.ID,
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

// CreateMessage creates a message and returns it
func (q *MySQLQuerier) CreateMessage(ctx context.Context, arg CreateMessageParams) (Message, error) {
	_, err := q.queries.CreateMessage(ctx, mysqldb.CreateMessageParams{
		ID:        arg.ID,
		SessionID: arg.SessionID,
		Role:      arg.Role,
		Parts:     arg.Parts,
		Model:     arg.Model,
	})
	if err != nil {
		return Message{}, err
	}

	mysqlMessage, err := q.queries.GetMessage(ctx, arg.ID)
	if err != nil {
		return Message{}, err
	}

	return Message{
		ID:         mysqlMessage.ID,
		SessionID:  mysqlMessage.SessionID,
		Role:       mysqlMessage.Role,
		Parts:      mysqlMessage.Parts,
		Model:      mysqlMessage.Model,
		CreatedAt:  mysqlMessage.CreatedAt,
		UpdatedAt:  mysqlMessage.UpdatedAt,
		FinishedAt: mysqlMessage.FinishedAt,
	}, nil
}

// GetMessage gets a message by ID
func (q *MySQLQuerier) GetMessage(ctx context.Context, id string) (Message, error) {
	mysqlMessage, err := q.queries.GetMessage(ctx, id)
	if err != nil {
		return Message{}, err
	}

	return Message{
		ID:         mysqlMessage.ID,
		SessionID:  mysqlMessage.SessionID,
		Role:       mysqlMessage.Role,
		Parts:      mysqlMessage.Parts,
		Model:      mysqlMessage.Model,
		CreatedAt:  mysqlMessage.CreatedAt,
		UpdatedAt:  mysqlMessage.UpdatedAt,
		FinishedAt: mysqlMessage.FinishedAt,
	}, nil
}

// ListMessagesBySession lists messages by session
func (q *MySQLQuerier) ListMessagesBySession(ctx context.Context, sessionID string) ([]Message, error) {
	mysqlMessages, err := q.queries.ListMessagesBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	messages := make([]Message, len(mysqlMessages))
	for i, m := range mysqlMessages {
		messages[i] = Message{
			ID:         m.ID,
			SessionID:  m.SessionID,
			Role:       m.Role,
			Parts:      m.Parts,
			Model:      m.Model,
			CreatedAt:  m.CreatedAt,
			UpdatedAt:  m.UpdatedAt,
			FinishedAt: m.FinishedAt,
		}
	}
	return messages, nil
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
		Output:         arg.Output,
		IsStructOutput: arg.IsStructOutput,
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

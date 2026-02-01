package db

import (
	"context"
	"database/sql"

	mysqldb "github.com/opencode-ai/opencode/internal/db/mysql"
)

// MySQLQuerier wraps the MySQL-generated queries and implements the Querier interface
type MySQLQuerier struct {
	*Queries // Embed to get WithTx and other methods
	queries  *mysqldb.Queries
	db       *sql.DB
}

// NewMySQLQuerier creates a new MySQL querier wrapper
func NewMySQLQuerier(database *sql.DB) *MySQLQuerier {
	return &MySQLQuerier{
		Queries: New(database), // Create embedded Queries for WithTx support
		queries: mysqldb.New(database),
		db:      database,
	}
}

// CreateSession creates a session and returns it
func (q *MySQLQuerier) CreateSession(ctx context.Context, arg CreateSessionParams) (Session, error) {
	_, err := q.queries.CreateSession(ctx, mysqldb.CreateSessionParams{
		ID:               arg.ID,
		ProjectID:        arg.ProjectID,
		ParentSessionID:  arg.ParentSessionID,
		Title:            arg.Title,
		MessageCount:     arg.MessageCount,
		PromptTokens:     arg.PromptTokens,
		CompletionTokens: arg.CompletionTokens,
		Cost:             arg.Cost,
	})
	if err != nil {
		return Session{}, err
	}

	// Fetch the created session
	mysqlSession, err := q.queries.GetSessionByID(ctx, arg.ID)
	if err != nil {
		return Session{}, err
	}

	return Session{
		ID:               mysqlSession.ID,
		ParentSessionID:  mysqlSession.ParentSessionID,
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

	// Fetch the updated session
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

	// Fetch the created message
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

	// Fetch the created file
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

	// Fetch the updated file
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

// ListNewFiles lists new files
func (q *MySQLQuerier) ListNewFiles(ctx context.Context) ([]File, error) {
	mysqlFiles, err := q.queries.ListNewFiles(ctx)
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

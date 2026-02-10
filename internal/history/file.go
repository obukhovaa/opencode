package history

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

const (
	InitialVersion = "initial"
)

type File struct {
	ID        string
	SessionID string
	Path      string
	Content   string
	Version   string
	CreatedAt int64
	UpdatedAt int64
}

type Service interface {
	pubsub.Suscriber[File]
	Create(ctx context.Context, sessionID, path, content string) (File, error)
	CreateVersion(ctx context.Context, sessionID, path, content string) (File, error)
	Get(ctx context.Context, id string) (File, error)
	GetByPathAndSession(ctx context.Context, path, sessionID string) (File, error)
	ListBySession(ctx context.Context, sessionID string) ([]File, error)
	ListLatestSessionFiles(ctx context.Context, sessionID string) ([]File, error)
	ListBySessionTree(ctx context.Context, rootSessionID string) ([]File, error)
	ListLatestSessionTreeFiles(ctx context.Context, rootSessionID string) ([]File, error)
	Update(ctx context.Context, file File) (File, error)
	Delete(ctx context.Context, id string) error
	DeleteSessionFiles(ctx context.Context, sessionID string) error
}

type service struct {
	*pubsub.Broker[File]
	db *sql.DB
	q  *db.Queries
}

func NewService(q *db.Queries, database *sql.DB) Service {
	return &service{
		Broker: pubsub.NewBroker[File](),
		q:      q,
		db:     database,
	}
}

func (s *service) Create(ctx context.Context, sessionID, path, content string) (File, error) {
	return s.createWithVersion(ctx, sessionID, path, content, InitialVersion)
}

func (s *service) CreateVersion(ctx context.Context, sessionID, path, content string) (File, error) {
	files, err := s.q.ListFilesByPath(ctx, path)
	if err != nil {
		return File{}, err
	}

	if len(files) == 0 {
		return s.Create(ctx, sessionID, path, content)
	}

	latestFile := files[0]
	latestVersion := latestFile.Version

	var nextVersion string
	if latestVersion == InitialVersion {
		nextVersion = "v1"
	} else if strings.HasPrefix(latestVersion, "v") {
		versionNum, err := strconv.Atoi(latestVersion[1:])
		if err != nil {
			nextVersion = fmt.Sprintf("v%d", latestFile.CreatedAt)
		} else {
			nextVersion = fmt.Sprintf("v%d", versionNum+1)
		}
	} else {
		nextVersion = fmt.Sprintf("v%d", latestFile.CreatedAt)
	}

	return s.createWithVersion(ctx, sessionID, path, content, nextVersion)
}

func (s *service) createWithVersion(ctx context.Context, sessionID, path, content, version string) (File, error) {
	const maxRetries = 3
	var file File
	var err error

	for attempt := range maxRetries {
		tx, txErr := s.db.Begin()
		if txErr != nil {
			return File{}, fmt.Errorf("failed to begin transaction: %w", txErr)
		}

		qtx := s.q.WithTx(tx)

		dbFile, txErr := qtx.CreateFile(ctx, db.CreateFileParams{
			ID:        uuid.New().String(),
			SessionID: sessionID,
			Path:      path,
			Content:   content,
			Version:   version,
		})
		if txErr != nil {
			tx.Rollback()

			if strings.Contains(txErr.Error(), "UNIQUE constraint failed") {
				if attempt < maxRetries-1 {
					if strings.HasPrefix(version, "v") {
						versionNum, parseErr := strconv.Atoi(version[1:])
						if parseErr == nil {
							version = fmt.Sprintf("v%d", versionNum+1)
							continue
						}
					}
					version = fmt.Sprintf("v%d", time.Now().Unix())
					continue
				}
			}
			return File{}, txErr
		}

		if txErr = tx.Commit(); txErr != nil {
			return File{}, fmt.Errorf("failed to commit transaction: %w", txErr)
		}

		file = s.fromDBItem(dbFile)
		s.Publish(pubsub.CreatedEvent, file)
		return file, nil
	}

	return file, err
}

func (s *service) Get(ctx context.Context, id string) (File, error) {
	dbFile, err := s.q.GetFile(ctx, id)
	if err != nil {
		return File{}, err
	}
	return s.fromDBItem(dbFile), nil
}

func (s *service) GetByPathAndSession(ctx context.Context, path, sessionID string) (File, error) {
	dbFile, err := s.q.GetFileByPathAndSession(ctx, db.GetFileByPathAndSessionParams{
		Path:      path,
		SessionID: sessionID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			logging.Debug("File not found in db", "path", path, "sessionID", sessionID)
		} else {
			logging.Error("Failed to select file from db", "path", path, "sessionID", sessionID, "cause", err.Error())
		}
		return File{}, err
	}
	logging.Debug("File selected from db", "path", path, "sessionID", sessionID, "fileID", dbFile.ID, "fileVersion", dbFile.Version)
	return s.fromDBItem(dbFile), nil
}

func (s *service) ListBySession(ctx context.Context, sessionID string) ([]File, error) {
	dbFiles, err := s.q.ListFilesBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	files := make([]File, len(dbFiles))
	for i, dbFile := range dbFiles {
		files[i] = s.fromDBItem(dbFile)
	}
	return files, nil
}

func (s *service) ListLatestSessionFiles(ctx context.Context, sessionID string) ([]File, error) {
	dbFiles, err := s.q.ListLatestSessionFiles(ctx, sessionID)
	if err != nil {
		logging.Error("Failed to select latest session files from db", "sessionID", sessionID, "cause", err.Error())
		return nil, err
	}
	files := make([]File, len(dbFiles))
	for i, dbFile := range dbFiles {
		logging.Debug("File selected from db (latest version)", "path", dbFile.Path, "sessionID", sessionID, "fileID", dbFile.ID, "fileVersion", dbFile.Version)
		files[i] = s.fromDBItem(dbFile)
	}
	return files, nil
}

func (s *service) ListBySessionTree(ctx context.Context, rootSessionID string) ([]File, error) {
	dbFiles, err := s.q.ListFilesBySessionTree(ctx, sql.NullString{String: rootSessionID, Valid: true})
	if err != nil {
		logging.Error("Failed to select all root session files from db", "rootSessionID", rootSessionID, "cause", err.Error())
		return nil, err
	}
	files := make([]File, len(dbFiles))
	for i, dbFile := range dbFiles {
		logging.Debug("File selected from db (all by root)", "path", dbFile.Path, "rootSessionID", rootSessionID, "fileID", dbFile.ID, "fileVersion", dbFile.Version)
		files[i] = s.fromDBItem(dbFile)
	}
	return files, nil
}

func (s *service) ListLatestSessionTreeFiles(ctx context.Context, rootSessionID string) ([]File, error) {
	dbFiles, err := s.q.ListLatestSessionTreeFiles(ctx, sql.NullString{String: rootSessionID, Valid: true})
	if err != nil {
		logging.Error("Failed to select root session latest version files from db", "rootSessionID", rootSessionID, "cause", err.Error())
		return nil, err
	}
	files := make([]File, len(dbFiles))
	for i, dbFile := range dbFiles {
		logging.Debug("File selected from db (latest version by root)", "path", dbFile.Path, "rootSessionID", rootSessionID, "fileID", dbFile.ID, "fileVersion", dbFile.Version)
		files[i] = s.fromDBItem(dbFile)
	}
	return files, nil
}

func (s *service) Update(ctx context.Context, file File) (File, error) {
	dbFile, err := s.q.UpdateFile(ctx, db.UpdateFileParams{
		ID:      file.ID,
		Content: file.Content,
		Version: file.Version,
	})
	if err != nil {
		return File{}, err
	}
	updatedFile := s.fromDBItem(dbFile)
	s.Publish(pubsub.UpdatedEvent, updatedFile)
	return updatedFile, nil
}

func (s *service) Delete(ctx context.Context, id string) error {
	file, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	err = s.q.DeleteFile(ctx, id)
	if err != nil {
		return err
	}
	s.Publish(pubsub.DeletedEvent, file)
	return nil
}

func (s *service) DeleteSessionFiles(ctx context.Context, sessionID string) error {
	files, err := s.ListBySession(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, file := range files {
		err = s.Delete(ctx, file.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *service) fromDBItem(item db.File) File {
	return File{
		ID:        item.ID,
		SessionID: item.SessionID,
		Path:      item.Path,
		Content:   item.Content,
		Version:   item.Version,
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
	}
}

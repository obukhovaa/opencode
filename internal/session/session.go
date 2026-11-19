package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// ErrEmptyTitle is returned by Rename when the requested title is empty or
// only whitespace.
var ErrEmptyTitle = errors.New("session title cannot be empty")

type Session struct {
	// FlowID and DynamicFlow are display annotations populated from flow state.
	// They are not persisted on the session row.
	FlowID                string
	DynamicFlow           bool
	ID                    string
	ProjectID             string
	ParentSessionID       string
	RootSessionID         string
	Title                 string
	MessageCount          int64
	PromptTokens          int64 // Last model pass's full input, including cache reads/writes.
	CompletionTokens      int64 // Last model pass's generated output, excluding cached input.
	TotalPromptTokens     int64 // Cumulative input across calls and server-side passes, including cache.
	TotalCompletionTokens int64 // Cumulative generated output across calls and server-side passes.
	SummaryMessageID      string
	Cost                  float64
	CreatedAt             int64
	UpdatedAt             int64
	// UserSetTitle is true once a user has explicitly renamed the session.
	// While set, automatic title generation must not overwrite Title.
	UserSetTitle bool
	// AgentName is the primary agent the session last ran under; empty when
	// none was recorded, in which case the process-wide active agent applies.
	AgentName string
}

type Service interface {
	pubsub.Suscriber[Session]
	Create(ctx context.Context, title string) (Session, error)
	CreateWithID(ctx context.Context, id, title string) (Session, error)
	CreateFlowSession(ctx context.Context, id, rootSessionID, title string) (Session, error)
	CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error)
	CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error)
	Get(ctx context.Context, id string) (Session, error)
	List(ctx context.Context) ([]Session, error)
	Page(ctx context.Context, options PageOptions) (Page, error)
	Walk(ctx context.Context, options PageOptions, visit func(Session) error) error
	CountOldSessions(ctx context.Context, activeSessionID string) (int64, error)
	VisibleSessionIDs(ctx context.Context, ids []string, currentID string) ([]string, error)
	ListChildren(ctx context.Context, rootSessionID string) ([]Session, error)
	Save(ctx context.Context, session Session) (Session, error)
	// Rename sets the user-facing title and durably marks the session as
	// user-titled so automatic title generation will not overwrite it.
	Rename(ctx context.Context, id, title string) (Session, error)
	// SetGeneratedTitle applies an automatically generated title, but only if
	// the session has not been user-renamed. It is a no-op on user-titled
	// sessions.
	SetGeneratedTitle(ctx context.Context, id, title string) (Session, error)
	// SetAgentName persists the primary agent the session runs under.
	SetAgentName(ctx context.Context, sessionID, name string) error
	// SetAgentNameIf is SetAgentName guarded by the stored agent: it writes
	// only while the session's agent still equals expected ("" for a session
	// never stamped) and reports whether it did.
	SetAgentNameIf(ctx context.Context, sessionID, expected, name string) (bool, error)
	Delete(ctx context.Context, id string) error
	DeleteTree(ctx context.Context, id string) error
	ListOldSessions(ctx context.Context, activeSessionID string) ([]Session, error)
	CleanupOldSessions(ctx context.Context, activeSessionID string) (int, error)
}

// DeleteRequest describes the existing deletion semantics. Tree requests target
// RootSessionID; a single child deletion targets SessionID only.
type DeleteRequest struct {
	SessionID     string
	RootSessionID string
	Tree          bool
}

// DeleteHandler coordinates additional durable state with session removal. It
// must invoke mutate exactly once using the supplied transactional querier (or
// refuse without mutation). Install during App initialization, before any runs.
type DeleteHandler func(context.Context, DeleteRequest, func(db.Querier) error) error

func SetDeleteHandler(svc Service, handler DeleteHandler) bool {
	s, ok := svc.(*service)
	if !ok {
		return false
	}
	s.deleteHandler = handler
	return true
}

type service struct {
	*pubsub.Broker[Session]
	q             db.Querier
	projectID     string
	deleteHandler DeleteHandler
}

func (s *service) Create(ctx context.Context, title string) (Session, error) {
	id := uuid.New().String()
	return s.createWithID(ctx, id, title)
}

func (s *service) CreateWithID(ctx context.Context, id, title string) (Session, error) {
	return s.createWithID(ctx, id, title)
}

func (s *service) CreateFlowSession(ctx context.Context, id, rootSessionID, title string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:            id,
		ProjectID:     sql.NullString{String: s.projectID, Valid: true},
		RootSessionID: sql.NullString{String: rootSessionID, Valid: true},
		Title:         title,
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) createWithID(ctx context.Context, id, title string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:            id,
		ProjectID:     sql.NullString{String: s.projectID, Valid: true},
		RootSessionID: sql.NullString{String: id, Valid: true},
		Title:         title,
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error) {
	parent, err := s.q.GetSessionByID(ctx, parentSessionID)
	if err != nil {
		return Session{}, fmt.Errorf("failed to get parent session: %w", err)
	}

	rootSessionID := parent.RootSessionID.String
	if !parent.RootSessionID.Valid || rootSessionID == "" {
		rootSessionID = parentSessionID
	}

	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              toolCallID,
		ProjectID:       sql.NullString{String: s.projectID, Valid: true},
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true},
		RootSessionID:   sql.NullString{String: rootSessionID, Valid: true},
		Title:           title,
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error) {
	parent, err := s.q.GetSessionByID(ctx, parentSessionID)
	if err != nil {
		return Session{}, fmt.Errorf("failed to get parent session: %w", err)
	}

	rootSessionID := parent.RootSessionID.String
	if !parent.RootSessionID.Valid || rootSessionID == "" {
		rootSessionID = parentSessionID
	}

	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              "title-" + parentSessionID,
		ProjectID:       sql.NullString{String: s.projectID, Valid: true},
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true},
		RootSessionID:   sql.NullString{String: rootSessionID, Valid: true},
		Title:           "Generate a title",
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) Delete(ctx context.Context, id string) error {
	session, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	// Only cascade to the entire tree when deleting the root session itself.
	// Deleting an intermediate node deletes only that single row so callers
	// that target a sub-session do not silently wipe siblings.
	if session.RootSessionID == "" || session.RootSessionID == session.ID {
		return s.deleteTree(ctx, session)
	}
	mutate := func(q db.Querier) error { return q.DeleteSession(ctx, session.ID) }
	if s.deleteHandler != nil {
		err = s.deleteHandler(ctx, DeleteRequest{SessionID: session.ID, RootSessionID: session.RootSessionID}, mutate)
	} else {
		err = mutate(s.q)
	}
	if err != nil {
		return err
	}
	s.Publish(pubsub.DeletedEvent, session)
	return nil
}

// DeleteTree deletes a session and every descendant sharing its root_session_id.
// It is safe to call with any session ID in the tree; the entire tree rooted at
// session.RootSessionID is removed.
func (s *service) DeleteTree(ctx context.Context, id string) error {
	session, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	return s.deleteTree(ctx, session)
}

func (s *service) deleteTree(ctx context.Context, session Session) error {
	rootID := session.RootSessionID
	if rootID == "" {
		rootID = session.ID
	}
	// Match both the root row itself (for legacy sessions where
	// root_session_id is NULL) and any descendants pointing at it.
	mutate := func(q db.Querier) error {
		return q.DeleteSessionTree(ctx, db.DeleteSessionTreeParams{ID: rootID, RootSessionID: sql.NullString{String: rootID, Valid: true}})
	}
	var err error
	if s.deleteHandler != nil {
		err = s.deleteHandler(ctx, DeleteRequest{SessionID: session.ID, RootSessionID: rootID, Tree: true}, mutate)
	} else {
		err = mutate(s.q)
	}
	if err != nil {
		return fmt.Errorf("failed to delete session tree: %w", err)
	}
	s.Publish(pubsub.DeletedEvent, session)
	return nil
}

func (s *service) Get(ctx context.Context, id string) (Session, error) {
	dbSession, err := s.q.GetSessionByID(ctx, id)
	if err != nil {
		return Session{}, err
	}
	return s.fromDBItem(dbSession), nil
}

func (s *service) Save(ctx context.Context, session Session) (Session, error) {
	dbSession, err := s.q.UpdateSession(ctx, db.UpdateSessionParams{
		ID:                    session.ID,
		Title:                 session.Title,
		PromptTokens:          session.PromptTokens,
		CompletionTokens:      session.CompletionTokens,
		TotalPromptTokens:     session.TotalPromptTokens,
		TotalCompletionTokens: session.TotalCompletionTokens,
		SummaryMessageID: sql.NullString{
			String: session.SummaryMessageID,
			Valid:  session.SummaryMessageID != "",
		},
		Cost: session.Cost,
	})
	if err != nil {
		return Session{}, err
	}
	session = s.fromDBItem(dbSession)
	s.Publish(pubsub.UpdatedEvent, session)
	return session, nil
}

func (s *service) Rename(ctx context.Context, id, title string) (Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Session{}, ErrEmptyTitle
	}
	dbSession, err := s.q.RenameSession(ctx, db.RenameSessionParams{
		ID:    id,
		Title: title,
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.UpdatedEvent, session)
	return session, nil
}

func (s *service) SetGeneratedTitle(ctx context.Context, id, title string) (Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return s.Get(ctx, id)
	}
	rows, err := s.q.SetGeneratedTitle(ctx, db.SetGeneratedTitleParams{
		ID:    id,
		Title: title,
	})
	if err != nil {
		return Session{}, err
	}
	// rows == 0 means the session was renamed by a user (user_set_title = TRUE)
	// so the guarded update matched nothing — leave the user's title intact and
	// do not broadcast a spurious change.
	if rows == 0 {
		return s.Get(ctx, id)
	}
	session, err := s.Get(ctx, id)
	if err != nil {
		return Session{}, err
	}
	s.Publish(pubsub.UpdatedEvent, session)
	return session, nil
}

func (s *service) SetAgentName(ctx context.Context, sessionID, name string) error {
	if err := s.q.SetSessionAgentName(ctx, db.SetSessionAgentNameParams{
		ID:        sessionID,
		AgentName: name,
	}); err != nil {
		return err
	}
	session, err := s.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	s.Publish(pubsub.UpdatedEvent, session)
	return nil
}

func (s *service) SetAgentNameIf(ctx context.Context, sessionID, expected, name string) (bool, error) {
	rows, err := s.q.SetSessionAgentNameIf(ctx, db.SetSessionAgentNameIfParams{
		ID:                sessionID,
		AgentName:         name,
		ExpectedAgentName: expected,
	})
	if err != nil {
		return false, err
	}
	if rows == 0 {
		return false, nil
	}
	session, err := s.Get(ctx, sessionID)
	if err != nil {
		return true, err
	}
	s.Publish(pubsub.UpdatedEvent, session)
	return true, nil
}

// List is a bounded legacy snapshot. Interactive callers use Page; full scans use Walk.
func (s *service) List(ctx context.Context) ([]Session, error) {
	page, err := s.Page(ctx, PageOptions{Limit: MaxPageSize})
	return page.Sessions, err
}

func (s *service) ListChildren(ctx context.Context, rootSessionID string) ([]Session, error) {
	dbSessions, err := s.q.ListChildSessions(ctx, sql.NullString{String: rootSessionID, Valid: true})
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, len(dbSessions))
	for i, dbSession := range dbSessions {
		sessions[i] = s.fromDBItem(dbSession)
	}
	return sessions, nil
}

func (s service) fromDBItem(item db.Session) Session {
	return Session{
		ID:                    item.ID,
		ProjectID:             item.ProjectID.String,
		ParentSessionID:       item.ParentSessionID.String,
		RootSessionID:         item.RootSessionID.String,
		Title:                 item.Title,
		MessageCount:          item.MessageCount,
		PromptTokens:          item.PromptTokens,
		CompletionTokens:      item.CompletionTokens,
		TotalPromptTokens:     item.TotalPromptTokens,
		TotalCompletionTokens: item.TotalCompletionTokens,
		SummaryMessageID:      item.SummaryMessageID.String,
		Cost:                  item.Cost,
		CreatedAt:             item.CreatedAt,
		UpdatedAt:             item.UpdatedAt,
		UserSetTitle:          item.UserSetTitle,
		AgentName:             item.AgentName,
	}
}

// oldSessionScope resolves just the active tree rather than loading every session.
func (s *service) oldSessionScope(ctx context.Context, activeID string) (string, int64, error) {
	root := activeID
	if activeID != "" {
		active, err := s.Get(ctx, activeID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", 0, err
		}
		if err == nil && active.RootSessionID != "" {
			root = active.RootSessionID
		}
	}
	if root != "" {
		run, err := s.q.GetConversationFlowRun(ctx, db.GetConversationFlowRunParams{ProjectID: s.projectID, ID: root})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", 0, err
		}
		if err == nil {
			root = run.ConversationID
		}
	}
	maxAge := config.DefaultSessionCleanupMaxAge
	if cfg := config.Get(); cfg != nil {
		maxAge = cfg.SessionCleanupMaxAge()
	}
	return root, time.Now().Add(-maxAge).Unix(), nil
}
func (s *service) CountOldSessions(ctx context.Context, activeID string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()
	root, cutoff, err := s.oldSessionScope(ctx, activeID)
	if err != nil {
		return 0, err
	}
	return s.q.CountOldSessionTrees(ctx, db.CountOldSessionTreesParams{ProjectID: sql.NullString{String: s.projectID, Valid: true}, Cutoff: cutoff, ActiveRoot: sql.NullString{String: root, Valid: true}})
}

// ListOldSessions returns a bounded preview; cleanup walks candidate pages.
func (s *service) ListOldSessions(ctx context.Context, activeID string) ([]Session, error) {
	ctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()
	root, cutoff, err := s.oldSessionScope(ctx, activeID)
	if err != nil {
		return nil, err
	}
	rows, err := s.oldSessionPage(ctx, root, cutoff, "")
	if err != nil {
		return nil, err
	}
	result := make([]Session, 0, len(rows))
	for _, r := range rows {
		row, err := s.Get(ctx, r.SessionID)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, nil
}
func (s *service) oldSessionPage(ctx context.Context, root string, cutoff int64, after string) ([]db.ListOldSessionTreePageRow, error) {
	ctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()
	return s.q.ListOldSessionTreePage(ctx, db.ListOldSessionTreePageParams{ProjectID: sql.NullString{String: s.projectID, Valid: true}, Cutoff: cutoff, ActiveRoot: sql.NullString{String: root, Valid: true}, AfterRoot: sql.NullString{String: after, Valid: true}, PageLimit: MaxPageSize})
}
func (s *service) CleanupOldSessions(ctx context.Context, activeID string) (int, error) {
	scopeCtx, scopeCancel := context.WithTimeout(ctx, pageTimeout)
	root, cutoff, err := s.oldSessionScope(scopeCtx, activeID)
	scopeCancel()
	if err != nil {
		return 0, err
	}
	deleted, after := 0, ""
	for {
		if err = ctx.Err(); err != nil {
			return deleted, err
		}
		rows, err := s.oldSessionPage(ctx, root, cutoff, after)
		if err != nil {
			return deleted, err
		}
		if len(rows) == 0 {
			return deleted, nil
		}
		for _, row := range rows {
			if err = ctx.Err(); err != nil {
				return deleted, err
			}
			if err = s.DeleteTree(ctx, row.SessionID); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return deleted, fmt.Errorf("failed to delete session %s: %w", row.SessionID, err)
			}
			deleted++
			after = row.TreeID
		}
	}
}

func NewService(q db.Querier, explicitProjectID string) Service {
	var projectID string
	if explicitProjectID != "" {
		projectID = explicitProjectID
	} else {
		cfg := config.Get()
		projectID = db.GetProjectID(cfg.WorkingDir)
	}
	broker := pubsub.NewBroker[Session]()
	return &service{Broker: broker, q: q, projectID: projectID}
}

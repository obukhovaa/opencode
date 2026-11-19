package session

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/opencode-ai/opencode/internal/db"
)

const DefaultPageSize = 50
const MaxPageSize = 200
const pageTimeout = 5 * time.Second

var ErrInvalidCursor = errors.New("invalid session cursor")

type PageOptions struct {
	Limit           int
	Cursor          string
	Search          string
	IDPrefix        string
	RootsOnly       bool
	TreeRoot        string
	OnlyID          string
	ExcludeID       string
	CurrentID       string
	HideEmpty       bool
	ExcludeExternal bool
	WithFlow        bool
}
type Page struct {
	Sessions   []Session
	NextCursor string
}
type pageCursor struct {
	Version int    `json:"v"`
	Created int64  `json:"t"`
	ID      string `json:"id"`
	Scope   string `json:"s"`
}

func (s *service) pageScope(o PageOptions) string {
	// A cursor carries a position, never authorization or caller-supplied SQL.
	b, _ := json.Marshal([]any{s.projectID, o.Search, o.IDPrefix, o.RootsOnly, o.TreeRoot, o.OnlyID, o.ExcludeID, o.CurrentID, o.HideEmpty, o.ExcludeExternal})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}
func (s *service) Page(ctx context.Context, o PageOptions) (Page, error) {
	if o.Limit < 0 || len(o.Search) > 1024 || utf8.RuneCountInString(o.IDPrefix) > 255 {
		return Page{}, fmt.Errorf("invalid session page options")
	}
	if o.Limit == 0 {
		o.Limit = DefaultPageSize
	}
	if o.Limit > MaxPageSize {
		o.Limit = MaxPageSize
	}
	o.Search = strings.TrimSpace(o.Search)
	scope := s.pageScope(o)
	cursor := pageCursor{}
	if o.Cursor != "" {
		if len(o.Cursor) > 2048 {
			return Page{}, ErrInvalidCursor
		}
		b, err := base64.RawURLEncoding.DecodeString(o.Cursor)
		if err != nil {
			return Page{}, ErrInvalidCursor
		}
		decoder := json.NewDecoder(strings.NewReader(string(b)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || cursor.Version != 1 || cursor.Created < 0 || cursor.ID == "" || utf8.RuneCountInString(cursor.ID) > 255 || !utf8.ValidString(cursor.ID) || cursor.Scope != scope {
			return Page{}, ErrInvalidCursor
		}
	}
	ctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()
	rows, err := s.q.ListSessionPage(ctx, db.ListSessionPageParams{
		ProjectID:       sql.NullString{String: s.projectID, Valid: true},
		WithFlow:        flag(o.WithFlow),
		OnlyID:          o.OnlyID,
		ExcludeID:       o.ExcludeID,
		TreeRoot:        o.TreeRoot,
		RootsOnly:       flag(o.RootsOnly),
		CurrentID:       o.CurrentID,
		Search:          o.Search,
		IDPrefix:        o.IDPrefix,
		HideEmpty:       flag(o.HideEmpty),
		ExcludeExternal: flag(o.ExcludeExternal),
		HasCursor:       flag(o.Cursor != ""),
		BeforeCreated:   cursor.Created,
		BeforeID:        cursor.ID,
		PageLimit:       int64(o.Limit + 1),
	})
	if err != nil {
		return Page{}, err
	}
	page := Page{Sessions: make([]Session, 0, min(len(rows), o.Limit))}
	if len(rows) > o.Limit {
		last := rows[o.Limit-1]
		b, _ := json.Marshal(pageCursor{Version: 1, Created: last.CreatedAt, ID: last.ID, Scope: scope})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(b)
		rows = rows[:o.Limit]
	}
	for _, r := range rows {
		page.Sessions = append(page.Sessions, s.fromPageRow(r))
	}
	return page, nil
}
func flag(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

// Walk streams bounded pages for the few operations that genuinely visit every
// session. It must not be used to populate an interactive list.
func (s *service) Walk(ctx context.Context, o PageOptions, visit func(Session) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := s.Page(ctx, o)
		if err != nil {
			return err
		}
		for _, row := range page.Sessions {
			if err = visit(row); err != nil {
				return err
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		o.Cursor = page.NextCursor
	}
}

// VisibleSessionIDs checks the external-session boundary for a bounded set of
// known IDs; cron visibility never needs a complete session listing.
func (s *service) VisibleSessionIDs(ctx context.Context, ids []string, currentID string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()
	visible := make([]string, 0, len(ids))
	for start := 0; start < len(ids); start += MaxPageSize {
		rows, err := s.q.ListVisibleSessionIDs(ctx, db.ListVisibleSessionIDsParams{ProjectID: sql.NullString{String: s.projectID, Valid: true}, Ids: ids[start:min(len(ids), start+MaxPageSize)], CurrentID: currentID})
		if err != nil {
			return nil, err
		}
		visible = append(visible, rows...)
	}
	return visible, nil
}

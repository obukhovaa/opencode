package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"

	"github.com/opencode-ai/opencode/internal/db"
)

func pagingSQLite(t *testing.T) (*sql.DB, db.Querier) {
	t.Helper()
	provider := db.NewSQLiteProvider(t.TempDir())
	conn, err := provider.Connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	goose.SetBaseFS(db.FS)
	if err = goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err = goose.Up(conn, "migrations/sqlite"); err != nil {
		t.Fatal(err)
	}
	return conn, db.New(conn)
}
func seedPagingSession(t *testing.T, conn *sql.DB, id, project, root string, parent any, created, updated int64) {
	t.Helper()
	_, err := conn.Exec(`INSERT INTO sessions(id,project_id,parent_session_id,root_session_id,title,created_at,updated_at)VALUES(?,?,?,?,?,?,?)`, id, project, parent, root, "Title "+id, created, updated)
	if err != nil {
		t.Fatal(err)
	}
}
func runPagingSuite(t *testing.T, conn *sql.DB, q db.Querier) {
	t.Helper()
	ctx := context.Background()
	project := "paging-" + uuid.NewString()
	svc := NewService(q, project)
	id := func(name string) string { return project + "-" + name }
	// Equal timestamps, including epoch zero, force the ID tie-breaker and do
	// not permit a timestamp-zero sentinel to repeat a page indefinitely.
	for i := 0; i < 9; i++ {
		seedPagingSession(t, conn, id(fmt.Sprintf("root-%02d", i)), project, id(fmt.Sprintf("root-%02d", i)), nil, int64(i/3), 10)
	}
	seedPagingSession(t, conn, id("foreign"), project+"-foreign", id("foreign"), nil, 999, 999)
	seedPagingSession(t, conn, id("step"), project, id("run"), nil, 999, 999)
	seedPagingSession(t, conn, id("task"), project, id("run"), id("step"), 999, 999)
	seen := map[string]bool{}
	options := PageOptions{RootsOnly: true, Limit: 2}
	for round := 0; ; round++ {
		if round > 10 {
			t.Fatal("cursor did not advance")
		}
		page, err := svc.Page(ctx, options)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Sessions) > 2 {
			t.Fatal("SQL page bound lost")
		}
		for _, row := range page.Sessions {
			if seen[row.ID] || !strings.Contains(row.ID, "root-") {
				t.Fatalf("duplicate or non-root row: %+v", row)
			}
			seen[row.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		options.Cursor = page.NextCursor
	}
	if len(seen) != 9 {
		t.Fatalf("paged roots=%d want9", len(seen))
	}
	first, err := svc.Page(ctx, PageOptions{RootsOnly: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Page(ctx, PageOptions{Cursor: first.NextCursor, Search: "changed"}); err != ErrInvalidCursor {
		t.Fatalf("cursor filter mismatch accepted: %v", err)
	}
	if _, err = svc.Page(ctx, PageOptions{Cursor: "not-json"}); err != ErrInvalidCursor {
		t.Fatalf("invalid cursor accepted: %v", err)
	}
	// Deleting the cursor row must not skip or repeat later rows (OFFSET would).
	if err = svc.Delete(ctx, first.Sessions[1].ID); err != nil {
		t.Fatal(err)
	}
	second, err := svc.Page(ctx, PageOptions{RootsOnly: true, Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(second.Sessions) != 2 {
		t.Fatalf("deleted cursor row broke paging: %+v %v", second, err)
	}
	search, err := svc.Page(ctx, PageOptions{Search: "root-00", RootsOnly: true})
	if err != nil || len(search.Sessions) != 1 {
		t.Fatalf("search=%+v %v", search, err)
	}
	foreign, err := svc.Page(ctx, PageOptions{OnlyID: id("foreign"), CurrentID: id("foreign")})
	if err != nil || len(foreign.Sessions) != 0 {
		t.Fatalf("project scope escaped: %+v %v", foreign, err)
	}
	// Visibility is applied BEFORE limit; hidden newest rows cannot consume a
	// page and hide visible older sessions. Binding a child hides its siblings.
	for i := 0; i < 12; i++ {
		name := id(fmt.Sprintf("private-%02d", i))
		seedPagingSession(t, conn, name, project, name, nil, 2000+int64(i), 2000)
		_, err = conn.Exec(`INSERT INTO bridge_sessions(project_id,channel,identity_id,peer_id,session_id,created_at,updated_at) VALUES(?,'external','c3',?,?,1,1)`, project, name, name)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = conn.Exec(`INSERT INTO bridge_sessions(project_id,channel,identity_id,peer_id,session_id,created_at,updated_at) VALUES(?,'external','c3','child',?,1,1)`, project, id("task"))
	if err != nil {
		t.Fatal(err)
	}
	visible, err := svc.Page(ctx, PageOptions{Limit: 4, ExcludeExternal: true})
	if err != nil || len(visible.Sessions) != 4 {
		t.Fatalf("visibility after limit: %+v %v", visible, err)
	}
	for _, row := range visible.Sessions {
		if strings.Contains(row.ID, "private") || row.ID == id("step") {
			t.Fatal("private session leaked", row.ID)
		}
	}
	own, err := svc.Page(ctx, PageOptions{Limit: 4, OnlyID: id("private-00"), CurrentID: id("private-00"), ExcludeExternal: true})
	if err != nil || len(own.Sessions) != 1 {
		t.Fatalf("current exception lost: %+v %v", own, err)
	}
	ids, err := svc.VisibleSessionIDs(ctx, []string{id("step"), id("task"), id("root-00"), id("foreign")}, "")
	if err != nil || len(ids) != 1 || ids[0] != id("root-00") {
		t.Fatalf("batch visibility=%v %v", ids, err)
	}
	// A badge fetch reads only headers, and never creates/imports a run.
	snapshot := `{"manifest":{"Definition":{"ID":"review"}}}`
	_, err = conn.Exec(`INSERT INTO conversation_flow_runs(id,project_id,conversation_id,invocation_id,invocation_hash,kind,status,snapshot,origin,created_at,updated_at) VALUES(?,?,?,'badge','hash','generated','completed',?,'{}',3,3)`, id("badge-run"), project, id("root-00"), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	badge, err := svc.Page(ctx, PageOptions{OnlyID: id("root-00"), WithFlow: true})
	if err != nil || len(badge.Sessions) != 1 || badge.Sessions[0].FlowID != "review" || !badge.Sessions[0].DynamicFlow {
		t.Fatalf("badge=%+v %v", badge, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = svc.Page(cancelled, PageOptions{}); err == nil {
		t.Fatal("cancelled page query succeeded")
	}
}
func TestSessionPagingSQLite(t *testing.T) { conn, q := pagingSQLite(t); runPagingSuite(t, conn, q) }

func TestSessionCleanupWalksBatchesAndProtectsActiveConversation(t *testing.T) {
	conn, q := pagingSQLite(t)
	runCleanupPagingSuite(t, conn, q)
}
func runCleanupPagingSuite(t *testing.T, conn *sql.DB, q db.Querier) {
	ctx := context.Background()
	project := "cleanup-" + uuid.NewString()
	svc := NewService(q, project)
	id := func(name string) string { return project + "-" + name }
	old := int64(1)
	for i := 0; i < MaxPageSize+13; i++ {
		name := id(fmt.Sprintf("old-%04d", i))
		seedPagingSession(t, conn, name, project, name, nil, old, old)
	}
	seedPagingSession(t, conn, id("active"), project, id("active"), nil, old, old)
	seedPagingSession(t, conn, id("active-step"), project, id("active-run"), nil, old, old)
	_, err := conn.Exec(`INSERT INTO conversation_flow_runs(id,project_id,conversation_id,invocation_id,invocation_hash,kind,status,snapshot,origin,created_at,updated_at) VALUES(?,?,?,'run','hash','generated','running','{}','{}',1,1)`, id("active-run"), project, id("active"))
	if err != nil {
		t.Fatal(err)
	}
	seedPagingSession(t, conn, id("fresh"), project, id("fresh"), nil, time.Now().Unix(), time.Now().Unix())
	count, err := svc.CountOldSessions(ctx, id("active-step"))
	if err != nil || count != MaxPageSize+13 {
		t.Fatalf("count=%d %v", count, err)
	}
	hooks := 0
	SetDeleteHandler(svc, func(ctx context.Context, req DeleteRequest, mutate func(db.Querier) error) error {
		hooks++
		if !req.Tree {
			t.Fatal("cleanup bypassed tree handler")
		}
		return mutate(q)
	})
	deleted, err := svc.CleanupOldSessions(ctx, id("active-step"))
	if err != nil || deleted != MaxPageSize+13 || hooks != deleted {
		t.Fatalf("deleted=%d hooks=%d error=%v", deleted, hooks, err)
	}
	for _, name := range []string{"active", "active-step", "fresh"} {
		if _, err = svc.Get(ctx, id(name)); err != nil {
			t.Fatalf("protected session %s deleted: %v", name, err)
		}
	}
}

func TestSessionPageLargeDatabaseStaysBounded(t *testing.T) {
	conn, q := pagingSQLite(t)
	_, err := conn.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<120000)
 INSERT INTO sessions(id,project_id,root_session_id,title,created_at,updated_at)
 SELECT printf('load-%05d',x),'load',CASE WHEN x%10=0 THEN printf('load-%05d',x) ELSE 'run' END,'load',x,x FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(q, "load")
	page, err := svc.Page(context.Background(), PageOptions{Limit: 50, RootsOnly: true, WithFlow: true})
	if err != nil || len(page.Sessions) != 50 || page.NextCursor == "" {
		t.Fatalf("large page=%d cursor=%q %v", len(page.Sessions), page.NextCursor, err)
	}
	snapshot, snapshotErr := svc.List(context.Background())
	if snapshotErr != nil || len(snapshot) != MaxPageSize {
		t.Fatalf("legacy snapshot=%d %v", len(snapshot), snapshotErr)
	}
	count := 0
	if err = svc.Walk(context.Background(), PageOptions{RootsOnly: true, Limit: 87}, func(Session) error { count++; return nil }); err != nil || count != 12000 {
		t.Fatalf("walk count=%d %v", count, err)
	}
	raw, _ := json.Marshal(page)
	if len(raw) > 40000 {
		t.Fatalf("unexpectedly retained %d bytes", len(raw))
	}
}

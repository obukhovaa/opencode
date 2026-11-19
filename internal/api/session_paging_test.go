package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/session"
)

func TestSessionHTTPPaginationPreservesArrayAndChildScopes(t *testing.T) {
	provider := db.NewSQLiteProvider(t.TempDir())
	conn, err := provider.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	goose.SetBaseFS(db.FS)
	if err = goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err = goose.Up(conn, "migrations/sqlite"); err != nil {
		t.Fatal(err)
	}
	sessions := session.NewService(db.New(conn), "api-page")
	ctx := context.Background()
	for i := 0; i < 125; i++ {
		if _, err = sessions.CreateWithID(ctx, fmt.Sprintf("s-%03d", i), fmt.Sprintf("Title %03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{app: &app.App{Sessions: sessions}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /session", s.handleSessionList)
	mux.HandleFunc("GET /session/status", s.handleSessionStatus)
	mux.HandleFunc("GET /session/{sessionID}/children", s.handleSessionChildren)
	read := func(url string) ([]APISession, http.Header, int) {
		request := httptest.NewRequest("GET", url, nil)
		request.Header.Set("X-Opencode-Directory", "/repo")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != 200 {
			return nil, response.Header(), response.Code
		}
		var rows []APISession
		if err = json.Unmarshal(response.Body.Bytes(), &rows); err != nil {
			t.Fatalf("array contract changed: %s", response.Body.String())
		}
		return rows, response.Header(), response.Code
	}
	url := "/session?limit=37"
	seen := map[string]bool{}
	for round := 0; ; round++ {
		if round > 5 {
			t.Fatal("HTTP cursor did not advance")
		}
		rows, headers, status := read(url)
		if status != 200 || len(rows) > 37 {
			t.Fatalf("bad page: %d/%d", status, len(rows))
		}
		for _, row := range rows {
			if seen[row.ID] {
				t.Fatal("repeated session")
			}
			seen[row.ID] = true
		}
		if headers.Get("X-Next-Cursor") == "" {
			break
		}
		if !strings.Contains(headers.Get("Access-Control-Expose-Headers"), "X-Next-Cursor") {
			t.Fatal("browser continuation header hidden")
		}
		url = strings.SplitN(strings.TrimPrefix(headers.Get("Link"), "<"), ">", 2)[0]
	}
	if len(seen) != 125 {
		t.Fatalf("only got %d sessions", len(seen))
	}
	idleStatus := httptest.NewRecorder()
	mux.ServeHTTP(idleStatus, httptest.NewRequest("GET", "/session/status", nil))
	if idleStatus.Code != http.StatusOK || strings.TrimSpace(idleStatus.Body.String()) != "{}" {
		t.Fatalf("idle history must not populate active status: %d %s", idleStatus.Code, idleStatus.Body.String())
	}
	_, _, status := read("/session?cursor=bad")
	if status != 400 {
		t.Fatalf("invalid cursor status=%d", status)
	}
	_, _, status = read("/session?limit=-1")
	if status != 400 {
		t.Fatalf("invalid limit status=%d", status)
	}
	if _, err = sessions.CreateTaskSession(ctx, "child-a", "s-000", "child a"); err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.CreateTaskSession(ctx, "child-b", "s-000", "child b"); err != nil {
		t.Fatal(err)
	}
	rows, headers, status := read("/session/s-000/children?limit=1")
	if status != 200 || len(rows) != 1 || rows[0].ID == "s-000" || headers.Get("X-Next-Cursor") == "" {
		t.Fatalf("child scope/limit incorrect: %+v %d", rows, status)
	}
}

package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/session"
)

func TestACPConsumesAndReturnsSessionCursor(t *testing.T) {
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
	sessions := session.NewService(db.New(conn), "acp-page")
	for i := 0; i < 125; i++ {
		if _, err = sessions.CreateWithID(context.Background(), fmt.Sprintf("s-%03d", i), "title"); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	h := NewHandler(&app.App{Sessions: sessions}, NewTransport(strings.NewReader(""), &output))
	cursor := ""
	seen := map[string]bool{}
	for round := 0; ; round++ {
		if round > 5 {
			t.Fatal("ACP cursor did not advance")
		}
		output.Reset()
		if err = h.HandleListSessions(round, ListSessionsParams{CWD: "/repo", Cursor: cursor}); err != nil {
			t.Fatal(err)
		}
		var response struct {
			Result ListSessionsResult `json:"result"`
			Error  *Error             `json:"error"`
		}
		if err = json.Unmarshal(output.Bytes(), &response); err != nil || response.Error != nil {
			t.Fatalf("bad response: %s", output.String())
		}
		if len(response.Result.Sessions) > session.DefaultPageSize {
			t.Fatal("ACP page bound lost")
		}
		for _, row := range response.Result.Sessions {
			if seen[row.SessionID] {
				t.Fatal("ACP repeated a row")
			}
			seen[row.SessionID] = true
			stamp, err := time.Parse(time.RFC3339, row.UpdatedAt)
			if err != nil || stamp.Year() < 2020 {
				t.Fatal("ACP timestamp used seconds as milliseconds", row.UpdatedAt)
			}
		}
		cursor = response.Result.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 125 {
		t.Fatalf("ACP only returned %d sessions", len(seen))
	}
	output.Reset()
	if err = h.HandleListSessions(9, ListSessionsParams{CWD: "/repo", Cursor: "invalid"}); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Error *Error `json:"error"`
	}
	json.Unmarshal(output.Bytes(), &response)
	if response.Error == nil || response.Error.Code != ErrCodeInvalidParams {
		t.Fatalf("invalid cursor was not rejected: %s", output.String())
	}
}

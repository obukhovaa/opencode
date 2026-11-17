package flowrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"

	"github.com/opencode-ai/opencode/internal/db"
)

func TestFlowCardsSQLite(t *testing.T) { runCardStoreSuite(t, sqliteStore) }

func TestFlowCardsMySQL(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN required")
	}
	runCardStoreSuite(t, func(t *testing.T) *Store {
		conn, err := sql.Open("mysql", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		migrateStore(t, conn, "mysql")
		return New(conn, mysqlQueries{db.NewMySQLQuerier(conn)}, "project-"+uuid.NewString(), "mysql")
	})
}

func runCardStoreSuite(t *testing.T, setup func(*testing.T) *Store) {
	ctx := context.Background()
	makeCard := func(runID string) Card {
		return Card{RunID: runID, Recipient: "slack-peer", MessageToken: "C123\x001234.5678", RenderHash: "digest", Sequence: 3, State: json.RawMessage(`{"status":"running","title":"résumé"}`)}
	}
	t.Run("recipient and project isolation", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "named")
		lease := leaseRun(t, s, r)
		card := makeCard(r.ID)
		if _, err := s.GetCard(ctx, r.ID, card.Recipient); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing card: %v", err)
		}
		if err := s.SaveCard(ctx, lease, card); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetCard(ctx, r.ID, card.Recipient)
		if err != nil || got.MessageToken != card.MessageToken || got.Sequence != card.Sequence || string(got.State) != string(card.State) || got.RenderHash != card.RenderHash || got.UpdatedAt < 1_000_000_000_000 {
			t.Fatalf("saved card: %+v %v", got, err)
		}
		if _, err := s.GetCard(ctx, r.ID, "another-peer"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("other recipient: %v", err)
		}
		second := card
		second.Recipient, second.MessageToken = "another-peer", "other-message"
		if err := s.SaveCard(ctx, lease, second); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetCard(ctx, r.ID, card.Recipient)
		if got.MessageToken != card.MessageToken {
			t.Fatal("recipient card overwritten")
		}
		foreign := New(s.conn, s.queries, "foreign-project", s.dialect)
		if _, err := foreign.GetCard(ctx, r.ID, card.Recipient); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign read: %v", err)
		}
		if err := foreign.SaveCard(ctx, lease, card); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("foreign write: %v", err)
		}
	})
	t.Run("monotonic state and same sequence repair", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "named")
		lease := leaseRun(t, s, r)
		card := makeCard(r.ID)
		if err := s.SaveCard(ctx, lease, card); err != nil {
			t.Fatal(err)
		}
		card.Sequence--
		if err := s.SaveCard(ctx, lease, card); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale sequence: %v", err)
		}
		card.Sequence++
		card.MessageToken = "replacement-message"
		card.State, card.RenderHash = json.RawMessage(`{"verbosity":"compact"}`), "new-digest"
		for range 2 {
			if err := s.SaveCard(ctx, lease, card); err != nil {
				t.Fatalf("same-sequence repair/retry: %v", err)
			}
		}
		card.Sequence++
		if err := s.SaveCard(ctx, lease, card); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetCard(ctx, r.ID, card.Recipient)
		if err != nil || got.Sequence != 4 || got.MessageToken != card.MessageToken || got.RenderHash != card.RenderHash || string(got.State) != string(card.State) {
			t.Fatalf("repaired card: %+v %v", got, err)
		}
	})
	t.Run("expired and superseded writers fenced", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "named")
		lease := leaseRun(t, s, r)
		card := makeCard(r.ID)
		if _, err := s.conn.Exec("UPDATE conversation_flow_runs SET lease_expires_at=0 WHERE id=?", r.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveCard(ctx, lease, card); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("expired owner: %v", err)
		}
		next, err := s.Claim(ctx, r.ID, "replacement", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.SaveCard(ctx, next, card); err != nil {
			t.Fatal(err)
		}
		card.Sequence++
		if err = s.SaveCard(ctx, lease, card); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("superseded owner: %v", err)
		}
	})
	t.Run("terminal delivery and deletion cascade", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "named")
		lease := leaseRun(t, s, r)
		if err := s.Release(ctx, lease, "completed"); err != nil {
			t.Fatal(err)
		}
		delivery, err := s.ClaimDelivery(ctx, r.ID, "delivery", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		card := makeCard(r.ID)
		if err := s.SaveCard(ctx, delivery, card); err != nil {
			t.Fatalf("terminal card: %v", err)
		}
		if _, err := s.conn.Exec("DELETE FROM conversation_flow_runs WHERE id=? AND project_id=?", r.ID, s.projectID); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := s.conn.QueryRow("SELECT COUNT(*) FROM conversation_flow_cards WHERE run_id=?", r.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cascade count=%d err=%v", count, err)
		}
	})
	t.Run("recipient move consumes source without overwriting destination", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "named")
		lease := leaseRun(t, s, r)
		card := makeCard(r.ID)
		if err := s.SaveCard(ctx, lease, card); err != nil {
			t.Fatal(err)
		}
		original, err := s.GetCard(ctx, r.ID, card.Recipient)
		if err != nil {
			t.Fatal(err)
		}
		moved, err := s.MoveCard(ctx, lease, card.Recipient, "thread-peer")
		if err != nil || moved.MessageToken != card.MessageToken || moved.Sequence != card.Sequence || string(moved.State) != string(card.State) || moved.Recipient != "thread-peer" || moved.UpdatedAt != original.UpdatedAt {
			t.Fatalf("move: %+v %v", moved, err)
		}
		if _, err := s.GetCard(ctx, r.ID, card.Recipient); !errors.Is(err, ErrNotFound) {
			t.Fatalf("old recipient survived: %v", err)
		}
		if _, err := s.MoveCard(ctx, lease, card.Recipient, "different-thread"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("source stolen twice: %v", err)
		}
		other := card
		other.Recipient, other.MessageToken = "occupied-peer", "other-token"
		if err := s.SaveCard(ctx, lease, other); err != nil {
			t.Fatal(err)
		}
		if _, err := s.MoveCard(ctx, lease, "thread-peer", other.Recipient); !errors.Is(err, ErrConflict) {
			t.Fatalf("occupied destination: %v", err)
		}
		for recipient, token := range map[string]string{"thread-peer": card.MessageToken, other.Recipient: other.MessageToken} {
			got, err := s.GetCard(ctx, r.ID, recipient)
			if err != nil || got.MessageToken != token {
				t.Fatalf("card overwritten: %+v %v", got, err)
			}
		}
		if _, err := s.conn.Exec("UPDATE conversation_flow_runs SET lease_expires_at=0 WHERE id=?", r.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.MoveCard(ctx, lease, "thread-peer", "next-peer"); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("expired move: %v", err)
		}
	})
	t.Run("invalid and oversized payloads", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "named")
		lease := leaseRun(t, s, r)
		for _, change := range []func(*Card){
			func(c *Card) { c.RunID = "other-run" },
			func(c *Card) { c.Recipient = "" },
			func(c *Card) { c.Sequence = -1 },
			func(c *Card) { c.State = json.RawMessage(`{"broken"`) },
			func(c *Card) { c.State = json.RawMessage(`"` + strings.Repeat("x", maxCardStateBytes) + `"`) },
			func(c *Card) { c.MessageToken = strings.Repeat("x", 4097) },
		} {
			card := makeCard(r.ID)
			change(&card)
			if err := s.SaveCard(ctx, lease, card); err == nil {
				t.Fatal("accepted invalid card")
			}
		}
		if _, err := s.GetCard(ctx, r.ID, "slack-peer"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("invalid card persisted: %v", err)
		}
	})
}

func TestFlowCardSQLiteReopen(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "cards.db") + "?_pragma=foreign_keys(1)"
	open := func() *sql.DB {
		conn, err := sql.Open("sqlite3", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	first := open()
	migrateStore(t, first, "sqlite3")
	s := New(first, sqliteQueries{db.New(first)}, "reopen-project", "sqlite")
	r := newRun(t, s, "named")
	lease := leaseRun(t, s, r)
	card := Card{RunID: r.ID, Recipient: "telegram-peer", MessageToken: "123:456", Sequence: 9, State: json.RawMessage(`{"status":"completed"}`)}
	if err := s.SaveCard(ctx, lease, card); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := open()
	reopened := New(second, sqliteQueries{db.New(second)}, "reopen-project", "sqlite")
	got, err := reopened.GetCard(ctx, r.ID, card.Recipient)
	if err != nil || got.MessageToken != card.MessageToken || got.Sequence != card.Sequence || string(got.State) != string(card.State) {
		t.Fatalf("reopened card: %+v %v", got, err)
	}
}

package flowrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/pressly/goose/v3"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

type sqliteQueries struct{ *db.Queries }

func (q sqliteQueries) WithTx(tx *sql.Tx) db.QuerierWithTx {
	return sqliteQueries{q.Queries.WithTx(tx)}
}

type mysqlQueries struct{ *db.MySQLQuerier }

func (q mysqlQueries) WithTx(tx *sql.Tx) db.QuerierWithTx {
	return mysqlQueries{q.MySQLQuerier.WithTx(tx)}
}

var migrateMu sync.Mutex

func migrateStore(t *testing.T, conn *sql.DB, dialect string) {
	t.Helper()
	migrateMu.Lock()
	defer migrateMu.Unlock()
	goose.SetBaseFS(db.FS)
	if err := goose.SetDialect(dialect); err != nil {
		t.Fatal(err)
	}
	dir := dialect
	if dir == "sqlite3" {
		dir = "sqlite"
	}
	if err := goose.Up(conn, "migrations/"+dir); err != nil {
		t.Fatal(err)
	}
}
func sqliteStore(t *testing.T) *Store {
	t.Helper()
	conn, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "flow.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	migrateStore(t, conn, "sqlite3")
	return New(conn, sqliteQueries{db.New(conn)}, "project-"+uuid.NewString(), "sqlite")
}
func conversation(t *testing.T, s *Store) string {
	t.Helper()
	id := uuid.NewString()
	_, err := s.queries.CreateSession(context.Background(), db.CreateSessionParams{ID: id, Title: "conversation", ProjectID: sql.NullString{String: s.projectID, Valid: true}, RootSessionID: sql.NullString{String: id, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func newRun(t *testing.T, s *Store, kind string) Run {
	t.Helper()
	r, created, err := s.Create(context.Background(), Run{ConversationID: conversation(t, s), InvocationID: "invoke", Kind: kind, Snapshot: json.RawMessage(`{"step":"work"}`), Origin: json.RawMessage(`{"requester":"user","job_id":"job"}`)})
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	return r
}
func leaseRun(t *testing.T, s *Store, r Run) Lease {
	t.Helper()
	l, err := s.Claim(context.Background(), r.ID, "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func TestStoreSQLite(t *testing.T) { runStoreSuite(t, sqliteStore) }
func runStoreSuite(t *testing.T, setup func(*testing.T) *Store) {
	ctx := context.Background()
	t.Run("idempotent admission and sequential conversations", func(t *testing.T) {
		s := setup(t)
		conv := conversation(t, s)
		request := Run{ConversationID: conv, InvocationID: "one", Kind: "generated", Snapshot: json.RawMessage(`{"a":1}`)}
		r, created, err := s.Create(ctx, request)
		if err != nil || !created {
			t.Fatalf("create %v", err)
		}
		retry, created, err := s.Create(ctx, request)
		if err != nil || created || retry.ID != r.ID {
			t.Fatalf("retry %v created=%v", err, created)
		}
		request.Snapshot = json.RawMessage(`{"a":2}`)
		if _, _, err = s.Create(ctx, request); !errors.Is(err, ErrConflict) {
			t.Fatalf("changed retry: %v", err)
		}
		request.InvocationID = "two"
		if _, _, err = s.Create(ctx, request); !errors.Is(err, ErrBusy) {
			t.Fatalf("second active: %v", err)
		}
		l := leaseRun(t, s, r)
		if err = s.Release(ctx, l, "suspended"); err != nil {
			t.Fatal(err)
		}
		if _, _, err = s.Create(ctx, request); !errors.Is(err, ErrBusy) {
			t.Fatalf("suspended lost reservation: %v", err)
		}
		l, err = s.Claim(ctx, r.ID, "worker-b", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Release(ctx, l, "completed"); err != nil {
			t.Fatal(err)
		}
		next, created, err := s.Create(ctx, request)
		if err != nil || !created || next.ID == r.ID {
			t.Fatalf("next: %v", err)
		}
		foreign := New(s.conn, s.queries, "another-project", s.dialect)
		if _, err = foreign.Get(ctx, r.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-project get: %v", err)
		}
	})
	t.Run("concurrent claim and expired owner fencing", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "generated")
		start := make(chan struct{})
		type claimResult struct {
			lease Lease
			err   error
		}
		results := make(chan claimResult, 2)
		for _, owner := range []string{"one", "two"} {
			go func(owner string) {
				<-start
				l, err := s.Claim(ctx, r.ID, owner, time.Minute)
				results <- claimResult{l, err}
			}(owner)
		}
		close(start)
		var won Lease
		success := 0
		for range 2 {
			result := <-results
			l, err := result.lease, result.err
			if err == nil {
				won = l
				success++
			} else if !errors.Is(err, ErrBusy) {
				t.Fatal(err)
			}
		}
		if success != 1 {
			t.Fatalf("owners=%d", success)
		}
		// Expire through SQL to avoid clock-dependent sleeps, then assert a new token.
		if _, err := s.conn.Exec("UPDATE conversation_flow_runs SET lease_expires_at=0 WHERE id=?", r.ID); err != nil {
			t.Fatal(err)
		}
		replacement, err := s.Claim(ctx, r.ID, "replacement", time.Minute)
		if err != nil || replacement.Token <= won.Token {
			t.Fatalf("takeover: %+v %v", replacement, err)
		}
		if _, err = s.Renew(ctx, won, time.Minute); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("stale renewal %v", err)
		}
		if _, err = s.SaveSnapshot(ctx, won, json.RawMessage(`{"step":"changed"}`), r.Version); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("stale snapshot %v", err)
		}
		if err = s.Transition(ctx, won, "completed"); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("stale complete %v", err)
		}
		saved, err := s.SaveSnapshot(ctx, replacement, json.RawMessage(`{"step":"changed"}`), r.Version)
		if err != nil || saved.Version != r.Version+1 {
			t.Fatalf("save %+v %v", saved, err)
		}
	})
	t.Run("durable inbox outbox and terminal receipts", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "generated")
		l := leaseRun(t, s, r)
		first, created, err := s.QueueInbox(ctx, r.ID, "msg1", json.RawMessage(`{"text":"one"}`))
		if err != nil || !created {
			t.Fatal(err)
		}
		_, created, err = s.QueueInbox(ctx, r.ID, "msg1", json.RawMessage(`{"text":"one"}`))
		if err != nil || created {
			t.Fatalf("inbox retry %v", err)
		}
		second, _, err := s.QueueInbox(ctx, r.ID, "msg2", json.RawMessage(`{"text":"two"}`))
		if err != nil || second.Sequence <= first.Sequence {
			t.Fatal(err)
		}
		if _, _, err = s.QueueInbox(ctx, r.ID, "msg1", json.RawMessage(`{"text":"different"}`)); !errors.Is(err, ErrConflict) {
			t.Fatalf("changed id accepted: %v", err)
		}
		stale := l
		stale.Token--
		if err = s.AckInbox(ctx, stale, []string{"msg1"}); !errors.Is(err, ErrLeaseLost) {
			t.Fatal(err)
		}
		if err = s.AckInbox(ctx, l, []string{"msg1"}); err != nil {
			t.Fatal(err)
		}
		recovered := New(s.conn, s.queries, s.projectID, s.dialect)
		pending, err := recovered.PeekInbox(ctx, r.ID)
		if err != nil || len(pending) != 1 || pending[0].ID != "msg2" {
			t.Fatalf("pending %+v %v", pending, err)
		}
		history, err := recovered.InboxHistory(ctx, r.ID)
		if err != nil || len(history) != 2 || history[0].ID != "msg1" || history[1].ID != "msg2" {
			t.Fatalf("history %+v %v", history, err)
		}
		accepted, err := recovered.GetInbox(ctx, r.ID, "msg1")
		if err != nil || string(accepted.Payload) != `{"text":"one"}` {
			t.Fatalf("accepted %+v %v", accepted, err)
		}
		foreign := New(s.conn, s.queries, "foreign", s.dialect)
		if _, err = foreign.GetInbox(ctx, r.ID, "msg1"); !errors.Is(err, ErrNotFound) {
			t.Fatal("inbox lookup crossed project boundary")
		}
		if _, err = foreign.InboxHistory(ctx, r.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("inbox history crossed project boundary")
		}

		event, err := s.AppendEvent(ctx, l, "done", "completed", json.RawMessage(`{"ok":true}`))
		if err != nil {
			t.Fatal(err)
		}
		retry, err := s.AppendEvent(ctx, l, "done", "completed", json.RawMessage(`{"ok":true}`))
		if err != nil || retry.Sequence != event.Sequence {
			t.Fatal(err)
		}
		if err = s.Transition(ctx, l, "completed"); err != nil {
			t.Fatal(err)
		}
		if err = s.AckEvent(ctx, l, "done", "peer1"); err != nil {
			t.Fatal(err)
		}
		if err = s.Release(ctx, l, "completed"); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Claim(ctx, r.ID, "executor", time.Minute); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("terminal execution claim: %v", err)
		}
		delivery, err := s.ClaimDelivery(ctx, r.ID, "relay", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.CheckDeliveryLease(ctx, delivery); err != nil {
			t.Fatal(err)
		}
		if err = s.CheckDeliveryLease(ctx, l); !errors.Is(err, ErrLeaseLost) {
			t.Fatal("stale delivery lease accepted")
		}
		if err = s.AckEvent(ctx, delivery, "done", "peer2"); err != nil {
			t.Fatal(err)
		}
		if delivered, err := s.Delivered(ctx, r.ID, "done", "peer1"); err != nil || !delivered {
			t.Fatal(err)
		}
		if delivered, err := s.Delivered(ctx, r.ID, "done", "other"); err != nil || delivered {
			t.Fatal("recipient receipts not scoped")
		}
		if err = s.Transition(ctx, delivery, "running"); !errors.Is(err, ErrConflict) {
			t.Fatalf("terminal resurrection: %v", err)
		}
	})
	t.Run("question rebind late answer and immutable prompt", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "named")
		l := leaseRun(t, s, r)
		q := Question{ID: "logical", SessionID: "step-session", StepID: "review", RequestID: "first-widget", Descriptor: json.RawMessage(`{"question":"approve?"}`)}
		if _, err := s.PutQuestion(ctx, l, q); err != nil {
			t.Fatal(err)
		}
		q.RequestID = "second-widget"
		if _, err := s.PutQuestion(ctx, l, q); err != nil {
			t.Fatal(err)
		}
		restored, err := s.FindQuestionByRequest(ctx, "first-widget")
		if err != nil || restored.ID != "logical" || restored.RequestID != "second-widget" {
			t.Fatalf("alias %+v %v", restored, err)
		}
		if _, err = s.FindQuestionByRequest(ctx, "logical"); err != nil {
			t.Fatal(err)
		}
		_, created, err := s.AnswerQuestion(ctx, r.ID, q.ID, "reply", json.RawMessage(`{"answer":"yes"}`))
		if err != nil || !created {
			t.Fatal(err)
		}
		if err = s.AckAnswer(ctx, l, q.ID); err != nil {
			t.Fatal(err)
		}
		_, created, err = s.AnswerQuestion(ctx, r.ID, q.ID, "reply", json.RawMessage(`{"answer":"yes"}`))
		if err != nil || created {
			t.Fatalf("reply retry: %v", err)
		}
		if _, _, err = s.AnswerQuestion(ctx, r.ID, q.ID, "reply", json.RawMessage(`{"answer":"no"}`)); !errors.Is(err, ErrConflict) {
			t.Fatal("changed answer accepted")
		}
		q.RequestID = "third-widget"
		restored, err = s.PutQuestion(ctx, l, q)
		if err != nil || restored.Status != "delivered" {
			t.Fatal("rebind lost receipt")
		}
		if questions, err := s.Questions(ctx, r.ID); err != nil || len(questions) != 0 {
			t.Fatal("delivered question resurfaced")
		}
		if _, err = s.SaveSnapshot(ctx, l, json.RawMessage(`{"changed":true}`), r.Version); !errors.Is(err, ErrConflict) {
			t.Fatal("named definition edited")
		}
		first, err := s.PutPrompt(ctx, l, "review", json.RawMessage(`{"text":"version one","version":1}`))
		if err != nil {
			t.Fatal(err)
		}
		again, err := s.PutPrompt(ctx, l, "review", json.RawMessage(`{"text":"version two","version":2}`))
		if err != nil || string(first) != string(again) {
			t.Fatal("remote prompt changed after entry")
		}
	})
	t.Run("fenced state transaction and stale writer", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "generated")
		l := leaseRun(t, s, r)
		step := uuid.NewString()
		if _, err := s.queries.CreateSession(ctx, db.CreateSessionParams{ID: step, Title: "step", ProjectID: sql.NullString{String: s.projectID, Valid: true}, RootSessionID: sql.NullString{String: r.ID, Valid: true}}); err != nil {
			t.Fatal(err)
		}
		q := s.FencedQuerier(l)
		if _, err := q.CreateFlowState(ctx, db.CreateFlowStateParams{SessionID: step, RootSessionID: r.ID, FlowID: "flow", StepID: "work", Status: "running", Iteration: 1}); err != nil {
			t.Fatal(err)
		}
		tx, err := s.conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = q.WithTx(tx).UpdateFlowState(ctx, db.UpdateFlowStateParams{SessionID: step, Status: "completed", Iteration: 1}); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		tx.Rollback()
		row, err := s.queries.GetFlowState(ctx, step)
		if err != nil || row.Status != "running" {
			t.Fatal("write escaped caller transaction")
		}
		if _, err = s.conn.Exec("UPDATE conversation_flow_runs SET lease_expires_at=0 WHERE id=?", r.ID); err != nil {
			t.Fatal(err)
		}
		next, err := s.Claim(ctx, r.ID, "new", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = q.UpdateFlowState(ctx, db.UpdateFlowStateParams{SessionID: step, Status: "completed"}); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("stale state write accepted: %v", err)
		}
		if _, err = s.FencedQuerier(next).UpdateFlowState(ctx, db.UpdateFlowStateParams{SessionID: step, Status: "completed", Iteration: 1}); err != nil {
			t.Fatal(err)
		}
		if err = q.DeleteFlowStatesByRootSession(ctx, r.ID); !errors.Is(err, ErrLeaseLost) {
			t.Fatal("stale deletion accepted")
		}
		if err = s.FencedQuerier(next).DeleteFlowStatesByRootSession(ctx, "another-run"); !errors.Is(err, ErrConflict) {
			t.Fatal("foreign root deletion accepted")
		}
	})
	t.Run("atomic batch and delivery event", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "generated")
		l := leaseRun(t, s, r)
		batch := []Inbox{{ID: "a", Payload: json.RawMessage(`{"text":"a"}`)}, {ID: "b", Payload: json.RawMessage(`{"text":"b"}`)}}
		accepted, err := s.QueueInboxBatch(ctx, r.ID, batch)
		if err != nil || len(accepted) != 2 || accepted[1].Sequence != accepted[0].Sequence+1 {
			t.Fatalf("batch %+v %v", accepted, err)
		}
		_, err = s.QueueInboxBatch(ctx, r.ID, []Inbox{{ID: "c", Payload: json.RawMessage(`{}`)}, {ID: "a", Payload: json.RawMessage(`{"changed":true}`)}})
		if !errors.Is(err, ErrConflict) {
			t.Fatal("conflicting batch accepted")
		}
		pending, err := s.PeekInbox(ctx, r.ID)
		if err != nil || len(pending) != 2 {
			t.Fatal("failed batch partially persisted")
		}
		if _, err = s.AppendEvent(ctx, l, "receipt", "delivered", json.RawMessage(`{"ids":["other"]}`)); err != nil {
			t.Fatal(err)
		}
		_, err = s.AckInboxEvent(ctx, l, []string{"a", "b"}, "receipt", "delivered", json.RawMessage(`{"ids":["a","b"]}`))
		if !errors.Is(err, ErrConflict) {
			t.Fatal("changed receipt accepted")
		}
		pending, err = s.PeekInbox(ctx, r.ID)
		if err != nil || len(pending) != 2 {
			t.Fatal("delivery ack committed without its event")
		}
		mutations := 0
		mutate := func(q db.QuerierWithTx) error {
			mutations++
			_, err := q.RenameSession(ctx, db.RenameSessionParams{ID: r.ConversationID, Title: "input delivered"})
			return err
		}
		for range 2 {
			if _, err = s.AckInboxEvent(ctx, l, []string{"a", "b"}, "receipt2", "delivered", json.RawMessage(`{"ids":["a","b"]}`), mutate); err != nil {
				t.Fatal(err)
			}
		}
		if mutations != 1 {
			t.Fatal("duplicate inbox event repeated user-message mutation")
		}
		pending, err = s.PeekInbox(ctx, r.ID)
		if err != nil || len(pending) != 0 {
			t.Fatal("atomic delivery did not consume batch")
		}
		if err = s.Release(ctx, l, ""); err != nil {
			t.Fatal(err)
		}
		stored, err := s.Get(ctx, r.ID)
		if err != nil || stored.Status != r.Status {
			t.Fatal("empty release changed status")
		}
	})
	t.Run("lease expiry measured after contended lock", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "generated")
		l, err := s.Claim(ctx, r.ID, "owner", 200*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := s.conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err = s.lockRun(ctx, tx, r.ID); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, err := s.Renew(ctx, l, time.Minute); done <- err }()
		time.Sleep(300 * time.Millisecond)
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if !errors.Is(err, ErrLeaseLost) {
				t.Fatalf("expired waiting renewal: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("renewal stuck")
		}
	})
	t.Run("lossless snapshot and paginated event replay", func(t *testing.T) {
		s := setup(t)
		conv := conversation(t, s)
		var previous string
		for i := 0; i < 3; i++ {
			r, _, err := s.Create(ctx, Run{ID: fmt.Sprintf("run-%d-%s", i, conv), ConversationID: conv, InvocationID: fmt.Sprint(i), Kind: "named", Snapshot: json.RawMessage(`{"exact_id":9007199254740993}`)})
			if err != nil {
				t.Fatal(err)
			}
			if string(r.Snapshot) != `{"exact_id":9007199254740993}` {
				t.Fatalf("snapshot number rounded: %s", r.Snapshot)
			}
			l := leaseRun(t, s, r)
			if _, err = s.AppendEvent(ctx, l, "event", "completed", json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			if err = s.Release(ctx, l, "completed"); err != nil {
				t.Fatal(err)
			}
		}
		seen := map[string]bool{}
		for {
			page, err := s.RunsWithEventsAfter(ctx, previous, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			if seen[page[0].ID] {
				t.Fatal("replay cursor repeated run")
			}
			seen[page[0].ID] = true
			previous = page[0].ID
		}
		if len(seen) != 3 {
			t.Fatalf("replay missed runs: %v", seen)
		}
	})
	t.Run("legacy standalone root identity", func(t *testing.T) {
		s := setup(t)
		id := strings.Repeat("legacy-", 25) + uuid.NewString()
		r, _, err := s.Create(ctx, Run{ID: id, ConversationID: conversation(t, s), InvocationID: "legacy-import", Kind: "standalone"})
		if err != nil || r.ID != id {
			t.Fatalf("legacy root: %v", err)
		}
		l := leaseRun(t, s, r)
		if _, err = s.AppendEvent(ctx, l, strings.Repeat("e", 255), "completed", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if err = s.AckEvent(ctx, l, strings.Repeat("e", 255), strings.Repeat("r", 255)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("terminal transition and event are atomic", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "generated")
		l := leaseRun(t, s, r)
		if _, err := s.AppendEvent(ctx, l, "terminal", "completed", json.RawMessage(`{"value":"old"}`)); err != nil {
			t.Fatal(err)
		}
		_, err := s.TransitionEvent(ctx, l, "completed", "terminal", "completed", json.RawMessage(`{"value":"new"}`))
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("event conflict: %v", err)
		}
		same, err := s.Get(ctx, r.ID)
		if err != nil || same.Status != r.Status {
			t.Fatal("status committed without terminal event")
		}
		event, err := s.TransitionEvent(ctx, l, "completed", "terminal2", "completed", json.RawMessage(`{"done":true}`))
		if err != nil {
			t.Fatal(err)
		}
		retry, err := s.TransitionEvent(ctx, l, "completed", "terminal2", "completed", json.RawMessage(`{"done":true}`))
		if err != nil || retry.Sequence != event.Sequence {
			t.Fatalf("terminal retry %v", err)
		}
		if err = s.CheckLease(ctx, l); !errors.Is(err, ErrLeaseLost) {
			t.Fatal("terminal lease authorized new tool")
		}
	})
	t.Run("conversation deletion is atomic and refuses live owners", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "generated")
		l := leaseRun(t, s, r)
		called := false
		remove := func(q db.QuerierWithTx) error {
			called = true
			return q.DeleteSessionTree(ctx, db.DeleteSessionTreeParams{ID: r.ConversationID, RootSessionID: sql.NullString{String: r.ConversationID, Valid: true}})
		}
		if err := s.DeleteConversation(ctx, r.ConversationID, remove); !errors.Is(err, ErrBusy) || called {
			t.Fatalf("active deletion %v called=%v", err, called)
		}
		if _, err := s.AppendEvent(ctx, l, "event", "completed", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if err := s.Release(ctx, l, "completed"); err != nil {
			t.Fatal(err)
		}
		rollback := errors.New("session delete failed")
		if err := s.DeleteConversation(ctx, r.ConversationID, func(db.QuerierWithTx) error { return rollback }); !errors.Is(err, rollback) {
			t.Fatal(err)
		}
		if _, err := s.Get(ctx, r.ID); err != nil {
			t.Fatal("metadata escaped rollback")
		}
		if err := s.DeleteConversation(ctx, r.ConversationID, remove); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(ctx, r.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("run retained after delete")
		}
		if _, err := s.queries.GetSessionByID(ctx, r.ConversationID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("session retained")
		}
		var count int
		if err := s.conn.QueryRow("SELECT COUNT(*) FROM conversation_flow_events WHERE run_id=?", r.ID).Scan(&count); err != nil || count != 0 {
			t.Fatal("outbox did not cascade")
		}
	})
	t.Run("create cannot race conversation deletion", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "generated")
		l := leaseRun(t, s, r)
		if err := s.Release(ctx, l, "completed"); err != nil {
			t.Fatal(err)
		}
		entered, release := make(chan struct{}), make(chan struct{})
		deleted := make(chan error, 1)
		go func() {
			deleted <- s.DeleteConversation(ctx, r.ConversationID, func(q db.QuerierWithTx) error {
				close(entered)
				<-release
				return q.DeleteSessionTree(ctx, db.DeleteSessionTreeParams{ID: r.ConversationID, RootSessionID: sql.NullString{String: r.ConversationID, Valid: true}})
			})
		}()
		<-entered
		created := make(chan error, 1)
		go func() {
			_, _, err := s.Create(ctx, Run{ConversationID: r.ConversationID, InvocationID: "race", Kind: "generated"})
			created <- err
		}()
		time.Sleep(20 * time.Millisecond)
		close(release)
		if err := <-deleted; err != nil {
			t.Fatal(err)
		}
		if err := <-created; !errors.Is(err, ErrNotFound) {
			t.Fatalf("creation crossed deletion: %v", err)
		}
		runs, err := s.List(ctx, r.ConversationID)
		if err != nil || len(runs) != 0 {
			t.Fatal("orphaned run created")
		}
	})
	t.Run("message mutations reject expired worker", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "generated")
		l := leaseRun(t, s, r)
		messages := message.NewService(s.queries, s.conn)
		defer messages.Shutdown()
		guarded := message.WithWriteGuard(ctx, func(ctx context.Context, tx *sql.Tx) error { return s.CheckLeaseTx(ctx, tx, l) })
		original, err := messages.Create(guarded, r.ConversationID, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "original"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = messages.Create(guarded, r.ConversationID, message.CreateMessageParams{Role: message.User, Seq: 42, Parts: []message.ContentPart{message.TextContent{Text: "explicit"}}}); err != nil {
			t.Fatal(err)
		}
		before, err := messages.List(ctx, r.ConversationID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.conn.Exec("UPDATE conversation_flow_runs SET lease_expires_at=0 WHERE id=?", r.ID); err != nil {
			t.Fatal(err)
		}
		fresh, err := s.Claim(ctx, r.ID, "replacement", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		inherited := tools.InheritFlowLimits(context.Background(), guarded)
		for _, seq := range []int64{0, 99} {
			if _, err = messages.Create(inherited, r.ConversationID, message.CreateMessageParams{Role: message.Assistant, Seq: seq}); !errors.Is(err, ErrLeaseLost) {
				t.Fatalf("stale create: %v", err)
			}
		}
		if _, _, err = messages.CreatePair(guarded, r.ConversationID, message.CreateMessageParams{Role: message.Assistant}, message.CreateMessageParams{Role: message.Tool}); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("stale pair: %v", err)
		}
		original.Parts = []message.ContentPart{message.TextContent{Text: "stale"}}
		if err = messages.Update(guarded, original); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("stale update: %v", err)
		}
		if err = messages.Delete(guarded, original.ID); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("stale delete: %v", err)
		}
		after, err := messages.List(ctx, r.ConversationID)
		if err != nil || len(after) != len(before) || after[0].Content().String() != "original" {
			t.Fatal("stale worker changed transcript")
		}
		newCtx := message.WithWriteGuard(ctx, func(ctx context.Context, tx *sql.Tx) error { return s.CheckLeaseTx(ctx, tx, fresh) })
		original.Parts = []message.ContentPart{message.TextContent{Text: "current"}}
		if err = messages.Update(newCtx, original); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("answer result and receipt commit once", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "generated")
		l := leaseRun(t, s, r)
		q := Question{ID: "logical", SessionID: r.ConversationID, StepID: "step", RequestID: "request", Descriptor: json.RawMessage(`{}`)}
		if _, err := s.PutQuestion(ctx, l, q); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.AnswerQuestion(ctx, r.ID, q.ID, "answer", json.RawMessage(`{"answer":"yes"}`)); err != nil {
			t.Fatal(err)
		}
		rollback := errors.New("after mutation")
		if err := s.CommitAnswer(ctx, l, q.ID, func(q db.QuerierWithTx) error {
			if _, err := q.RenameSession(ctx, db.RenameSessionParams{ID: r.ConversationID, Title: "changed"}); err != nil {
				return err
			}
			return rollback
		}); !errors.Is(err, rollback) {
			t.Fatal(err)
		}
		session, err := s.queries.GetSessionByID(ctx, r.ConversationID)
		if err != nil || session.Title == "changed" {
			t.Fatal("answer mutation escaped rollback")
		}
		question, err := s.GetQuestion(ctx, r.ID, q.ID)
		if err != nil || question.Status != "answered" {
			t.Fatal("receipt escaped rollback")
		}
		count := 0
		mutate := func(q db.QuerierWithTx) error {
			count++
			_, err := q.RenameSession(ctx, db.RenameSessionParams{ID: r.ConversationID, Title: "committed"})
			return err
		}
		if err = s.CommitAnswer(ctx, l, q.ID, mutate); err != nil {
			t.Fatal(err)
		}
		if err = s.CommitAnswer(ctx, l, q.ID, mutate); err != nil || count != 1 {
			t.Fatalf("repeated answer mutation count=%d err=%v", count, err)
		}
	})
	t.Run("conversation creation order survives clock ties", func(t *testing.T) {
		s := setup(t)
		first := newRun(t, s, "generated")
		l := leaseRun(t, s, first)
		if err := s.Release(ctx, l, "completed"); err != nil {
			t.Fatal(err)
		}
		future := first.CreatedAt + 60000
		if _, err := s.conn.Exec("UPDATE conversation_flow_runs SET created_at=? WHERE id=?", future, first.ID); err != nil {
			t.Fatal(err)
		}
		next, _, err := s.Create(ctx, Run{ConversationID: first.ConversationID, InvocationID: "next", Kind: "generated"})
		if err != nil || next.CreatedAt != future+1 {
			t.Fatalf("order %+v %v", next, err)
		}
		runs, err := s.List(ctx, first.ConversationID)
		if err != nil || runs[0].ID != next.ID {
			t.Fatal("latest run ambiguous")
		}
		latest, err := s.Latest(ctx, first.ConversationID)
		if err != nil || latest.ID != next.ID {
			t.Fatal("bounded latest lookup disagrees")
		}
		original, err := s.ByInvocation(ctx, first.ConversationID, first.InvocationID)
		if err != nil || original.ID != first.ID {
			t.Fatal("bounded invocation lookup failed")
		}

	})
	t.Run("expired deadline settles without a runnable lease", func(t *testing.T) {
		s := setup(t)
		r, _, err := s.Create(ctx, Run{ConversationID: conversation(t, s), InvocationID: "deadline", Kind: "generated", Deadline: time.Now().Add(time.Minute).UnixMilli()})
		if err != nil {
			t.Fatal(err)
		}
		l := leaseRun(t, s, r)
		payload := json.RawMessage(`{"reason":"deadline exceeded"}`)
		if _, err = s.TerminateExpired(ctx, r.ID, "expired", "terminated", payload); !errors.Is(err, ErrConflict) {
			t.Fatalf("live deadline stopped: %v", err)
		}
		if _, err = s.conn.Exec("UPDATE conversation_flow_runs SET deadline=1,lease_expires_at=1 WHERE id=?", r.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Claim(ctx, r.ID, "control", time.Minute); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("expired execution admitted: %v", err)
		}
		event, err := s.TerminateExpired(ctx, r.ID, "expired", "terminated", payload)
		if err != nil {
			t.Fatal(err)
		}
		again, err := s.TerminateExpired(ctx, r.ID, "expired", "terminated", payload)
		if err != nil || again.Sequence != event.Sequence {
			t.Fatalf("expiry retry: %v", err)
		}
		if err = s.CheckDeliveryLease(ctx, l); !errors.Is(err, ErrLeaseLost) {
			t.Fatal("old expiredowner retained authority")
		}
		stored, err := s.Get(ctx, r.ID)
		if err != nil || stored.Status != "terminated" {
			t.Fatal("deadline record stranded")
		}
		if _, _, err = s.Create(ctx, Run{ConversationID: r.ConversationID, InvocationID: "next", Kind: "generated"}); err != nil {
			t.Fatalf("deadline still reserves conversation: %v", err)
		}
	})
	t.Run("queued input wins unanswered question and accepted answer wins later input", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "generated")
		l := leaseRun(t, s, r)
		q := Question{ID: "q1", SessionID: r.ConversationID, RequestID: "req1", Descriptor: json.RawMessage(`{}`)}
		if _, err := s.PutQuestion(ctx, l, q); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.QueueInbox(ctx, r.ID, "steer", json.RawMessage(`{"targetQuestionId":"q1","text":"change goal"}`)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.AnswerQuestion(ctx, r.ID, q.ID, "reply", json.RawMessage(`{"answers":[["yes"]]}`)); !errors.Is(err, ErrInputPending) {
			t.Fatalf("queued input lost priority: %v", err)
		}
		interrupted, err := s.InterruptQuestion(ctx, l, q.ID, "queued input")
		if err != nil || interrupted.Status != "interrupted" {
			t.Fatalf("interrupt %+v %v", interrupted, err)
		}
		if err = s.CommitAnswer(ctx, l, q.ID, func(db.QuerierWithTx) error { return nil }); err != nil {
			t.Fatal(err)
		}
		q.ID = "q2"
		q.RequestID = "req2"
		if _, err = s.PutQuestion(ctx, l, q); err != nil {
			t.Fatal(err)
		}
		if _, _, err = s.AnswerQuestion(ctx, r.ID, q.ID, "reply2", json.RawMessage(`{"answers":[["yes"]]}`)); err != nil {
			t.Fatal(err)
		}
		if _, _, err = s.QueueInbox(ctx, r.ID, "late", json.RawMessage(`{"targetQuestionId":"q2","text":"later"}`)); err != nil {
			t.Fatal(err)
		}
		answered, err := s.InterruptQuestion(ctx, l, q.ID, "queued input")
		if err != nil || answered.Status != "answered" || answered.AnswerID != "reply2" {
			t.Fatal("later input overwrote committed answer")
		}
	})
	t.Run("untargeted input is claimed by one competing question", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "generated")
		l := leaseRun(t, s, r)
		for _, id := range []string{"one", "two"} {
			if _, err := s.PutQuestion(ctx, l, Question{ID: id, SessionID: "step-" + id, StepID: id, RequestID: "req-" + id, Descriptor: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
		}
		items := []Inbox{{ID: "m1", Payload: json.RawMessage(`{"text":"new directions","rootMessageId":"stable","number":9007199254740993}`)}, {ID: "m2", Payload: json.RawMessage(`{"text":"also tests"}`)}, {ID: "stale-hint", Payload: json.RawMessage(`{"text":"normal boundary","noQuestionRetarget":true}`)}}
		if _, err := s.QueueInboxBatch(ctx, r.ID, items); err != nil {
			t.Fatal(err)
		}
		type targetResult struct {
			q     Question
			items []Inbox
			err   error
		}
		results := make(chan targetResult, 2)
		start := make(chan struct{})
		for _, id := range []string{"one", "two"} {
			go func(id string) {
				<-start
				q, items, err := s.TargetInbox(ctx, l, id, "new input")
				results <- targetResult{q, items, err}
			}(id)
		}
		close(start)
		winner := ""
		for range 2 {
			result := <-results
			if result.err != nil {
				t.Fatal(result.err)
			}
			if len(result.items) > 0 {
				if winner != "" || len(result.items) != 2 || result.q.Status != "interrupted" {
					t.Fatalf("multiple consumers: %+v", result)
				}
				winner = result.q.ID
			} else if result.q.Status != "pending" {
				t.Fatal("losing question changed")
			}
		}
		if winner == "" {
			t.Fatal("no question claimed prior input")
		}
		history, err := s.InboxHistory(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range history {
			var payload map[string]json.RawMessage
			if err = json.Unmarshal(item.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if item.ID == "stale-hint" {
				if len(payload["targetQuestionId"]) > 0 {
					t.Fatal("stale explicitly hinted input retargeted")
				}
				continue
			}
			var target string
			json.Unmarshal(payload["targetQuestionId"], &target)
			if target != winner {
				t.Fatal("batch split between questions")
			}
			if item.ID == "m1" && string(payload["number"]) != "9007199254740993" {
				t.Fatal("targeting rounded input")
			}
		}
		repeated, claimed, err := s.TargetInbox(ctx, l, winner, "retry")
		if err != nil || repeated.Status != "interrupted" || len(claimed) != 2 {
			t.Fatalf("retry %+v %v", claimed, err)
		}
		if _, _, err = s.QueueInbox(ctx, r.ID, "later", json.RawMessage(`{"text":"later"}`)); err != nil {
			t.Fatal(err)
		}
		_, claimed, err = s.TargetInbox(ctx, l, winner, "retry")
		if err != nil || len(claimed) != 2 {
			t.Fatal("interrupted question claimed newer input")
		}
	})
	t.Run("answered question cannot claim future input", func(t *testing.T) {
		s := setup(t)
		r := newRun(t, s, "generated")
		l := leaseRun(t, s, r)
		if _, err := s.PutQuestion(ctx, l, Question{ID: "answered", SessionID: r.ConversationID, RequestID: "req", Descriptor: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.AnswerQuestion(ctx, r.ID, "answered", "answer", json.RawMessage(`{"answers":[["yes"]]}`)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.QueueInbox(ctx, r.ID, "input", json.RawMessage(`{"text":"next"}`)); err != nil {
			t.Fatal(err)
		}
		q, claimed, err := s.TargetInbox(ctx, l, "answered", "input")
		if err != nil || q.Status != "answered" || len(claimed) != 0 {
			t.Fatal("answered question consumed later directions")
		}
		stored, err := s.GetInbox(ctx, r.ID, "input")
		if err != nil || strings.Contains(string(stored.Payload), "targetQuestionId") {
			t.Fatal("untargeted input mutated")
		}
	})
	t.Run("origin secrets rejected", func(t *testing.T) {
		s := setup(t)
		_, _, err := s.Create(ctx, Run{ConversationID: conversation(t, s), InvocationID: "secret", Kind: "generated", Origin: json.RawMessage(`{"nested":{"access_token":"do-not-store"}}`)})
		if err == nil {
			t.Fatal("credential accepted")
		}
	})
}

func TestSimultaneousCreateReturnsOneRun(t *testing.T) {
	s := sqliteStore(t)
	ctx := context.Background()
	r := Run{ConversationID: conversation(t, s), InvocationID: "same", Kind: "generated"}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); run, _, err := s.Create(ctx, r); ids <- run.ID; errs <- err }()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("duplicate runs %s %s", id, first)
		}
	}
}

func TestRunIdentifiersAreBounded(t *testing.T) {
	s := sqliteStore(t)
	_, _, err := s.Create(context.Background(), Run{ConversationID: conversation(t, s), InvocationID: fmt.Sprintf("%0256d", 1), Kind: "generated"})
	if err == nil {
		t.Fatal("unbounded identifier accepted")
	}
}

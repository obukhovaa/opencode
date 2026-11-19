package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/session"
)

type noBulkSessionReads struct{ session.Service }

func (*noBulkSessionReads) List(context.Context) ([]session.Session, error) {
	panic("bridge must not load all sessions")
}

func TestBridgeSessionPagingAcrossConnectors(t *testing.T) {
	svc, conn := newOrchestratorForTest(t)
	ctx := context.Background()
	now := time.Now().Unix()
	if _, err := conn.Exec(`UPDATE sessions SET created_at = ? WHERE id = 'S1'`, now-9999); err != nil {
		t.Fatal(err)
	}
	svc.app = &app.App{Sessions: &noBulkSessionReads{session.NewService(db.New(conn), "proj")}}
	for i := 0; i < 180; i++ {
		id := fmt.Sprintf("h%07d", i)
		if _, err := conn.Exec(`INSERT INTO sessions(id,project_id,root_session_id,title,created_at,updated_at)VALUES(?,'proj',?,'PRIVATE',?,?)`, id, id, now+int64(i+1000), now); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.store.UpsertBinding(ctx, store.Binding{ProjectID: "proj", Channel: "external", IdentityID: "c3", PeerID: id, SessionID: id}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 27; i++ {
		id := fmt.Sprintf("p%07d", i)
		if _, err := conn.Exec(`INSERT INTO sessions(id,project_id,root_session_id,title,created_at,updated_at)VALUES(?,'proj',?,?,?,?)`, id, id, fmt.Sprintf("Visible %02d", i), now-int64(i+100), now); err != nil {
			t.Fatal(err)
		}
	}
	for _, channel := range []string{"slack", "telegram", "mattermost", "external"} {
		t.Run(channel, func(t *testing.T) {
			peer := bridge.PeerRef{Channel: channel, Identity: "default", PeerID: "peer-" + channel}
			if _, err := svc.store.UpsertBinding(ctx, store.Binding{ProjectID: "proj", Channel: channel, IdentityID: "default", PeerID: peer.PeerID, SessionID: "S1"}); err != nil {
				t.Fatal(err)
			}
			if channel == "external" {
				off := false
				svc.cfg.IsolateExternalSessions = &off
				svc.cfg.Channels.External = &bridge.ExternalChannelConfig{Enabled: true, Commands: []string{"sessions", "session"}}
			}
			args := ""
			seen := map[string]bool{}
			pages := 0
			for {
				pages++
				if pages > 5 {
					t.Fatal("bridge cursor failed to advance")
				}
				reply := svc.handleChatCommand(ctx, bridge.Inbound{Peer: peer, Command: "sessions", CommandArgs: args})
				if reply == nil || reply.Hint == nil {
					t.Fatalf("missing table: %+v", reply)
				}
				if strings.Contains(reply.Text, "PRIVATE") {
					t.Fatal("private rows leaked")
				}
				for _, row := range reply.Hint.Rows {
					key := strings.TrimSpace(strings.TrimPrefix(row[0], "★ "))
					if seen[key] {
						t.Fatal("row repeated across pages", key)
					}
					seen[key] = true
				}
				marker := "More: `"
				at := strings.Index(reply.Text, marker)
				if at < 0 {
					break
				}
				if !strings.Contains(reply.Hint.Footer, "More: `") {
					t.Fatal("native table lost continuation command")
				}
				command := strings.SplitN(reply.Text[at+len(marker):], "`", 2)[0]
				args = strings.TrimPrefix(command, "/sessions ")
			}
			want := 28
			if channel == "external" {
				want = 1
			}
			if len(seen) != want {
				t.Fatalf("got %d visible sessions across %d pages, want %d", len(seen), pages, want)
			}
			if channel != "external" {
				reply := svc.handleChatCommand(ctx, bridge.Inbound{Peer: peer, Command: "sessions", CommandArgs: "Visible 26"})
				if reply == nil || reply.Hint == nil || len(reply.Hint.Rows) != 1 || !strings.Contains(reply.Text, "Visible 26") {
					t.Fatalf("server search failed: %+v", reply)
				}
			} else {
				reply := svc.handleChatCommand(ctx, bridge.Inbound{Peer: peer, Command: "sessions", CommandArgs: "--all Visible"})
				if reply == nil || strings.Contains(reply.Text, "Visible 26") {
					t.Fatal("external --all escaped its own session")
				}
			}
		})
	}
}

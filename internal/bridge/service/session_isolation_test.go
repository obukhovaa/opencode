package service

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/cron"
	opencodedb "github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/session"
)

// channelLookupStore counts ListSessionIDsByChannel calls and, when err is
// set, fails them; every other method passes through.
type channelLookupStore struct {
	store.Store
	calls atomic.Int32
	err   error
}

func (s *channelLookupStore) ListSessionIDsByChannel(ctx context.Context, projectID, channel string) ([]string, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return s.Store.ListSessionIDsByChannel(ctx, projectID, channel)
}

// countingSessions counts List and Get calls on the session service.
type countingSessions struct {
	session.Service
	calls atomic.Int32
}

func (c *countingSessions) List(ctx context.Context) ([]session.Session, error) {
	c.calls.Add(1)
	return c.Service.List(ctx)
}

func (c *countingSessions) Get(ctx context.Context, id string) (session.Session, error) {
	c.calls.Add(1)
	return c.Service.Get(ctx, id)
}

// stubCrons serves a fixed active-job list to /crons.
type stubCrons struct {
	cron.Service
	jobs []cron.CronJob
}

func (c *stubCrons) ListActive(context.Context) ([]cron.CronJob, error) { return c.jobs, nil }

type isolationSetup struct {
	isolate bool
	// currentExternal also binds the caller's own session S1 to an
	// external peer.
	currentExternal bool
	lookupErr       error
}

// newIsolationTestService builds a bridge whose chat peer slack:default:D1
// is bound to S1, next to two sessions bound to external conversations
// (ab01-ext "Tenant A plan", cd01-ext "Tenant D plan"), one session bound to
// another chat peer (ab02-chat "Chat B", its own root) and one unbound
// (ab03-chat "Chat C"). A flow run rooted at flowrun-1 has an interactive
// step bound to an external peer (ef01-step "Tenant E review"), an unbound
// sibling step (ef02-step "Tenant E draft") and, under that sibling, a
// subagent session (toolu-ef1) that /sessions does not list.
func newIsolationTestService(t *testing.T, tc isolationSetup) (*Service, *channelLookupStore) {
	t.Helper()
	svc, conn := newOrchestratorForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	svc.ctx, svc.cancel = ctx, cancel
	if _, err := conn.Exec(`
		INSERT INTO sessions (id, project_id, parent_session_id, root_session_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at)
		VALUES ('ab01-ext',  'proj', NULL,        'ab01-ext',  'Tenant A plan',   3, 0, 0, 0, strftime('%s','now'), strftime('%s','now')),
		       ('ab02-chat', 'proj', NULL,        'ab02-chat', 'Chat B',          2, 0, 0, 0, strftime('%s','now'), strftime('%s','now')),
		       ('ab03-chat', 'proj', NULL,        NULL,        'Chat C',          1, 0, 0, 0, strftime('%s','now'), strftime('%s','now')),
		       ('cd01-ext',  'proj', NULL,        NULL,        'Tenant D plan',   1, 0, 0, 0, strftime('%s','now'), strftime('%s','now')),
		       ('ef01-step', 'proj', NULL,        'flowrun-1', 'Tenant E review', 2, 0, 0, 0, strftime('%s','now'), strftime('%s','now')),
		       ('ef02-step', 'proj', NULL,        'flowrun-1', 'Tenant E draft',  4, 0, 0, 0, strftime('%s','now'), strftime('%s','now')),
		       ('toolu-ef1', 'proj', 'ef02-step', 'flowrun-1', 'Tenant E sub',    1, 0, 0, 0, strftime('%s','now'), strftime('%s','now'))
	`); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
	svc.app = &app.App{Sessions: session.NewService(opencodedb.New(conn), "proj")}
	bindings := []store.Binding{
		{Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "S1"},
		{Channel: "slack", IdentityID: "default", PeerID: "D9", SessionID: "ab02-chat"},
		{Channel: "external", IdentityID: "c3", PeerID: "aidA:flow:run1", SessionID: "ab01-ext"},
		{Channel: "external", IdentityID: "c3", PeerID: "aidD:flow:run1", SessionID: "cd01-ext"},
		{Channel: "external", IdentityID: "c3", PeerID: "aidE:flow:run1", SessionID: "ef01-step"},
	}
	if tc.currentExternal {
		bindings = append(bindings, store.Binding{Channel: "external", IdentityID: "c3", PeerID: "aidS:flow:run1", SessionID: "S1"})
	}
	for _, b := range bindings {
		b.ProjectID = "proj"
		if _, err := svc.store.UpsertBinding(context.Background(), b); err != nil {
			t.Fatalf("UpsertBinding %s: %v", b.PeerID, err)
		}
	}
	svc.cfg.IsolateExternalSessions = tc.isolate
	lookups := &channelLookupStore{Store: svc.store, err: tc.lookupErr}
	svc.store = lookups
	return svc, lookups
}

func assertReply(t *testing.T, reply *bridge.CommandReply, want, wantNot []string) {
	t.Helper()
	if reply == nil {
		t.Fatal("nil reply")
	}
	for _, w := range want {
		if !strings.Contains(reply.Text, w) {
			t.Errorf("reply lacks %q:\n%s", w, reply.Text)
		}
	}
	for _, w := range wantNot {
		if strings.Contains(reply.Text, w) {
			t.Errorf("reply leaks %q:\n%s", w, reply.Text)
		}
		if reply.Hint != nil {
			for _, row := range reply.Hint.Rows {
				for _, cell := range row {
					if strings.Contains(cell, w) {
						t.Errorf("rendered table leaks %q: %v", w, row)
					}
				}
			}
		}
	}
}

func TestCmdSessionsIsolatesExternalSessions(t *testing.T) {
	tests := []struct {
		name        string
		setup       isolationSetup
		want        []string
		wantNot     []string
		wantLookups int32
	}{
		{
			name:        "off lists external-bound sessions and their run",
			want:        []string{"★ S1", "Tenant A plan", "Tenant D plan", "Tenant E review", "Tenant E draft", "Chat B", "Chat C"},
			wantLookups: 0,
		},
		{
			name:        "on hides external-bound sessions and their run's siblings",
			setup:       isolationSetup{isolate: true},
			want:        []string{"★ S1", "Chat B", "Chat C"},
			wantNot:     []string{"Tenant A plan", "Tenant D plan", "Tenant E review", "Tenant E draft", "ab01-ext", "cd01-ext", "ef01-ste", "ef02-ste"},
			wantLookups: 1,
		},
		{
			name:        "on keeps the current session though external-bound",
			setup:       isolationSetup{isolate: true, currentExternal: true},
			want:        []string{"★ S1", "Chat B"},
			wantNot:     []string{"Tenant A plan", "Tenant D plan"},
			wantLookups: 1,
		},
		{
			name:        "lookup error fails closed to the current session",
			setup:       isolationSetup{isolate: true, lookupErr: errors.New("db down")},
			want:        []string{"★ S1"},
			wantNot:     []string{"Tenant A plan", "Tenant D plan", "Chat B", "Chat C"},
			wantLookups: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, lookups := newIsolationTestService(t, tt.setup)
			reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerD1(), Command: "sessions"})
			assertReply(t, reply, tt.want, tt.wantNot)
			if got := lookups.calls.Load(); got != tt.wantLookups {
				t.Errorf("external-session lookups = %d, want %d", got, tt.wantLookups)
			}
		})
	}
}

func TestCmdSessionSwitchIsolatesExternalSessions(t *testing.T) {
	tests := []struct {
		name      string
		setup     isolationSetup
		args      string
		want      []string
		wantNot   []string
		wantBound string
	}{
		{
			name:      "off switches into an external-bound session",
			args:      "cd",
			want:      []string{"Switched to session cd01-ext"},
			wantBound: "cd01-ext",
		},
		{
			name:      "on refuses with the ordinary no-match reply",
			setup:     isolationSetup{isolate: true},
			args:      "cd",
			want:      []string{`No session matches prefix "cd"`},
			wantNot:   []string{"Tenant D plan", "cd01-ext"},
			wantBound: "S1",
		},
		{
			name:      "on refuses the full id too",
			setup:     isolationSetup{isolate: true},
			args:      "ab01-ext",
			want:      []string{`No session matches prefix "ab01-ext"`},
			wantNot:   []string{"Tenant A plan"},
			wantBound: "S1",
		},
		{
			name:      "off switches into an external run's unbound step",
			args:      "ef02",
			want:      []string{"Switched to session ef02-ste · Tenant E draft"},
			wantBound: "ef02-step",
		},
		{
			name:      "on refuses an external run's unbound step",
			setup:     isolationSetup{isolate: true},
			args:      "ef02",
			want:      []string{`No session matches prefix "ef02"`},
			wantNot:   []string{"Tenant E draft"},
			wantBound: "S1",
		},
		{
			name:      "on still switches into a chat session",
			setup:     isolationSetup{isolate: true},
			args:      "ab02",
			want:      []string{"Switched to session ab02-cha · Chat B"},
			wantBound: "ab02-chat",
		},
		{
			name:      "off ambiguous listing names it",
			args:      "ab",
			want:      []string{"3 matches", "Tenant A plan", "Chat B", "Chat C"},
			wantBound: "S1",
		},
		{
			name:      "on ambiguous listing leaves it out",
			setup:     isolationSetup{isolate: true},
			args:      "ab",
			want:      []string{"2 matches", "Chat B", "Chat C"},
			wantNot:   []string{"Tenant A plan", "ab01-ext"},
			wantBound: "S1",
		},
		{
			name:      "on keeps the current session matchable though external-bound",
			setup:     isolationSetup{isolate: true, currentExternal: true},
			args:      "S1",
			want:      []string{"Already on session S1"},
			wantBound: "S1",
		},
		{
			name:      "lookup error fails closed",
			setup:     isolationSetup{isolate: true, lookupErr: errors.New("db down")},
			args:      "ab02",
			want:      []string{`No session matches prefix "ab02"`},
			wantNot:   []string{"Chat B"},
			wantBound: "S1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newIsolationTestService(t, tt.setup)
			reply := svc.handleChatCommand(context.Background(),
				bridge.Inbound{Peer: peerD1(), Command: "session", CommandArgs: tt.args})
			assertReply(t, reply, tt.want, tt.wantNot)
			b, err := svc.store.GetBinding(context.Background(), "proj", "slack", "default", "D1")
			if err != nil {
				t.Fatalf("GetBinding: %v", err)
			}
			if b.SessionID != tt.wantBound {
				t.Errorf("D1 bound to %q, want %q", b.SessionID, tt.wantBound)
			}
		})
	}
}

func TestCmdCronsIsolatesExternalSessions(t *testing.T) {
	jobs := []cron.CronJob{
		{ID: "job-own1", SessionID: "S1", TaskTitle: "own check", Schedule: "*/5 * * * *", Source: "agent"},
		{ID: "job-ext1", SessionID: "ab01-ext", TaskTitle: "tenant A digest", Schedule: "*/5 * * * *", Source: "agent"},
		{ID: "job-cht1", SessionID: "ab02-chat", TaskTitle: "chat B digest", Schedule: "*/5 * * * *", Source: "agent"},
		{ID: "job-sib1", SessionID: "ef02-step", TaskTitle: "tenant E sweep", Schedule: "*/5 * * * *", Source: "agent"},
		{ID: "job-sub1", SessionID: "toolu-ef1", TaskTitle: "tenant E subtask", Schedule: "*/5 * * * *", Source: "agent"},
		{ID: "job-gon1", SessionID: "gone-123", TaskTitle: "orphan job", Schedule: "*/5 * * * *", Source: "agent"},
	}
	tests := []struct {
		name             string
		setup            isolationSetup
		jobs             []cron.CronJob
		want             []string
		wantNot          []string
		wantNoSessionUse bool
	}{
		{
			name:             "off lists every job without reading sessions",
			jobs:             jobs,
			want:             []string{"own check", "tenant A digest", "chat B digest", "tenant E sweep", "tenant E subtask", "orphan job"},
			wantNoSessionUse: true,
		},
		{
			name:    "on leaves out jobs of an external conversation's run",
			setup:   isolationSetup{isolate: true},
			jobs:    jobs,
			want:    []string{"own check", "chat B digest"},
			wantNot: []string{"tenant A digest", "ab01-ext", "tenant E sweep", "tenant E subtask", "orphan job"},
		},
		{
			name:    "on with only external jobs reads as none",
			setup:   isolationSetup{isolate: true},
			jobs:    jobs[1:2],
			want:    []string{"No active scheduled cron jobs."},
			wantNot: []string{"tenant A digest"},
		},
		{
			name:    "on keeps the current session's jobs though external-bound",
			setup:   isolationSetup{isolate: true, currentExternal: true},
			jobs:    jobs,
			want:    []string{"own check", "chat B digest"},
			wantNot: []string{"tenant A digest", "tenant E sweep"},
		},
		{
			name:    "lookup error fails closed to the current session",
			setup:   isolationSetup{isolate: true, lookupErr: errors.New("db down")},
			jobs:    jobs,
			want:    []string{"own check"},
			wantNot: []string{"tenant A digest", "chat B digest", "tenant E sweep"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newIsolationTestService(t, tt.setup)
			svc.app.Crons = &stubCrons{jobs: tt.jobs}
			// Count only the command's own reads: the binding lookup
			// already happened while seeding.
			sessions := &countingSessions{Service: svc.app.Sessions}
			svc.app.Sessions = sessions
			reply := svc.handleChatCommand(context.Background(), bridge.Inbound{Peer: peerD1(), Command: "crons"})
			assertReply(t, reply, tt.want, tt.wantNot)
			if tt.wantNoSessionUse && sessions.calls.Load() != 0 {
				t.Errorf("session reads = %d with the setting off, want 0", sessions.calls.Load())
			}
		})
	}
}

func TestExternalSessionsIsolatedDefaultsOff(t *testing.T) {
	tests := []struct {
		name string
		cfg  *bridge.Config
		want bool
	}{
		{"nil config", nil, false},
		{"unset", &bridge.Config{}, false},
		{"on", &bridge.Config{IsolateExternalSessions: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ExternalSessionsIsolated(); got != tt.want {
				t.Errorf("ExternalSessionsIsolated() = %v, want %v", got, tt.want)
			}
		})
	}
}

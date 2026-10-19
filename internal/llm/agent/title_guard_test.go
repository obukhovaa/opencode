package agent

import (
	"context"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/session"
)

// titleProviderSpy embeds the shared stubProvider and records SendMessages
// calls so we can assert title generation is (not) invoked.
type titleProviderSpy struct {
	*stubProvider
	sendCalls int
}

func (s *titleProviderSpy) SendMessages(_ context.Context, _ []message.Message, _ []tools.BaseTool) (*provider.ProviderResponse, error) {
	s.sendCalls++
	return &provider.ProviderResponse{Content: "generated title"}, nil
}

// TestGenerateTitleSkipsUserTitledSession verifies that once a session is
// user-titled, generateTitle returns without calling the title model — the
// early guard that avoids a wasted descriptor request. (The race-safe DB
// no-op is covered by the session package's SetGeneratedTitle test.)
func TestGenerateTitleSkipsUserTitledSession(t *testing.T) {
	spy := &titleProviderSpy{stubProvider: &stubProvider{}}
	a := &agent{
		titleProvider: spy,
		sessions:      &stubSessionService{sess: session.Session{ID: "s1", UserSetTitle: true}},
	}

	if err := a.generateTitle(context.Background(), "s1", "some first message"); err != nil {
		t.Fatalf("generateTitle: %v", err)
	}
	if spy.sendCalls != 0 {
		t.Errorf("title model invoked %d times, want 0 for a user-titled session", spy.sendCalls)
	}
}

func TestTitleWanted(t *testing.T) {
	tests := []struct {
		name  string
		setup func(permission.Service)
		want  bool
	}{
		{name: "attended session (TUI, bridge, API)", setup: func(permission.Service) {}, want: true},
		{name: "flow step or headless run", setup: func(p permission.Service) { p.MarkUnattendedSession("s1") }, want: false},
		{name: "interactive flow step", setup: func(p permission.Service) {
			p.MarkUnattendedSession("s1")
			p.MarkInteractiveSession("s1")
		}, want: true},
		{name: "subagent of a flow step", setup: func(p permission.Service) {
			p.MarkUnattendedSession("parent")
			p.LinkSession("s1", "parent")
		}, want: false},
		{name: "cron subagent in a watched session", setup: func(p permission.Service) {
			// The scheduler clears the mark when the TUI or a chat binding
			// watches the job's session (cron/scheduler.go).
			p.MarkUnattendedSession("parent")
			p.RemoveUnattendedSession("parent")
			p.LinkSession("s1", "parent")
		}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			perms := permission.NewPermissionService()
			tt.setup(perms)
			a := &agent{permissions: perms}
			if got := a.titleWanted("s1"); got != tt.want {
				t.Errorf("titleWanted = %v, want %v", got, tt.want)
			}
		})
	}
	if !(&agent{}).titleWanted("s1") {
		t.Error("an agent without a permission service keeps title generation")
	}
}

// An unattended session's first turn makes no descriptor call.
func TestProcessGenerationSkipsTitleForUnattendedSession(t *testing.T) {
	withFreshTaskRegistry(t)
	a := newLoopAgent(t, &scriptedProvider{respond: func(int, bool) *provider.ProviderResponse { return endTurn() }})
	spy := &titleRequesterSpy{stubProvider: &stubProvider{}, got: make(chan string, 1)}
	a.titleProvider = spy
	perms := permission.NewPermissionService()
	perms.MarkUnattendedSession("sess-flow")
	a.permissions = perms

	if res := a.processGeneration(context.Background(), "sess-flow", "first message", 0, nil, RunOptions{NonInteractive: true}); res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	select {
	case <-spy.got:
		t.Fatal("title generated for an unattended session")
	case <-time.After(200 * time.Millisecond):
	}
}

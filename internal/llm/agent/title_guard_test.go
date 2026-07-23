package agent

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
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

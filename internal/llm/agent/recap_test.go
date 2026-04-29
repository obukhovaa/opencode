package agent

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
)

// stubMessageService implements message.Service for recap tests.
type stubMessageService struct {
	message.Service
	msgs []message.Message
	err  error
}

func (s *stubMessageService) ListLatest(_ context.Context, _ string, _ int64) ([]message.Message, error) {
	return s.msgs, s.err
}

// stubSessionService implements session.Service for recap tests.
type stubSessionService struct {
	session.Service
	sess session.Session
	err  error
}

func (s *stubSessionService) Get(_ context.Context, _ string) (session.Session, error) {
	return s.sess, s.err
}

// stubProvider implements provider.Provider for recap tests.
// It counts invocations of StreamResponse.
type stubProvider struct {
	streamCalls int
}

func (s *stubProvider) StreamResponse(_ context.Context, _ []message.Message, _ []tools.BaseTool) <-chan provider.ProviderEvent {
	s.streamCalls++
	ch := make(chan provider.ProviderEvent, 1)
	ch <- provider.ProviderEvent{
		Type: provider.EventComplete,
		Response: &provider.ProviderResponse{
			Content: "stub-recap",
		},
	}
	close(ch)
	return ch
}

func (s *stubProvider) SendMessages(_ context.Context, _ []message.Message, _ []tools.BaseTool) (*provider.ProviderResponse, error) {
	return &provider.ProviderResponse{}, nil
}

func (s *stubProvider) Model() models.Model {
	return models.Model{}
}

func (s *stubProvider) CountTokens(_ context.Context, _ float64, _ []message.Message, _ []tools.BaseTool) (int64, bool) {
	return 0, false
}

func (s *stubProvider) AdjustMaxTokens(_ int64) int64 {
	return 0
}

func makeMessages(n int) []message.Message {
	msgs := make([]message.Message, n)
	for i := range msgs {
		msgs[i] = message.Message{Role: message.User}
	}
	return msgs
}

func TestGenerateRecap_SkipsForSmallSessions(t *testing.T) {
	tests := []struct {
		name             string
		msgs             []message.Message
		promptTokens     int64
		completionTokens int64
		wantResult       string
		wantProviderCall bool
	}{
		{
			name:             "too few messages (4 msgs)",
			msgs:             makeMessages(4),
			promptTokens:     100000,
			completionTokens: 100000,
			wantResult:       "",
			wantProviderCall: false,
		},
		{
			name:             "zero messages",
			msgs:             makeMessages(0),
			promptTokens:     100000,
			completionTokens: 100000,
			wantResult:       "",
			wantProviderCall: false,
		},
		{
			name:             "enough messages but few tokens (5 msgs, 1000 total)",
			msgs:             makeMessages(5),
			promptTokens:     500,
			completionTokens: 500,
			wantResult:       "",
			wantProviderCall: false,
		},
		{
			name:             "enough messages and tokens unrecorded (5 msgs, 0 total)",
			msgs:             makeMessages(5),
			promptTokens:     0,
			completionTokens: 0,
			wantResult:       "stub-recap",
			wantProviderCall: true,
		},
		{
			name:             "enough messages and enough tokens",
			msgs:             makeMessages(5),
			promptTokens:     4000,
			completionTokens: 4000,
			wantResult:       "stub-recap",
			wantProviderCall: true,
		},
		{
			name:             "boundary: exactly 5 messages, exactly 8000 tokens",
			msgs:             makeMessages(5),
			promptTokens:     4000,
			completionTokens: 4000,
			wantResult:       "stub-recap",
			wantProviderCall: true,
		},
		{
			name:             "boundary: 5 messages, 7999 tokens",
			msgs:             makeMessages(5),
			promptTokens:     3999,
			completionTokens: 4000,
			wantResult:       "",
			wantProviderCall: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubProvider{}
			a := &agent{
				Broker:            pubsub.NewBroker[AgentEvent](),
				summarizeProvider: stub,
				messages:          &stubMessageService{msgs: tc.msgs},
				sessions: &stubSessionService{sess: session.Session{
					TotalPromptTokens:     tc.promptTokens,
					TotalCompletionTokens: tc.completionTokens,
				}},
			}

			got, err := a.GenerateRecap(context.Background(), "test-session")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantResult {
				t.Errorf("result = %q, want %q", got, tc.wantResult)
			}

			if tc.wantProviderCall && stub.streamCalls == 0 {
				t.Error("expected StreamResponse to be called, but it was not")
			}
			if !tc.wantProviderCall && stub.streamCalls > 0 {
				t.Errorf("expected StreamResponse NOT to be called, but it was called %d time(s)", stub.streamCalls)
			}
		})
	}
}

func TestGenerateRecap_NoSummarizeProvider(t *testing.T) {
	a := &agent{
		Broker:            pubsub.NewBroker[AgentEvent](),
		summarizeProvider: nil,
	}
	_, err := a.GenerateRecap(context.Background(), "test-session")
	if err == nil {
		t.Fatal("expected error when summarizeProvider is nil, got nil")
	}
}

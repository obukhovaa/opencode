package agent

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

var errInternal = errors.New("500 internal server error")

// err429Retried is what the Anthropic client returns once its retry budget on
// a 429 ran out.
var err429Retried = errors.New("maximum retry attempts reached for HTTP 429: 8 retries")

var errRSTStream = errors.New("stream error: stream ID 147; INTERNAL_ERROR; received from peer")

// clientTimeout is net/http's Client.Timeout error underneath a *url.Error.
type clientTimeout struct{}

func (clientTimeout) Error() string {
	return "net/http: request canceled (Client.Timeout exceeded while awaiting headers)"
}
func (clientTimeout) Timeout() bool   { return true }
func (clientTimeout) Temporary() bool { return true }

var errClientTimeout error = &url.Error{Op: "Post", URL: "https://api.anthropic.com/v1/messages", Err: clientTimeout{}}

var errPromptTooLong = errors.New(`POST "https://api.anthropic.com/v1/messages": 400 Bad Request {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 212000 tokens > 200000 maximum"}}`)

// overflowingProvider is the main model: failWhen picks, per request, the
// stream error it ends in (nil for a normal response).
type overflowingProvider struct {
	*compactionProvider
	failWhen func(msgs []message.Message) error
}

func (p *overflowingProvider) StreamResponse(ctx context.Context, msgs []message.Message, ts []tools.BaseTool) <-chan provider.ProviderEvent {
	p.fail = p.failWhen(msgs)
	return p.compactionProvider.StreamResponse(ctx, msgs, ts)
}

func headedBySummary(msgs []message.Message) bool {
	return len(msgs) > 0 && firstText(msgs[0]) == testSummaryText
}

// newOverflowAgent: a session with some history and a report well under the
// threshold, so only the overflow path can compact.
func newOverflowAgent(t *testing.T, sessionID string, failWhen func([]message.Message) error, ts ...tools.BaseTool) (*agent, *compactionProvider, *[]string) {
	t.Helper()
	withFreshTaskRegistry(t)
	a, main, summarizer, log := newCompactingAgent(t, 100, ts)
	a.provider = &overflowingProvider{compactionProvider: main, failWhen: failWhen}
	seedHistory(t, a, sessionID, textMsg(message.User, "earlier task"), textMsg(message.Assistant, "earlier answer"))
	seedReport(t, a, sessionID, 100)
	return a, summarizer, log
}

func countOf(log []string, name string) int {
	n := 0
	for _, l := range log {
		if l == name {
			n++
		}
	}
	return n
}

// Overflow -> one compaction -> the same turn succeeds on the compacted history.
func TestOverflowRetry_CompactsAndRetriesOnce(t *testing.T) {
	withAutoCompact(t, true)
	const sess = "s-overflow-ok"
	a, _, log := newOverflowAgent(t, sess, func(msgs []message.Message) error {
		if headedBySummary(msgs) {
			return nil
		}
		return errPromptTooLong
	})
	main := a.provider.(*overflowingProvider)

	res := a.processGeneration(context.Background(), sess, "next task", 0, nil, RunOptions{})

	if res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	if got := strings.Join(*log, ","); got != "main,summarizer,main" {
		t.Fatalf("call order = %s, want main,summarizer,main", got)
	}
	retry := main.requests[1]
	if !headedBySummary(retry) {
		t.Fatal("the retry was not sent the compacted history")
	}
	if last := retry[len(retry)-1]; last.Role != message.User || firstText(last) != "next task" {
		t.Errorf("the retry must end with the turn's user message, got %+v", last)
	}
	sessAfter, _ := a.sessions.Get(context.Background(), sess)
	if sessAfter.SummaryMessageID == "" {
		t.Error("no summary recorded")
	}
}

// An overflow on the call after a tool turn keeps that turn's call and
// result past the summary, so the retried call still sees what it reacts to.
func TestOverflowRetry_AfterToolTurnKeepsThePair(t *testing.T) {
	withAutoCompact(t, true)
	const sess = "s-overflow-tool"
	calls := 0
	a, _, log := newOverflowAgent(t, sess, func(msgs []message.Message) error {
		if calls == 1 && !headedBySummary(msgs) {
			return errPromptTooLong
		}
		return nil
	}, noopTool{})
	main := a.provider.(*overflowingProvider)
	main.respond = func([]message.Message) *provider.ProviderResponse {
		calls++
		if calls == 1 {
			return toolTurn(call("noop", 1))
		}
		return &provider.ProviderResponse{Content: "done", FinishReason: message.FinishReasonEndTurn}
	}

	res := a.processGeneration(context.Background(), sess, "next task", 0, nil, RunOptions{})

	if res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	if got := strings.Join(*log, ","); got != "main,main,summarizer,main" {
		t.Fatalf("call order = %s", got)
	}
	retry := main.requests[2]
	if !headedBySummary(retry) {
		t.Fatal("the retry was not sent the compacted history")
	}
	tail := retry[len(retry)-2:]
	if len(tail[0].ToolCalls()) != 1 || tail[0].ToolCalls()[0].ID != "noop-1" || len(tail[1].ToolResults()) != 1 {
		t.Fatalf("the retry must end with the tool turn's call and result, got %+v", tail)
	}
}

func TestOverflowRetry_NoRecovery(t *testing.T) {
	tests := []struct {
		name           string
		autoCompact    bool
		failWhen       func([]message.Message) error
		summarizerFail error
		backoff        bool
		wantLog        string
	}{
		{
			name:        "overflow again after compaction returns the original error",
			autoCompact: true,
			failWhen:    func([]message.Message) error { return errPromptTooLong },
			wantLog:     "main,summarizer,main",
		},
		{
			name:           "failed compaction returns the original error",
			autoCompact:    true,
			failWhen:       func([]message.Message) error { return errPromptTooLong },
			summarizerFail: errors.New("summarizer down"),
			wantLog:        "main,summarizer",
		},
		{
			name:        "auto-compaction off",
			autoCompact: false,
			failWhen:    func([]message.Message) error { return errPromptTooLong },
			wantLog:     "main",
		},
		{
			name:        "backoff after a failed compaction",
			autoCompact: true,
			failWhen:    func([]message.Message) error { return errPromptTooLong },
			backoff:     true,
			wantLog:     "main",
		},
		{
			name:        "an error that is not an overflow",
			autoCompact: true,
			failWhen:    func([]message.Message) error { return errInternal },
			wantLog:     "main",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withAutoCompact(t, tt.autoCompact)
			const sess = "s-overflow-fail"
			a, summarizer, log := newOverflowAgent(t, sess, tt.failWhen)
			summarizer.fail = tt.summarizerFail
			if tt.backoff {
				a.compactionBackoff.failed(sess, 1_000_000)
			}

			res := a.processGeneration(context.Background(), sess, "next task", 0, nil, RunOptions{})

			if got := strings.Join(*log, ","); got != tt.wantLog {
				t.Fatalf("call order = %s, want %s", got, tt.wantLog)
			}
			want := tt.failWhen(nil)
			if res.Error == nil || !errors.Is(res.Error, want) {
				t.Fatalf("error = %v, want the original %v", res.Error, want)
			}
		})
	}
}

// Near the window, only an unspecific failure is read as an overflow: an
// error the provider already retried as transient, or a timeout, names its own
// cause and leaves the session uncompacted. A provider's overflow message
// recovers at any context size.
func TestOverflowRetry_NearWindowSkipsRetriedTransientErrors(t *testing.T) {
	tests := []struct {
		name    string
		report  int64
		err     error
		wantLog string
	}{
		{name: "429 after retries at 92%", report: 920, err: err429Retried, wantLog: "main"},
		{name: "http client timeout at 92%", report: 920, err: errClientTimeout, wantLog: "main"},
		{name: "stalled stream at 92%", report: 920, err: provider.ErrStreamStalled, wantLog: "main"},
		{name: "peer stream reset at 92% still recovers", report: 920, err: errRSTStream, wantLog: "main,summarizer,main"},
		{name: "overflow marker at 10% recovers", report: 100, err: errPromptTooLong, wantLog: "main,summarizer,main"},
		{name: "overflow marker at 92% recovers", report: 920, err: errPromptTooLong, wantLog: "main,summarizer,main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withAutoCompact(t, true)
			const sess = "s-overflow-transient"
			a, _, log := newOverflowAgent(t, sess, func(msgs []message.Message) error {
				if headedBySummary(msgs) {
					return nil
				}
				return tt.err
			})
			seedReport(t, a, sess, tt.report)

			res := a.processGeneration(context.Background(), sess, "next task", 0, nil, RunOptions{})

			if got := strings.Join(*log, ","); got != tt.wantLog {
				t.Fatalf("call order = %s, want %s", got, tt.wantLog)
			}
			recovered := tt.wantLog != "main"
			if recovered && res.Error != nil {
				t.Fatalf("processGeneration: %v", res.Error)
			}
			if !recovered && (res.Error == nil || !errors.Is(res.Error, tt.err)) {
				t.Fatalf("error = %v, want the original %v", res.Error, tt.err)
			}
		})
	}
}

// toolMsgFailingMessages fails saving the turn's tool results, an error that
// comes after the tools ran.
type toolMsgFailingMessages struct {
	*memMessages
}

func (m *toolMsgFailingMessages) Create(ctx context.Context, sessionID string, params message.CreateMessageParams) (message.Message, error) {
	if params.Role == message.Tool {
		return message.Message{}, errPromptTooLong
	}
	return m.memMessages.Create(ctx, sessionID, params)
}

// Only a provider stream error is retried: an error after the turn's tools
// ran must not send the turn again, or the tools would run twice.
func TestOverflowRetry_NotAfterToolsRan(t *testing.T) {
	withAutoCompact(t, true)
	const sess = "s-overflow-after-tools"
	ran := 0
	counting := &countingTool{run: func() { ran++ }}
	a, _, log := newOverflowAgent(t, sess, func([]message.Message) error { return nil }, counting)
	main := a.provider.(*overflowingProvider)
	main.respond = func([]message.Message) *provider.ProviderResponse { return toolTurn(call("count", 1)) }
	a.messages = &toolMsgFailingMessages{memMessages: a.messages.(*memMessages)}

	res := a.processGeneration(context.Background(), sess, "next task", 0, nil, RunOptions{})

	if res.Error == nil {
		t.Fatal("want the save error")
	}
	if got := strings.Join(*log, ","); got != "main" {
		t.Fatalf("call order = %s, want main only", got)
	}
	if ran != 1 {
		t.Errorf("tool ran %d times, want 1", ran)
	}
}

// expiringCtx reports DeadlineExceeded once expire is called, as a flow
// step's timeout does while its model call is in flight.
type expiringCtx struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func newExpiringCtx() *expiringCtx {
	return &expiringCtx{Context: context.Background(), done: make(chan struct{})}
}

func (c *expiringCtx) expire()               { c.once.Do(func() { close(c.done) }) }
func (c *expiringCtx) Done() <-chan struct{} { return c.done }
func (c *expiringCtx) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// A call that failed because the run ran out of time is no overflow, even
// near the window: the compaction would run on a dead context (and record a
// backoff failure for the session) and the retry could not be sent.
func TestOverflowRetry_NotOnceTheRunIsOutOfTime(t *testing.T) {
	withAutoCompact(t, true)
	const sess = "s-overflow-deadline"
	ctx := newExpiringCtx()
	a, _, log := newOverflowAgent(t, sess, func([]message.Message) error {
		ctx.expire()
		return errInternal
	})
	// 92% of the window: a near_window cause, under the 95% threshold.
	seedReport(t, a, sess, 920)

	res := a.processGeneration(ctx, sess, "next task", 0, nil, RunOptions{})

	if got := strings.Join(*log, ","); got != "main" {
		t.Fatalf("call order = %s, want main only", got)
	}
	if res.Error == nil || !errors.Is(res.Error, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the deadline", res.Error)
	}
	if n := a.compactionBackoff.failureCount(sess); n != 0 {
		t.Errorf("backoff failures = %d, want 0", n)
	}
}

type countingTool struct{ run func() }

func (*countingTool) Info() tools.ToolInfo {
	return tools.ToolInfo{Name: "count", Description: "count"}
}
func (c *countingTool) Run(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
	c.run()
	return tools.NewTextResponse("counted"), nil
}
func (*countingTool) AllowParallelism(tools.ToolCall, []tools.ToolCall) bool { return false }
func (*countingTool) IsBaseline() bool                                       { return true }

func TestContextOverflowCause(t *testing.T) {
	a := &agent{provider: &compactionProvider{name: "main", window: 1000, log: &[]string{}}}
	tests := []struct {
		name      string
		estimated int64
		err       error
		want      string
	}{
		{name: "anthropic", estimated: 10, err: errPromptTooLong, want: "provider_error"},
		{name: "bedrock", estimated: 10, err: errors.New("ValidationException: Input is too long for requested model."), want: "provider_error"},
		{name: "max_tokens over the limit", estimated: 10, err: errors.New("input length and `max_tokens` exceed context limit: 190000 + 32000 > 200000"), want: "provider_error"},
		{name: "openai", estimated: 10, err: errors.New(`{"code":"context_length_exceeded"}`), want: "provider_error"},
		{name: "gemini", estimated: 10, err: errors.New("The input token count (1200000) exceeds the maximum number of tokens allowed (1048576)."), want: "provider_error"},
		{name: "litellm", estimated: 10, err: errors.New(`POST "http://litellm/chat/completions": 400 Bad Request {"error":{"message":"litellm.ContextWindowExceededError: litellm.BadRequestError: MoonshotException - Invalid request","code":"400"}}`), want: "provider_error"},
		{name: "kimi", estimated: 10, err: errors.New(`{"error":{"message":"Invalid request: Your request exceeded model token limit: 262144","type":"invalid_request_error"}}`), want: "provider_error"},
		{name: "openai responses", estimated: 10, err: errors.New("Your input exceeds the context window of this model. Please adjust your input and try again."), want: "provider_error"},
		{name: "stream reset near the window", estimated: 950, err: errors.New("stream reset"), want: "near_window"},
		{name: "peer RST_STREAM near the window", estimated: 950, err: errRSTStream, want: "near_window"},
		{name: "rate limit far from the window", estimated: 100, err: errors.New("429 rate limited"), want: ""},
		{name: "429 after retries near the window", estimated: 950, err: err429Retried, want: ""},
		{name: "529 after retries near the window", estimated: 950, err: errors.New("maximum retry attempts reached for HTTP 529: 8 retries"), want: ""},
		{name: "overloaded near the window", estimated: 950, err: errors.New(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), want: ""},
		{name: "bedrock throttling near the window", estimated: 950, err: errors.New("received exception ThrottlingException: Too many tokens, please wait"), want: ""},
		{name: "http client timeout near the window", estimated: 950, err: errClientTimeout, want: ""},
		{name: "stalled stream near the window", estimated: 950, err: provider.ErrStreamStalled, want: ""},
		{name: "overflow marker wins over a transient signature", estimated: 10, err: errors.New("maximum retry attempts reached: prompt is too long"), want: "provider_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := a.contextOverflowCause(tt.estimated, tt.err); got != tt.want {
				t.Errorf("cause = %q, want %q", got, tt.want)
			}
		})
	}
}

var _ provider.Provider = (*overflowingProvider)(nil)

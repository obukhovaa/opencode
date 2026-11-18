package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/inbox"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

// recoveryFailureProvider records complete requests using compactionProvider,
// but can fail selected sends after streaming an unfinished tool call.
type recoveryFailureProvider struct {
	*compactionProvider
	failures      map[int]error
	beforeFailure func(int)
	partialTools  bool
	partialID     string
}

func (p *recoveryFailureProvider) StreamResponse(ctx context.Context, msgs []message.Message, ts []tools.BaseTool) <-chan provider.ProviderEvent {
	n := len(p.requests) + 1
	p.fail = p.failures[n]
	if p.fail != nil && p.beforeFailure != nil {
		p.beforeFailure(n)
	}
	inner := p.compactionProvider.StreamResponse(ctx, msgs, ts)
	if p.fail == nil || !p.partialTools {
		return inner
	}
	p.partialID = fmt.Sprintf("unfinished-%d", n)
	out := make(chan provider.ProviderEvent, 2)
	out <- provider.ProviderEvent{Type: provider.EventToolUseStart, ToolCall: &message.ToolCall{
		ID: p.partialID, Name: "count", Input: `{"incomplete":`,
	}}
	for event := range inner {
		out <- event
	}
	close(out)
	return out
}

// The preserved result alone exceeds the main model's window, so threshold
// compaction can legitimately be followed by an overflow-recovery compaction.
type largeRecoveryResultTool struct{ *countingTool }

func (t largeRecoveryResultTool) Run(ctx context.Context, call tools.ToolCall) (tools.ToolResponse, error) {
	if _, err := t.countingTool.Run(ctx, call); err != nil {
		return tools.ToolResponse{}, err
	}
	return tools.NewTextResponse(strings.Repeat("large tool output\n", 400)), nil
}

func TestProcessGeneration_DoubleCompactionDoesNotReplayDeliveredInput(t *testing.T) {
	for _, content := range []string{"", "continue the task"} {
		name := "auto resume"
		if content != "" {
			name = "user turn"
		}
		t.Run(name, func(t *testing.T) {
			withAutoCompact(t, true)
			withFreshTaskRegistry(t)
			const sess = "double-compaction-inbox"
			acked := useSessionInbox(t, sess)
			executions := 0
			a, main, summary, _ := newCompactingAgent(t, 980, []tools.BaseTool{
				largeRecoveryResultTool{&countingTool{run: func() { executions++ }}},
			})
			p := &recoveryFailureProvider{compactionProvider: main, failures: map[int]error{2: errPromptTooLong}}
			a.provider = p
			const input = "Handle this completion once"
			main.respond = func(msgs []message.Message) *provider.ProviderResponse {
				// A stale completion/instruction without the result of handling it
				// would cause the model to repeat the completed action.
				instruction, handled := false, false
				for _, m := range msgs {
					instruction = instruction || m.Content().String() == input
					for _, r := range m.ToolResults() {
						handled = handled || r.ToolCallID == "handled-report"
					}
				}
				if (instruction || hasToolResult(msgs, "BACKGROUND COMPLETION")) && !handled {
					return countToolTurn("handled-report")
				}
				return endTurn()
			}
			summary.count = func([]message.Message) int64 { return 50 }
			seedHistory(t, a, sess, completionPair("initial-report", "BACKGROUND COMPLETION")...)
			seedReport(t, a, sess, 100)
			inbox.Default().Push(sess, nil, inbox.Item{ID: "initial", Text: input})

			res := a.processGeneration(context.Background(), sess, content, 0, nil, RunOptions{})
			if res.Error != nil {
				t.Fatal(res.Error)
			}
			if executions != 1 {
				t.Errorf("completed action executed %d times, want once", executions)
			}
			if len(main.requests) != 3 || len(summary.requests) != 2 {
				t.Fatalf("model calls=%d compactions=%d, want 3/2", len(main.requests), len(summary.requests))
			}
			for i, req := range main.requests[1:] {
				if !headedBySummary(req) {
					t.Errorf("request %d did not compact", i+2)
				}
				calls, results := 0, 0
				for _, m := range req {
					if m.Content().String() == input {
						t.Errorf("request %d replayed consumed inbox input", i+2)
					}
					for _, call := range m.ToolCalls() {
						if call.ID == "handled-report" {
							calls++
						}
					}
					for _, result := range m.ToolResults() {
						if result.ToolCallID == "handled-report" {
							results++
						}
					}
				}
				if hasToolResult(req, "BACKGROUND COMPLETION") || calls != 1 || results != 1 {
					t.Errorf("request %d must preserve the completed action, not its consumed input: calls=%d results=%d", i+2, calls, results)
				}
			}
			if !reflect.DeepEqual(acked(), []string{"initial"}) {
				t.Errorf("delivery acknowledgements=%v, want [initial]", acked())
			}
		})
	}
}

func TestProcessGeneration_RecoveryKeepsUnconsumedInboxUntilAcceptedIteration(t *testing.T) {
	withAutoCompact(t, true)
	withFreshTaskRegistry(t)
	const sess = "unconsumed-inbox-recovery"
	acked := useSessionInbox(t, sess)
	a, main, summary, _ := newCompactingAgent(t, 100, nil)
	summary.count = func([]message.Message) int64 { return 50 }
	p := &recoveryFailureProvider{compactionProvider: main, failures: map[int]error{
		1: provider.ErrStreamStalled, 2: errPromptTooLong, 3: errInternal,
	}}
	a.provider = p
	p.beforeFailure = func(n int) {
		switch n {
		case 1:
			inbox.Default().Push(sess, nil, inbox.Item{ID: "stall", Text: "input during stall"})
		case 3:
			inbox.Default().Push(sess, nil, inbox.Item{ID: "pending", Text: "input during final failure"})
		}
	}
	seedHistory(t, a, sess, completionPair("initial-report", "UNCONSUMED COMPLETION")...)
	seedReport(t, a, sess, 100)
	inbox.Default().Push(sess, nil, inbox.Item{ID: "initial", Text: "input before run"})

	res := a.processGeneration(context.Background(), sess, "", 0, nil, RunOptions{})
	if res.Error == nil || len(main.requests) != 3 || len(summary.requests) != 1 {
		t.Fatalf("error=%v calls=%d compactions=%d", res.Error, len(main.requests), len(summary.requests))
	}
	retry := main.requests[2]
	if !headedBySummary(retry) || !hasToolResult(retry, "UNCONSUMED COMPLETION") {
		t.Fatal("overflow recovery lost the unconsumed completion")
	}
	if got := tail(retry, 2); !reflect.DeepEqual(got, []string{"user:input before run", "user:input during stall"}) {
		t.Fatalf("overflow recovery lost delivered input: %v", got)
	}
	if !reflect.DeepEqual(acked(), []string{"initial", "stall"}) || inbox.Default().Len(sess) != 1 {
		t.Fatalf("failed run acknowledged undelivered input: acks=%v pending=%d", acked(), inbox.Default().Len(sess))
	}

	res = a.processGeneration(context.Background(), sess, "continue", 0, nil, RunOptions{})
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if got := tail(main.requests[3], 1); !reflect.DeepEqual(got, []string{"user:input during final failure"}) {
		t.Fatalf("next run lost pending input: %v", got)
	}
	if !reflect.DeepEqual(acked(), []string{"initial", "stall", "pending"}) || inbox.Default().Len(sess) != 0 {
		t.Fatalf("next run delivery: acks=%v pending=%d", acked(), inbox.Default().Len(sess))
	}
}

func TestProcessGeneration_WrapUpFailuresClosePartialTools(t *testing.T) {
	for _, forced := range []bool{false, true} {
		for _, stallFirst := range []bool{false, true} {
			for _, failure := range []struct {
				name   string
				err    error
				finish message.FinishReason
			}{
				{"gateway error", errors.New("bad gateway"), message.FinishReasonError},
				{"peer reset", errRSTStream, message.FinishReasonError},
				{"deadline", context.DeadlineExceeded, message.FinishReasonError},
				{"cancelled", context.Canceled, message.FinishReasonCanceled},
				{"stall exhaustion", provider.ErrStreamStalled, message.FinishReasonError},
			} {
				t.Run(fmt.Sprintf("forced=%t/stallFirst=%t/%s", forced, stallFirst, failure.name), func(t *testing.T) {
					withAutoCompact(t, false)
					withFreshTaskRegistry(t)
					const sess = "wrap-up-partial-tools"
					acked := useSessionInbox(t, sess)
					executions := 0
					ts := []tools.BaseTool{&countingTool{run: func() { executions++ }}}
					if forced {
						ts = append(ts, testStructOutputTool())
					}
					a, main, _, _ := newCompactingAgent(t, 100, ts)
					p := &recoveryFailureProvider{compactionProvider: main, partialTools: true, failures: map[int]error{
						2: failure.err, 3: failure.err, 4: failure.err,
					}}
					if stallFirst {
						p.failures[2] = provider.ErrStreamStalled
					}
					a.provider = p
					main.respond = func([]message.Message) *provider.ProviderResponse {
						// Reopen the accepted output so maxTurns=1 forces wrap-up.
						inbox.Default().Push(sess, nil, inbox.Item{ID: "reopen", Text: "check again"})
						if forced {
							return structOutputCall("accepted", `{"status":"done"}`)
						}
						return endTurn()
					}
					wantSends := 2
					if stallFirst {
						wantSends = 3
					}
					if errors.Is(failure.err, provider.ErrStreamStalled) {
						wantSends = 4
					}
					p.beforeFailure = func(n int) {
						if n == wantSends {
							inbox.Default().Push(sess, nil, inbox.Item{ID: "pending", Text: "not delivered"})
						}
					}

					res := a.processGeneration(context.Background(), sess, "start", 1, nil, RunOptions{})
					if res.Error != nil || !res.Done || !res.TurnsExhausted || len(main.requests) != wantSends {
						t.Fatalf("error=%v done=%t exhausted=%t sends=%d, want %d", res.Error, res.Done, res.TurnsExhausted, len(main.requests), wantSends)
					}
					if forced && (res.StructOutput == nil || res.StructOutput.IsError || res.StructOutput.ToolCallID != "accepted") {
						t.Fatalf("accepted struct_output lost: %+v", res.StructOutput)
					}
					if !forced && res.StructOutput != nil {
						t.Fatalf("plain wrap-up produced structured output: %+v", res.StructOutput)
					}
					if executions != 0 {
						t.Errorf("executed %d unfinished tool calls", executions)
					}
					msgs, err := a.messages.List(context.Background(), sess)
					if err != nil {
						t.Fatal(err)
					}
					partialCalls, partialResults := 0, 0
					for _, m := range msgs {
						if m.Role == message.Assistant && !m.IsFinished() {
							t.Errorf("unfinished assistant message %s", m.ID)
						}
						for _, call := range m.ToolCalls() {
							if !strings.HasPrefix(call.ID, "unfinished-") {
								continue
							}
							partialCalls++
							if call.ID != p.partialID || m.FinishReason() != failure.finish {
								t.Errorf("partial call %s finished %s, want %s/%s", call.ID, m.FinishReason(), p.partialID, failure.finish)
							}
						}
						for _, r := range m.ToolResults() {
							if strings.HasPrefix(r.ToolCallID, "unfinished-") {
								partialResults++
								if r.ToolCallID != p.partialID || !r.IsError {
									t.Errorf("incorrect partial tool result: %+v", r)
								}
							}
						}
					}
					if partialCalls != 1 || partialResults != 1 {
						t.Errorf("persisted partial calls/results=%d/%d, want 1/1", partialCalls, partialResults)
					}
					if !reflect.DeepEqual(acked(), []string{"reopen"}) || inbox.Default().Len(sess) != 1 {
						t.Errorf("wrap-up delivered final failed-call input: acks=%v pending=%d", acked(), inbox.Default().Len(sess))
					}
				})
			}
		}
	}
}

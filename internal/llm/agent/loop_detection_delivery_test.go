package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opencode-ai/opencode/internal/inbox"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

type repeatResultTool struct {
	name     string
	parallel bool
	calls    atomic.Int32
	result   func(int) (tools.ToolResponse, error)
}

func (r *repeatResultTool) Info() tools.ToolInfo                                   { return tools.ToolInfo{Name: r.name} }
func (r *repeatResultTool) IsBaseline() bool                                       { return true }
func (r *repeatResultTool) AllowParallelism(tools.ToolCall, []tools.ToolCall) bool { return r.parallel }
func (r *repeatResultTool) Run(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
	return r.result(int(r.calls.Add(1)))
}
func repeatCall(name string, n int) message.ToolCall {
	return message.ToolCall{ID: fmt.Sprintf("%s-%d", name, n), Name: name, Input: `{}`, Finished: true}
}

func TestLoopDetection_CompletedResults(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		for _, changed := range []bool{false, true} {
			t.Run(fmt.Sprintf("parallel=%v/changed=%v", parallel, changed), func(t *testing.T) {
				t.Setenv("OPENCODE_MAX_REPEAT_CALLS", "3")
				withFreshTaskRegistry(t)
				tool := &repeatResultTool{name: "question", parallel: parallel, result: func(n int) (tools.ToolResponse, error) {
					if changed && n >= 3 {
						return tools.NewTextResponse(fmt.Sprintf("answer %d", n)), nil
					}
					return tools.NewTextErrorResponse("Question dismissed"), nil
				}}
				p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
					if n <= 5 {
						return toolTurn(repeatCall("question", n))
					}
					return endTurn()
				}}
				a := newLoopAgentWithTools(t, p, []tools.BaseTool{tool})
				a.allowParallelism = parallel
				res := a.processGeneration(context.Background(), t.Name(), "start", 10, nil, RunOptions{NonInteractive: true})
				if res.Error != nil {
					t.Fatal(res.Error)
				}
				results := toolResultsOf(t, a, t.Name())
				if len(results) != 5 {
					t.Fatalf("results=%d", len(results))
				}
				for i, r := range results {
					wantLoop := !changed && i >= 2
					if strings.Contains(r.Content, "Loop detected") != wantLoop {
						t.Errorf("result %d: %+v, want loop=%v", i, r, wantLoop)
					}
				}
				wantCalls := int32(3)
				if changed {
					wantCalls = 5
				}
				if tool.calls.Load() != wantCalls {
					t.Fatalf("executions=%d, want %d", tool.calls.Load(), wantCalls)
				}
			})
		}
	}
}

// Exercise both inbox drain sites with the same question arguments and result.
// The natural-end variant delivers input in text-only turns between tool turns.
func TestLoopDetection_DeliveredMessages(t *testing.T) {
	for _, naturalEnd := range []bool{false, true} {
		for _, chat := range []bool{false, true} {
			for _, failDelivery := range []bool{false, true} {
				t.Run(fmt.Sprintf("natural=%v/chat=%v/fail=%v", naturalEnd, chat, failDelivery), func(t *testing.T) {
					t.Setenv("OPENCODE_MAX_REPEAT_CALLS", "3")
					withFreshTaskRegistry(t)
					sess := t.Name()
					acked := useSessionInbox(t, sess)
					push := func(n int) { inbox.Default().Push(sess, nil, inbox.Item{ID: fmt.Sprint(n), Text: "incoming message"}) }
					question := &repeatResultTool{name: "question", result: func(n int) (tools.ToolResponse, error) {
						if !naturalEnd {
							push(n)
						}
						return tools.NewTextErrorResponse("User has not answered and has sent messages"), nil
					}}
					send := &repeatResultTool{name: "router_send", result: func(int) (tools.ToolResponse, error) { return tools.NewTextResponse("sent"), nil }}
					p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
						exchange := n
						if naturalEnd {
							if n%2 == 0 {
								if n < 8 {
									push(n)
								}
								return endTurn()
							}
							exchange = (n + 1) / 2
						}
						if exchange > 4 {
							return endTurn()
						}
						calls := []message.ToolCall{repeatCall("question", n)}
						if chat {
							calls = append([]message.ToolCall{repeatCall("router_send", n)}, calls...)
						}
						return toolTurn(calls...)
					}}
					a := newLoopAgentWithTools(t, p, []tools.BaseTool{question, send})
					if failDelivery {
						a.messages = &rejectInboxMessages{Service: a.messages}
					}
					res := a.processGeneration(context.Background(), sess, "start", 12, nil, RunOptions{NonInteractive: true})
					if res.Error != nil {
						t.Fatal(res.Error)
					}
					results := toolResultsOf(t, a, sess)
					loops := 0
					for _, r := range results {
						if strings.Contains(r.Content, "Loop detected") {
							loops++
						}
					}
					if !failDelivery {
						if loops != 0 || question.calls.Load() != 4 {
							t.Fatalf("loops=%d, question executions=%d", loops, question.calls.Load())
						}
						wantAck := 4
						if naturalEnd {
							wantAck = 3
						}
						if len(acked()) != wantAck {
							t.Fatalf("acked=%v", acked())
						}
						if chat && send.calls.Load() != 4 {
							t.Fatalf("send executions=%d", send.calls.Load())
						}
					} else if !naturalEnd && loops == 0 {
						t.Fatal("failed persistence incorrectly reset repetition")
					}
				})
			}
		}
	}
}

type rejectInboxMessages struct{ message.Service }

func (r *rejectInboxMessages) Create(ctx context.Context, sess string, p message.CreateMessageParams) (message.Message, error) {
	if p.Role == message.User {
		for _, part := range p.Parts {
			if text, ok := part.(message.TextContent); ok && text.Text == "incoming message" {
				return message.Message{}, errors.New("persist failed")
			}
		}
	}
	return r.Service.Create(ctx, sess, p)
}

func TestLoopDetection_ForcedToolAndParallelBatch(t *testing.T) {
	t.Run("forced terminal tool", func(t *testing.T) {
		t.Setenv("OPENCODE_MAX_REPEAT_CALLS", "1")
		tool := &repeatResultTool{name: tools.StructOutputToolName, result: func(int) (tools.ToolResponse, error) { return tools.NewTextResponse(`{"status":"done"}`), nil }}
		p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse { return toolTurn(repeatCall(tool.name, n)) }}
		a := newLoopAgentWithTools(t, p, []tools.BaseTool{tool})
		tracker := newCallTracker()
		tracker.Track(tool.name, `{}`, message.ToolResult{Content: `{"status":"done"}`})
		ctx := provider.WithForcedTool(context.Background(), tool.name)
		_, result, err := a.streamAndHandleEvents(ctx, t.Name(), nil, []tools.BaseTool{tool}, tracker)
		if err != nil {
			t.Fatal(err)
		}
		if tool.calls.Load() != 1 || result.ToolResults()[0].IsError {
			t.Fatalf("forced call was blocked: %+v", result)
		}
	})
	t.Run("parallel completions", func(t *testing.T) {
		t.Setenv("OPENCODE_MAX_REPEAT_CALLS", "3")
		tool := &repeatResultTool{name: "read", parallel: true, result: func(int) (tools.ToolResponse, error) { return tools.NewTextResponse("same"), nil }}
		p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
			calls := make([]message.ToolCall, 6)
			for i := range calls {
				calls[i] = repeatCall(tool.name, i)
			}
			return toolTurn(calls...)
		}}
		a := newLoopAgentWithTools(t, p, []tools.BaseTool{tool})
		a.allowParallelism = true
		tracker := newCallTracker()
		_, result, err := a.streamAndHandleEvents(context.Background(), t.Name(), nil, []tools.BaseTool{tool}, tracker)
		if err != nil {
			t.Fatal(err)
		}
		loops := 0
		for _, r := range result.ToolResults() {
			if strings.Contains(r.Content, "Loop detected") {
				loops++
			}
		}
		if loops != 4 || tracker.streakCount["read"] != 6 {
			t.Fatalf("loops=%d, streak=%d", loops, tracker.streakCount["read"])
		}
	})
}

func TestLoopDetection_IdenticalQuestionAnswersAreInput(t *testing.T) {
	t.Setenv("OPENCODE_MAX_REPEAT_CALLS", "3")
	withFreshTaskRegistry(t)
	question := &repeatResultTool{name: tools.QuestionToolName, result: func(int) (tools.ToolResponse, error) {
		response := tools.NewTextResponse("Approve")
		response.UserInputDelivered = true
		return response, nil
	}}
	send := &repeatResultTool{name: "router_send", result: func(int) (tools.ToolResponse, error) {
		return tools.NewTextResponse("sent"), nil
	}}
	p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
		if n <= 5 {
			return toolTurn(repeatCall("router_send", n), repeatCall(tools.QuestionToolName, n))
		}
		return endTurn()
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{question, send})
	res := a.processGeneration(context.Background(), t.Name(), "start", 10, nil, RunOptions{NonInteractive: true})
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	for _, result := range toolResultsOf(t, a, t.Name()) {
		if result.IsError || strings.Contains(result.Content, "Loop detected") {
			t.Fatalf("user answer did not reset repetition: %+v", result)
		}
	}
	if question.calls.Load() != 5 || send.calls.Load() != 5 {
		t.Fatalf("executions question=%d, send=%d", question.calls.Load(), send.calls.Load())
	}
}

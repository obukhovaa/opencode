package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

func TestLoopDetectionStopsLaterSequentialCallsInSameBatch(t *testing.T) {
	t.Setenv("OPENCODE_MAX_REPEAT_CALLS", "3")
	tool := &repeatResultTool{name: "read", result: func(int) (tools.ToolResponse, error) { return tools.NewTextResponse("same"), nil }}
	p := &scriptedProvider{respond: func(int, bool) *provider.ProviderResponse {
		calls := make([]message.ToolCall, 6)
		for i := range calls {
			calls[i] = repeatCall(tool.name, i)
		}
		return toolTurn(calls...)
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{tool})
	tracker := newCallTracker()
	_, results, err := a.streamAndHandleEvents(context.Background(), t.Name(), nil, []tools.BaseTool{tool}, tracker)
	if err != nil {
		t.Fatal(err)
	}
	if got := tool.calls.Load(); got != 3 {
		t.Fatalf("executed %d calls; calls after the third completed repetition must not run", got)
	}
	if tracker.streakCount[tool.name] != 3 {
		t.Fatalf("blocked calls changed streak: %d", tracker.streakCount[tool.name])
	}
	for i, result := range results.ToolResults() {
		if got := strings.Contains(result.Content, "Loop detected"); got != (i >= 2) {
			t.Errorf("result %d=%+v", i, result)
		}
	}
}

func TestLoopDetectionEarlierAnswerUnblocksLaterSequentialCall(t *testing.T) {
	t.Setenv("OPENCODE_MAX_REPEAT_CALLS", "3")
	question := &repeatResultTool{name: tools.QuestionToolName, result: func(int) (tools.ToolResponse, error) {
		response := tools.NewTextResponse("Approve")
		response.UserInputDelivered = true
		return response, nil
	}}
	read := &repeatResultTool{name: "read", result: func(int) (tools.ToolResponse, error) { return tools.NewTextResponse("same"), nil }}
	p := &scriptedProvider{respond: func(int, bool) *provider.ProviderResponse {
		return toolTurn(repeatCall(question.name, 1), repeatCall(read.name, 1))
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{question, read})
	tracker := newCallTracker()
	for i := 0; i < 3; i++ {
		tracker.Track("read", "{}", message.ToolResult{Type: message.ToolResultTypeText, Content: "same"})
	}
	_, results, err := a.streamAndHandleEvents(context.Background(), t.Name(), nil, []tools.BaseTool{question, read}, tracker)
	if err != nil {
		t.Fatal(err)
	}
	if read.calls.Load() != 1 {
		t.Fatal("read was discarded before the earlier question could deliver input")
	}
	for _, result := range results.ToolResults() {
		if result.IsError {
			t.Fatalf("unexpected blocked result: %+v", result)
		}
	}
}

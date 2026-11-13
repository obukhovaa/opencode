package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/inbox"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/question"
)

type loopQuestionService struct {
	question.Service
	ask func(context.Context, string, []question.Prompt) ([][]string, error)
}

func (s loopQuestionService) Ask(ctx context.Context, id string, prompts []question.Prompt) ([][]string, error) {
	return s.ask(ctx, id, prompts)
}

type countedQuestionTool struct {
	tools.BaseTool
	calls int
}

func (t *countedQuestionTool) Run(ctx context.Context, call tools.ToolCall) (tools.ToolResponse, error) {
	t.calls++
	return t.BaseTool.Run(ctx, call)
}

func TestLoopDetectionRealQuestionInputProvenance(t *testing.T) {
	for _, mode := range []string{"human-answer", "automatic-default", "unanswered-failed-delivery"} {
		t.Run(mode, func(t *testing.T) {
			threshold := "3"
			if mode == "human-answer" {
				threshold = "1"
			}
			t.Setenv("OPENCODE_MAX_REPEAT_CALLS", threshold)
			withFreshTaskRegistry(t)
			sid := t.Name()
			useSessionInbox(t, sid)
			svc := loopQuestionService{ask: func(_ context.Context, id string, _ []question.Prompt) ([][]string, error) {
				if mode == "automatic-default" {
					t.Fatal("unattended question reached human service")
				}
				if mode == "unanswered-failed-delivery" {
					inbox.Default().Push(id, nil, inbox.Item{Text: "incoming message"})
					return nil, &question.UnansweredError{Note: "User sent new directions"}
				}
				return [][]string{{"Approve"}}, nil
			}}
			perms := permission.NewPermissionService()
			if mode == "automatic-default" {
				perms.AutoApproveSession(sid)
				perms.MarkUnattendedSession(sid)
			}
			tool := &countedQuestionTool{BaseTool: tools.NewQuestionTool(svc, perms)}
			p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
				if n > 5 {
					return endTurn()
				}
				return toolTurn(message.ToolCall{ID: fmt.Sprint(n), Name: tools.QuestionToolName, Input: `{"questions":[{"question":"Proceed?","options":[{"label":"Approve"}]}]}`, Finished: true})
			}}
			a := newLoopAgentWithTools(t, p, []tools.BaseTool{tool})
			if mode == "unanswered-failed-delivery" {
				a.messages = &rejectInboxMessages{Service: a.messages}
			}
			event := a.processGeneration(context.Background(), sid, "start", 10, nil, RunOptions{NonInteractive: true})
			if event.Error != nil {
				t.Fatal(event.Error)
			}
			wantCalls := 3
			if mode == "human-answer" {
				wantCalls = 5
			}
			if tool.calls != wantCalls {
				t.Errorf("executions=%d want=%d", tool.calls, wantCalls)
			}
			for i, result := range toolResultsOf(t, a, sid) {
				wantLoop := mode != "human-answer" && i >= 2
				if strings.Contains(result.Content, "Loop detected") != wantLoop {
					t.Errorf("result%d=%+v wantLoop=%t", i, result, wantLoop)
				}
			}
		})
	}
}

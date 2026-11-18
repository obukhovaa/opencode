package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opencode-ai/opencode/internal/inbox"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/task"
)

func TestProcessGeneration_CompositionRejectionStopsWithPendingReport(t *testing.T) {
	for _, queuedInput := range []bool{false, true} {
		name := "report"
		if queuedInput {
			name = "report and reviewer input"
		}
		t.Run(name, func(t *testing.T) {
			withAutoCompact(t, false)
			ctx, reg, mon := backgroundWaitSetup(t)
			t.Cleanup(func() { inbox.Default().Clear(mon.SessionID) })
			var childCalls atomic.Int32
			registry, leaf := compositionTestSetup(t, permission.ActionAsk, func(context.Context, string) tools.ToolResponse {
				childCalls.Add(1)
				return compositionText(`{"id":123}`)
			})
			p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
				if n == 1 {
					return toolTurn(message.ToolCall{ID: "compose", Name: tools.MCPComposeToolName, Input: compositionPlan(1), Finished: true})
				}
				// Bound the regression: an incorrect second call must fail
				// the assertions instead of waiting on the monitor forever.
				reg.MarkFinished(mon.ID, task.StateCompleted, nil)
				return endTurn()
			}}
			a := newLoopAgentWithTools(t, p, []tools.BaseTool{tools.NewMCPComposeTool(), leaf})
			a.reg = registry
			permissions := leaf.(*mcpTool).permissions
			events := permissions.Subscribe(ctx)
			delivery := make(chan error, 1)
			go func() {
				select {
				case event := <-events:
					err := persistBackgroundEvent(ctx, a, reg, mon.SessionID, "awaiting-approval")
					if queuedInput {
						inbox.Default().Push(mon.SessionID, nil, inbox.Item{Text: "queued reviewer input"})
					}
					permissions.Deny(event.Payload)
					delivery <- err
				case <-ctx.Done():
					delivery <- ctx.Err()
				}
			}()
			result := a.processGeneration(ctx, mon.SessionID, "read", 0, nil, RunOptions{NonInteractive: true})
			if err := <-delivery; err != nil {
				t.Fatal(err)
			}
			if result.Error != nil || !result.Done || result.Message.FinishReason() != message.FinishReasonPermissionDenied || p.callCount() != 1 {
				t.Fatalf("error=%v, done=%v, finish=%s, calls=%d; want permission_denied after one call", result.Error, result.Done, result.Message.FinishReason(), p.callCount())
			}
			if childCalls.Load() != 0 || mon.State() != task.StateRunning {
				t.Fatalf("child calls=%d, monitor=%s; rejection must not execute the child or stop the monitor", childCalls.Load(), mon.State())
			}
			if queuedInput && inbox.Default().Len(mon.SessionID) != 1 {
				t.Fatal("rejection consumed queued reviewer input")
			}
			foundRejection := false
			for _, r := range toolResultsOf(t, a, mon.SessionID) {
				if r.ToolCallID == "compose" && r.IsError && strings.Contains(r.Content, `"code":"permission_denied"`) {
					foundRejection = true
				}
			}
			if !foundRejection {
				t.Fatal("composition rejection envelope was not persisted")
			}
		})
	}
}

func TestProcessGeneration_ExplicitStopDoesNotConsumePendingInput(t *testing.T) {
	for _, reason := range []message.FinishReason{message.FinishReasonPermissionDenied, message.FinishReasonCanceled, message.FinishReasonError} {
		t.Run(string(reason), func(t *testing.T) {
			withAutoCompact(t, false)
			ctx, reg, mon := backgroundWaitSetup(t)
			t.Cleanup(func() { inbox.Default().Clear(mon.SessionID) })
			var a *agent
			p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
				if n == 1 {
					if err := persistBackgroundEvent(ctx, a, reg, mon.SessionID, "stopped-turn"); err != nil {
						t.Error(err)
					}
					inbox.Default().Push(mon.SessionID, nil, inbox.Item{Text: "queued reviewer input"})
					return &provider.ProviderResponse{Content: "stopped", FinishReason: reason}
				}
				reg.MarkFinished(mon.ID, task.StateCompleted, nil)
				return endTurn()
			}}
			a = newLoopAgentWithTools(t, p, nil)
			result := a.processGeneration(ctx, mon.SessionID, "read", 0, nil, RunOptions{NonInteractive: true})
			if result.Error != nil || result.Message.FinishReason() != reason || p.callCount() != 1 {
				t.Fatalf("error=%v, finish=%s, calls=%d; want %s after one call", result.Error, result.Message.FinishReason(), p.callCount(), reason)
			}
			if inbox.Default().Len(mon.SessionID) != 1 || mon.State() != task.StateRunning {
				t.Fatal("explicit stop consumed pending input or stopped the monitor")
			}
		})
	}
}

func TestCompletionHandoff_ExplicitStopKeepsUnseenReport(t *testing.T) {
	for _, tc := range []struct {
		reason         message.FinishReason
		turnsExhausted bool
		wantResume     bool
	}{
		{reason: message.FinishReasonPermissionDenied},
		{reason: message.FinishReasonCanceled},
		{reason: message.FinishReasonError},
		{reason: message.FinishReasonEndTurn, wantResume: true},
		{reason: message.FinishReasonMaxTokens, wantResume: true},
		{reason: message.FinishReasonEndTurn, turnsExhausted: true, wantResume: true},
	} {
		name := string(tc.reason)
		if tc.turnsExhausted {
			name += " exhausted"
		}
		t.Run(name, func(t *testing.T) {
			withAutoCompact(t, false)
			ctx, _, mon := backgroundWaitSetup(t)
			a := newLoopAgentWithTools(t, &scriptedProvider{}, nil)
			deps := &completionDeps{messages: a.messages, resumed: make(chan string, 1)}
			t.Cleanup(task.SetDeps(deps))
			t.Cleanup(func() { task.TakeSkippedResume(mon.SessionID) })
			if !acquireSessionSlot(mon.SessionID, func() {}) {
				t.Fatal("session unexpectedly busy")
			}
			t.Cleanup(func() { releaseSessionSlot(mon.SessionID) })
			if err := task.EnqueueTaskCompletion(ctx, task.CompletionInput{
				SessionID: mon.SessionID, OriginatingToolName: tools.MonitorToolName,
				TaskID: mon.ID, Kind: task.KindMonitor, Status: task.StatusMonitorEvent, Content: "unseen report",
			}); err != nil {
				t.Fatal(err)
			}
			msg := textMsg(message.Assistant, "finished")
			msg.AddFinish(tc.reason)
			after := a.compactAfterTurn(ctx, context.Background(), mon.SessionID, AgentEvent{Message: msg, Done: true, TurnsExhausted: tc.turnsExhausted})
			releaseSessionSlot(mon.SessionID)
			if after != nil {
				after()
			}
			if resumed := len(deps.resumed) > 0; resumed != tc.wantResume {
				t.Fatalf("resumed=%v, want %v after %s", resumed, tc.wantResume, tc.reason)
			}
			if !tc.wantResume {
				if _, ok := task.TakeSkippedResume(mon.SessionID); !ok {
					t.Fatal("explicit stop discarded the unseen report's resume marker")
				}
			}
		})
	}
}

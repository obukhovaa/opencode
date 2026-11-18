package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/inbox"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/task"
)

// Observe the actual wait entry, after tool-boundary report consumption.
// Every invocation must also exit before the next model request can run.
type backgroundWaitRegistry struct {
	task.Registry
	entered chan struct{}
	exited  chan struct{}
	waitErr error
}

func (r *backgroundWaitRegistry) WaitForActiveTasks(ctx context.Context, sessionID string, opts task.WaitOptions) error {
	r.entered <- struct{}{}
	defer func() { r.exited <- struct{}{} }()
	if r.waitErr != nil {
		return r.waitErr
	}
	return r.Registry.WaitForActiveTasks(ctx, sessionID, opts)
}

func backgroundWaitSetup(t *testing.T) (context.Context, task.Registry, *task.Task) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	reg := withFreshTaskRegistry(t)
	mon := &task.Task{ID: task.NewTaskID(task.KindMonitor), SessionID: t.Name(), Kind: task.KindMonitor}
	if err := reg.Register(mon); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.MarkFinished(mon.ID, task.StateCompleted, nil) })
	return ctx, reg, mon
}

func observeBackgroundWait(reg task.Registry) *backgroundWaitRegistry {
	r := &backgroundWaitRegistry{Registry: reg, entered: make(chan struct{}, 16), exited: make(chan struct{}, 16)}
	task.ResetGlobalRegistry()
	task.SetGlobalRegistry(r)
	return r
}

func awaitBackgroundSignal(t *testing.T, ctx context.Context, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatalf("background wait handshake: %v", ctx.Err())
	}
}

func runBackgroundLoop(a *agent, ctx context.Context, sessionID string, noninteractive bool) <-chan AgentEvent {
	done := make(chan AgentEvent, 1)
	go func() {
		done <- a.processGeneration(ctx, sessionID, "start", 0, nil, RunOptions{NonInteractive: noninteractive})
	}()
	return done
}

func awaitBackgroundLoop(t *testing.T, ctx context.Context, done <-chan AgentEvent) AgentEvent {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-ctx.Done():
		t.Fatalf("processGeneration did not return: %v", ctx.Err())
		return AgentEvent{}
	}
}

func persistBackgroundEvent(ctx context.Context, a *agent, reg task.Registry, sessionID, id string) error {
	_, _, err := a.messages.CreatePair(ctx, sessionID,
		message.CreateMessageParams{Role: message.Assistant, Synthetic: true, Parts: []message.ContentPart{
			message.ToolCall{ID: id, Name: "monitor", Input: `{}`, Finished: true},
		}},
		message.CreateMessageParams{Role: message.Tool, Synthetic: true, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: id, Name: "monitor", Content: "event " + id},
		}},
	)
	if err == nil {
		reg.NotifySession(sessionID)
	}
	return err
}

func assertBackgroundEvent(t *testing.T, history []message.Message, id string, want int) {
	t.Helper()
	count := 0
	for i, msg := range history {
		for _, result := range msg.ToolResultsByToolName("monitor") {
			if result.ToolCallID != id {
				continue
			}
			count++
			if !msg.Synthetic || i == 0 || !history[i-1].Synthetic {
				t.Fatalf("event %s lost its synthetic pair", id)
			}
			calls := history[i-1].ToolCalls()
			if len(calls) != 1 || calls[0].ID != id || result.Content != "event "+id {
				t.Fatalf("event %s pair is not adjacent/intact", id)
			}
		}
	}
	if count != want {
		t.Fatalf("event %s occurs %d times, want %d", id, count, want)
	}
}

func yieldBackgroundTurn(id string) *provider.ProviderResponse {
	return toolTurn(message.ToolCall{ID: id, Name: tools.YieldToolName, Input: `{}`, Finished: true})
}

func TestProcessGeneration_BackgroundYieldRepeatedEvents(t *testing.T) {
	ctx, reg, mon := backgroundWaitSetup(t)
	r := observeBackgroundWait(reg)
	p := &recordingProvider{scriptedProvider: &scriptedProvider{respond: func(call int, _ bool) *provider.ProviderResponse {
		if call <= 2 {
			return yieldBackgroundTurn(string(rune('a' + call)))
		}
		if mon.State() != task.StateRunning {
			t.Error("monitor exited before reacting to both events")
		}
		reg.MarkFinished(mon.ID, task.StateCompleted, nil)
		return endTurn()
	}}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{tools.NewYieldTool()})
	done := runBackgroundLoop(a, ctx, mon.SessionID, true)
	for i, id := range []string{"first", "second"} {
		awaitBackgroundSignal(t, ctx, r.entered)
		if got := p.callCount(); got != i+1 {
			t.Fatalf("calls before event %s = %d, want %d", id, got, i+1)
		}
		if err := persistBackgroundEvent(ctx, a, reg, mon.SessionID, id); err != nil {
			t.Fatal(err)
		}
		awaitBackgroundSignal(t, ctx, r.exited)
	}
	if result := awaitBackgroundLoop(t, ctx, done); result.Error != nil {
		t.Fatal(result.Error)
	}
	if p.callCount() != 3 {
		t.Fatalf("calls = %d, want 3", p.callCount())
	}
	assertBackgroundEvent(t, p.request(2), "first", 1)
	assertBackgroundEvent(t, p.request(2), "second", 0)
	assertBackgroundEvent(t, p.request(3), "first", 1)
	assertBackgroundEvent(t, p.request(3), "second", 1)
}

type backgroundIndependentTool struct {
	onRun func() error
}

func (backgroundIndependentTool) Info() tools.ToolInfo { return tools.ToolInfo{Name: "independent"} }
func (t backgroundIndependentTool) Run(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
	return tools.NewTextResponse("work complete"), t.onRun()
}
func (backgroundIndependentTool) AllowParallelism(tools.ToolCall, []tools.ToolCall) bool { return true }
func (backgroundIndependentTool) IsBaseline() bool                                       { return true }

func TestProcessGeneration_BackgroundIndependentWorkGetsReports(t *testing.T) {
	ctx, reg, mon := backgroundWaitSetup(t)
	p := &recordingProvider{scriptedProvider: &scriptedProvider{respond: func(call int, _ bool) *provider.ProviderResponse {
		if call == 1 {
			return toolTurn(message.ToolCall{ID: "work", Name: "independent", Input: `{}`, Finished: true})
		}
		return endTurn()
	}}}
	var a *agent
	ran := false
	work := backgroundIndependentTool{onRun: func() error {
		ran = true
		if mon.State() != task.StateRunning {
			return errors.New("monitor exited before independent work")
		}
		return persistBackgroundEvent(ctx, a, reg, mon.SessionID, "during-work")
	}}
	a = newLoopAgentWithTools(t, p, []tools.BaseTool{work, tools.NewYieldTool()})
	if result := awaitBackgroundLoop(t, ctx, runBackgroundLoop(a, ctx, mon.SessionID, false)); result.Error != nil {
		t.Fatal(result.Error)
	}
	if !ran || p.callCount() != 2 || mon.State() != task.StateRunning {
		t.Fatalf("independent tool ran=%v, calls=%d, monitor=%v", ran, p.callCount(), mon.State())
	}
	assertBackgroundEvent(t, p.request(2), "during-work", 1)
}

func TestProcessGeneration_BackgroundReportBeforeYieldDoesNotWaitAgain(t *testing.T) {
	ctx, reg, mon := backgroundWaitSetup(t)
	var a *agent
	p := &recordingProvider{scriptedProvider: &scriptedProvider{respond: func(call int, _ bool) *provider.ProviderResponse {
		if call == 1 {
			// Persist during generation: appendBackgroundReports consumes it
			// after the yield tool batch, before the outer wait is reached.
			if err := persistBackgroundEvent(ctx, a, reg, mon.SessionID, "early"); err != nil {
				t.Error(err)
			}
			return yieldBackgroundTurn("yield-early")
		}
		if mon.State() != task.StateRunning {
			t.Error("monitor exited before reacting to the early event")
		}
		reg.MarkFinished(mon.ID, task.StateCompleted, nil)
		return endTurn()
	}}}
	a = newLoopAgentWithTools(t, p, []tools.BaseTool{tools.NewYieldTool()})
	if result := awaitBackgroundLoop(t, ctx, runBackgroundLoop(a, ctx, mon.SessionID, true)); result.Error != nil {
		t.Fatal(result.Error)
	}
	if p.callCount() != 2 {
		t.Fatalf("calls=%d, want 2", p.callCount())
	}
	assertBackgroundEvent(t, p.request(2), "early", 1)
}

func TestProcessGeneration_BackgroundYieldWithoutActiveTasks(t *testing.T) {
	withFreshTaskRegistry(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &scriptedProvider{respond: func(call int, _ bool) *provider.ProviderResponse {
		if call == 1 {
			return yieldBackgroundTurn("idle")
		}
		return endTurn()
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{tools.NewYieldTool()})
	if result := awaitBackgroundLoop(t, ctx, runBackgroundLoop(a, ctx, t.Name(), true)); result.Error != nil || p.callCount() != 2 {
		t.Fatalf("result=%v, calls=%d", result.Error, p.callCount())
	}
}

func TestProcessGeneration_BackgroundEndTurnWakesOnNonterminalEvent(t *testing.T) {
	ctx, reg, mon := backgroundWaitSetup(t)
	r := observeBackgroundWait(reg)
	p := &recordingProvider{scriptedProvider: &scriptedProvider{respond: func(call int, _ bool) *provider.ProviderResponse {
		if call == 2 {
			if mon.State() != task.StateRunning {
				t.Error("monitor exited before reacting to event")
			}
			reg.MarkFinished(mon.ID, task.StateCompleted, nil)
		}
		return endTurn()
	}}}
	a := newLoopAgentWithTools(t, p, nil)
	done := runBackgroundLoop(a, ctx, mon.SessionID, true)
	awaitBackgroundSignal(t, ctx, r.entered)
	if err := persistBackgroundEvent(ctx, a, reg, mon.SessionID, "nonterminal"); err != nil {
		t.Fatal(err)
	}
	if result := awaitBackgroundLoop(t, ctx, done); result.Error != nil || p.callCount() != 2 {
		t.Fatalf("result=%v, calls=%d", result.Error, p.callCount())
	}
	assertBackgroundEvent(t, p.request(2), "nonterminal", 1)
}

func TestProcessGeneration_BackgroundWaitCancellationAndErrorJoin(t *testing.T) {
	for _, waitError := range []bool{false, true} {
		name := "cancel"
		if waitError {
			name = "wait-result-consumed-by-select"
		}
		t.Run(name, func(t *testing.T) {
			deadline, reg, mon := backgroundWaitSetup(t)
			ctx, cancel := context.WithCancel(deadline)
			defer cancel()
			r := observeBackgroundWait(reg)
			wantErr := context.Canceled
			if waitError {
				wantErr = errors.New("wait failed")
				r.waitErr = wantErr
			}
			p := &scriptedProvider{respond: func(int, bool) *provider.ProviderResponse { return yieldBackgroundTurn("cancel") }}
			a := newLoopAgentWithTools(t, p, []tools.BaseTool{tools.NewYieldTool()})
			done := runBackgroundLoop(a, ctx, mon.SessionID, true)
			awaitBackgroundSignal(t, deadline, r.entered)
			if !waitError {
				cancel()
			}
			result := awaitBackgroundLoop(t, deadline, done)
			if !errors.Is(result.Error, wantErr) || p.callCount() != 1 {
				t.Fatalf("error=%v, want %v; calls=%d, want 1", result.Error, wantErr, p.callCount())
			}
			awaitBackgroundSignal(t, deadline, r.exited)
		})
	}
}

func TestProcessGeneration_BackgroundYieldInboxWake(t *testing.T) {
	ctx, reg, mon := backgroundWaitSetup(t)
	r := observeBackgroundWait(reg)
	t.Cleanup(func() { inbox.Default().Clear(mon.SessionID) })
	p := &recordingProvider{scriptedProvider: &scriptedProvider{respond: func(call int, _ bool) *provider.ProviderResponse {
		if call == 1 {
			return yieldBackgroundTurn("inbox")
		}
		if mon.State() != task.StateRunning {
			t.Error("monitor exited before reacting to user input")
		}
		reg.MarkFinished(mon.ID, task.StateCompleted, nil)
		return endTurn()
	}}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{tools.NewYieldTool()})
	done := runBackgroundLoop(a, ctx, mon.SessionID, true)
	awaitBackgroundSignal(t, ctx, r.entered)
	inbox.Default().Push(mon.SessionID, nil, inbox.Item{Text: "user interrupts the wait"})
	if result := awaitBackgroundLoop(t, ctx, done); result.Error != nil || p.callCount() != 2 {
		t.Fatalf("result=%v, calls=%d", result.Error, p.callCount())
	}
	awaitBackgroundSignal(t, ctx, r.exited)
	req := p.request(2)
	last := req[len(req)-1]
	if last.Role != message.User || last.Content().Text != "user interrupts the wait" {
		t.Fatalf("last message=%+v", last)
	}
}

func TestProcessGeneration_BackgroundInteractiveYieldReleasesSession(t *testing.T) {
	ctx, reg, mon := backgroundWaitSetup(t)
	r := observeBackgroundWait(reg)
	p := &scriptedProvider{respond: func(int, bool) *provider.ProviderResponse {
		return yieldBackgroundTurn("interactive")
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{tools.NewYieldTool()})
	result := awaitBackgroundLoop(t, ctx, runBackgroundLoop(a, ctx, mon.SessionID, false))
	if result.Error != nil || !result.Done || p.callCount() != 1 || mon.State() != task.StateRunning {
		t.Fatalf("error=%v, done=%v, calls=%d, monitor=%v", result.Error, result.Done, p.callCount(), mon.State())
	}
	select {
	case <-r.entered:
		t.Fatal("interactive yield entered the background wait")
	default:
	}
}

func TestProcessGeneration_BackgroundCompletesAtWaitEntry(t *testing.T) {
	ctx, reg, mon := backgroundWaitSetup(t)
	var a *agent
	wrapped := &completingRegistry{Registry: reg, complete: func() {
		if err := persistBackgroundEvent(ctx, a, reg, mon.SessionID, "at-entry"); err != nil {
			t.Error(err)
		}
		reg.MarkFinished(mon.ID, task.StateCompleted, nil)
	}}
	task.ResetGlobalRegistry()
	task.SetGlobalRegistry(wrapped)
	p := &recordingProvider{scriptedProvider: &scriptedProvider{respond: func(call int, _ bool) *provider.ProviderResponse {
		if call == 1 {
			return yieldBackgroundTurn("entry")
		}
		return endTurn()
	}}}
	a = newLoopAgentWithTools(t, p, []tools.BaseTool{tools.NewYieldTool()})
	if result := awaitBackgroundLoop(t, ctx, runBackgroundLoop(a, ctx, mon.SessionID, true)); result.Error != nil || p.callCount() != 2 {
		t.Fatalf("result=%v, calls=%d", result.Error, p.callCount())
	}
	assertBackgroundEvent(t, p.request(2), "at-entry", 1)
}

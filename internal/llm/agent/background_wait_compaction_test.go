package agent

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/inbox"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/task"
)

func TestBackgroundMessages_OnlyCompleteSyntheticBackgroundPairs(t *testing.T) {
	pair := func(name string, synthetic bool) []message.Message {
		return []message.Message{
			{Role: message.Assistant, Synthetic: synthetic, Parts: []message.ContentPart{
				message.ToolCall{ID: "event", Name: name, Input: `{}`, Finished: true},
			}},
			{Role: message.Tool, Synthetic: synthetic, Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "event", Name: name, Content: "report"},
			}},
		}
	}
	syntheticText := func(text string) message.Message {
		m := textMsg(message.User, text)
		m.Synthetic = true
		return m
	}
	type testCase struct {
		name string
		msgs []message.Message
		seen bool
		want int
	}
	tests := []testCase{
		{name: "schema envelope", msgs: []message.Message{syntheticText(`<struct_output_schema>{"type":"object"}</struct_output_schema>`)}},
		{name: "memory index", msgs: []message.Message{syntheticText("<memory_index>MEMORY.md snapshot</memory_index>")}},
		{name: "deferred tool delta", msgs: []message.Message{syntheticText("<deferred_tools>new tools available</deferred_tools>")}},
		{name: "orphan call", msgs: pair("monitor", true)[:1]},
		{name: "orphan result", msgs: pair("monitor", true)[1:]},
		{name: "ordinary tool pair", msgs: pair("monitor", false)},
		{name: "unrelated synthetic tool", msgs: pair("read", true)},
		{name: "bash report", msgs: pair("bash", true), want: 2},
		{name: "task report", msgs: pair("task", true), want: 2},
		{name: "monitor report", msgs: pair("monitor", true), want: 2},
		{name: "consumed report", msgs: pair("monitor", true), seen: true},
	}
	mismatched := pair("monitor", true)
	mismatched[1].Parts = []message.ContentPart{message.ToolResult{ToolCallID: "different", Content: "report"}}
	halfSynthetic := pair("monitor", true)
	halfSynthetic[1].Synthetic = false
	nonadjacent := pair("monitor", true)
	nonadjacent = []message.Message{nonadjacent[0], syntheticText("intervening context"), nonadjacent[1]}
	tests = append(tests,
		testCase{name: "mismatched call ID", msgs: mismatched},
		testCase{name: "only call synthetic", msgs: halfSynthetic},
		testCase{name: "nonadjacent pair", msgs: nonadjacent},
	)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withFreshTaskRegistry(t)
			a := &agent{messages: newMemMessages()}
			seedHistory(t, a, t.Name(), tc.msgs...)
			seen := map[string]bool{}
			if tc.seen {
				msgs, err := a.messages.List(context.Background(), t.Name())
				if err != nil {
					t.Fatal(err)
				}
				rememberMessages(seen, msgs)
			}
			got, err := a.backgroundMessages(context.Background(), t.Name(), seen)
			if err != nil || len(got) != tc.want {
				t.Fatalf("reports=%v, error=%v; want %d messages", texts(got), err, tc.want)
			}
			for i := range got {
				if got[i].Role != tc.msgs[i].Role || !reflect.DeepEqual(got[i].Parts, tc.msgs[i].Parts) {
					t.Fatalf("report message %d changed: %+v", i, got[i])
				}
			}
		})
	}
}

// Inject synchronously after subscribing and before reading durable state.
// This puts reports and reviewer input at the outer wait boundary together.
type backgroundWatchHookRegistry struct {
	task.Registry
	once    sync.Once
	onWatch func()
}

func (r *backgroundWatchHookRegistry) WatchSession(sessionID string) <-chan struct{} {
	ch := r.Registry.WatchSession(sessionID)
	r.once.Do(r.onWatch)
	return ch
}

type backgroundPendingHookRegistry struct {
	task.Registry
	once      sync.Once
	onPending func()
}

func (r *backgroundPendingHookRegistry) PendingForSession(sessionID string, filter func(*task.Task) bool) []*task.Task {
	r.once.Do(r.onPending)
	return r.Registry.PendingForSession(sessionID, filter)
}

func TestProcessGeneration_BackgroundReentryCompactionPreservesInput(t *testing.T) {
	for _, arrival := range []string{"outer yield", "outer end turn", "yield tool boundary", "early report late reviewer"} {
		t.Run(arrival, func(t *testing.T) {
			withAutoCompact(t, true)
			ctx, reg, mon := backgroundWaitSetup(t)
			t.Cleanup(func() { inbox.Default().Clear(mon.SessionID) })
			a, main, _, log := newCompactingAgent(t, 100, []tools.BaseTool{tools.NewYieldTool()})
			report := "REPORT VERBATIM\n" + strings.Repeat("r", 4000)
			input := "REVIEWER VERBATIM\n" + strings.Repeat("u", 4000)
			pair := completionPair("compact-event", report)
			publishReport := func() {
				_, _, err := a.messages.CreatePair(ctx, mon.SessionID,
					message.CreateMessageParams{Role: pair[0].Role, Parts: pair[0].Parts, Synthetic: true},
					message.CreateMessageParams{Role: pair[1].Role, Parts: pair[1].Parts, Synthetic: true})
				if err != nil {
					t.Error(err)
				}
				reg.NotifySession(mon.SessionID)
			}
			publishReviewer := func() {
				inbox.Default().Push(mon.SessionID, nil, inbox.Item{Text: input})
				reg.MarkFinished(mon.ID, task.StateCompleted, nil)
			}
			inject := func() { publishReport(); publishReviewer() }
			if arrival == "early report late reviewer" {
				task.ResetGlobalRegistry()
				task.SetGlobalRegistry(&backgroundPendingHookRegistry{Registry: reg, onPending: publishReviewer})
			} else if arrival != "yield tool boundary" {
				task.ResetGlobalRegistry()
				task.SetGlobalRegistry(&backgroundWatchHookRegistry{Registry: reg, onWatch: inject})
			}
			calls := 0
			main.respond = func([]message.Message) *provider.ProviderResponse {
				calls++
				if calls == 1 {
					if arrival == "yield tool boundary" {
						inject()
					} else if arrival == "early report late reviewer" {
						publishReport()
					}
					if arrival != "outer end turn" {
						return yieldBackgroundTurn("compact-yield")
					}
				}
				return endTurn()
			}
			result := awaitBackgroundLoop(t, ctx, runBackgroundLoop(a, ctx, mon.SessionID, true))
			if result.Error != nil {
				t.Fatal(result.Error)
			}
			if want := []string{"main", "summarizer", "main"}; !slices.Equal(*log, want) {
				t.Fatalf("call order=%v, want %v", *log, want)
			}
			req := main.requests[1]
			if len(req) < 4 || firstText(req[0]) != testSummaryText {
				t.Fatalf("reentry request=%v, want summary followed by fresh input", texts(req))
			}
			tail := req[len(req)-3:]
			for i := 0; i < 2; i++ {
				if tail[i].Role != pair[i].Role || !tail[i].Synthetic || !reflect.DeepEqual(tail[i].Parts, pair[i].Parts) {
					t.Fatalf("compaction lost report message %d verbatim", i)
				}
			}
			if tail[2].Role != message.User || firstText(tail[2]) != input {
				t.Fatal("compaction lost reviewer input verbatim")
			}
			if arrival == "yield tool boundary" || arrival == "early report late reviewer" {
				// yieldReady must retain the complete real tool batch too.
				if len(req) != 6 {
					t.Fatalf("request roles=%v, want summary, yield pair, report pair, user", roles(req))
				}
				if results := req[2].ToolResultsByToolName(tools.YieldToolName); len(results) != 1 || results[0].ToolCallID != "compact-yield" {
					t.Fatal("yieldReady compaction lost yield tool result")
				}
				if uses := req[1].ToolCalls(); len(uses) != 1 || uses[0].ID != "compact-yield" {
					t.Fatal("yieldReady compaction lost yield tool call")
				}
			}
		})
	}
}

type backgroundListObserver struct {
	message.Service
	onList func([]message.Message)
}

func (m *backgroundListObserver) List(ctx context.Context, sessionID string) ([]message.Message, error) {
	msgs, err := m.Service.List(ctx, sessionID)
	if err == nil {
		m.onList(msgs)
	}
	return msgs, err
}

func TestProcessGeneration_BackgroundFinalOutputScanCatchesLastCompletion(t *testing.T) {
	ctx, reg, mon := backgroundWaitSetup(t)
	p := &recordingProvider{scriptedProvider: &scriptedProvider{respond: func(call int, _ bool) *provider.ProviderResponse {
		switch call {
		case 1:
			return structOutputTurnWith("old-final", "before-completion")
		case 2:
			return toolTurn(message.ToolCall{ID: "inspect-final", Name: "independent", Input: `{}`, Finished: true})
		case 3:
			return structOutputTurnWith("new-final", "after-completion")
		}
		return endTurn()
	}}}
	inspections := 0
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{
		testStructOutputTool(),
		backgroundIndependentTool{onRun: func() error { inspections++; return nil }},
	})
	scannedOutput := false
	a.messages = &backgroundListObserver{Service: a.messages, onList: func(msgs []message.Message) {
		for _, m := range msgs {
			for _, result := range m.ToolResultsByToolName(tools.StructOutputToolName) {
				if result.ToolCallID == "old-final" {
					scannedOutput = true
				}
			}
		}
	}}
	completedAtCheck := false
	wrapped := &backgroundPendingHookRegistry{Registry: reg, onPending: func() {
		// appendBackgroundReports has just scanned the accepted output;
		// publish the final completion before the pending check returns zero.
		if !scannedOutput {
			t.Error("completion hook fired before the tool-boundary report scan")
		}
		if err := persistBackgroundEvent(ctx, a, reg, mon.SessionID, "last-completion"); err != nil {
			t.Error(err)
		}
		reg.MarkFinished(mon.ID, task.StateCompleted, nil)
		completedAtCheck = true
	}}
	task.ResetGlobalRegistry()
	task.SetGlobalRegistry(wrapped)
	result := awaitBackgroundLoop(t, ctx, runBackgroundLoop(a, ctx, mon.SessionID, true))
	if result.Error != nil || !completedAtCheck || p.callCount() != 3 || inspections != 1 {
		t.Fatalf("error=%v, hook=%v, calls=%d, inspections=%d", result.Error, completedAtCheck, p.callCount(), inspections)
	}
	if result.StructOutput == nil || result.StructOutput.IsError || !strings.Contains(result.StructOutput.Content, "after-completion") {
		t.Fatalf("output=%+v, want the updated final output", result.StructOutput)
	}
	assertBackgroundEvent(t, p.request(2), "last-completion", 1)
	if !hasToolResult(p.request(3), "work complete") {
		t.Fatal("updated final output did not observe the inspection")
	}
}

func TestProcessGeneration_BackgroundReportReopensAcceptedOutput(t *testing.T) {
	for _, arrival := range []string{"outer yield", "outer end turn", "yield tool boundary"} {
		t.Run(arrival, func(t *testing.T) {
			ctx, reg, mon := backgroundWaitSetup(t)
			var a *agent
			inject := func() {
				if err := persistBackgroundEvent(ctx, a, reg, mon.SessionID, "reopen"); err != nil {
					t.Error(err)
				}
				reg.MarkFinished(mon.ID, task.StateCompleted, nil)
			}
			if arrival != "yield tool boundary" {
				task.ResetGlobalRegistry()
				task.SetGlobalRegistry(&backgroundWatchHookRegistry{Registry: reg, onWatch: inject})
			}
			p := &recordingProvider{scriptedProvider: &scriptedProvider{respond: func(call int, _ bool) *provider.ProviderResponse {
				switch call {
				case 1:
					return structOutputTurnWith("old-output", "before-report")
				case 2:
					if arrival == "yield tool boundary" {
						inject()
					}
					if arrival == "outer end turn" {
						return endTurn()
					}
					return yieldBackgroundTurn("output-yield")
				case 3:
					return toolTurn(message.ToolCall{ID: "inspect", Name: "independent", Input: `{}`, Finished: true})
				case 4:
					return structOutputTurnWith("updated-output", "after-inspection")
				}
				return endTurn()
			}}}
			inspections := 0
			work := backgroundIndependentTool{onRun: func() error { inspections++; return nil }}
			a = newLoopAgentWithTools(t, p, []tools.BaseTool{tools.NewYieldTool(), testStructOutputTool(), work})
			result := awaitBackgroundLoop(t, ctx, runBackgroundLoop(a, ctx, mon.SessionID, true))
			if result.Error != nil || p.callCount() != 4 || inspections != 1 {
				t.Fatalf("error=%v, calls=%d, inspections=%d; want four calls and one inspection", result.Error, p.callCount(), inspections)
			}
			if result.StructOutput == nil || result.StructOutput.IsError || !strings.Contains(result.StructOutput.Content, "after-inspection") {
				t.Fatalf("output=%+v, want replacement after inspection", result.StructOutput)
			}
			assertBackgroundEvent(t, p.request(3), "reopen", 1)
			if !hasToolResult(p.request(4), "work complete") {
				t.Fatal("updated output request did not receive inspection result")
			}
		})
	}
}

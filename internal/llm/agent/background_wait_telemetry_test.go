package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/opencode-ai/opencode/internal/inbox"
	"github.com/opencode-ai/opencode/internal/langfuse"
	"github.com/opencode-ai/opencode/internal/task"
)

func TestBackgroundWaitTelemetry(t *testing.T) {
	for _, outcome := range []string{"background_report", "inbox", "no_pending_tasks", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			recorder := setLangfuseClient(t, true)
			ctx, reg, mon := backgroundWaitSetup(t)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			ctx = langfuse.Get().TraceStart(ctx, langfuse.TraceParams{Name: "parent"})
			defer langfuse.Get().TraceEnd(ctx)
			a := newLoopAgentWithTools(t, &scriptedProvider{}, nil)
			seen := map[string]bool{}
			if outcome == "no_pending_tasks" {
				reg.MarkFinished(mon.ID, task.StateCompleted, nil)
			}
			done := make(chan error, 1)
			go func() {
				_, err := a.waitForBackgroundReport(ctx, reg, mon.SessionID, seen, "yield")
				done <- err
			}()
			if outcome != "no_pending_tasks" {
				// Wait until the span is started, then supply the event. The
				// agent must stay out of model generation while suspended.
				deadline := time.Now().Add(time.Second)
				for len(recorder.Started()) < 3 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				switch outcome {
				case "background_report":
					if err := persistBackgroundEvent(ctx, a, reg, mon.SessionID, "report"); err != nil {
						t.Fatal(err)
					}
				case "inbox":
					inbox.Default().Push(mon.SessionID, nil, inbox.Item{Text: "reviewer"})
				case "cancelled":
					cancel()
				}
			}
			select {
			case err := <-done:
				if outcome != "cancelled" && err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("wait did not resume")
			}
			inbox.Default().Clear(mon.SessionID)
			var span sdktrace.ReadOnlySpan
			for _, s := range recorder.Ended() {
				if s.Name() == "background_wait" {
					span = s
				}
			}
			if span == nil {
				t.Fatal("missing wait observation")
			}
			var output struct {
				Reason  string  `json:"wake_reason"`
				MS      float64 `json:"suspended_ms"`
				Reports int     `json:"reports"`
			}
			for _, attr := range span.Attributes() {
				if attr.Key == "langfuse.observation.output" {
					if err := json.Unmarshal([]byte(attr.Value.AsString()), &output); err != nil {
						t.Fatal(err)
					}
				}
				if attr.Key == "langfuse.observation.metadata.wake_reason" && attr.Value.AsString() != outcome {
					t.Fatalf("wrong filterable wake reason: %v", attr)
				}
			}
			if output.Reason != outcome || outcome == "no_pending_tasks" && output.MS != 0 {
				t.Fatalf("wrong wait result: %+v", output)
			}
			if outcome == "background_report" && output.Reports != 1 {
				t.Fatalf("report count: %+v", output)
			}
		})
	}
}

func TestBackgroundWaitAlreadyReadyTelemetry(t *testing.T) {
	recorder := setLangfuseClient(t, true)
	withFreshTaskRegistry(t)
	ctx := langfuse.Get().TraceStart(context.Background(), langfuse.TraceParams{Name: "parent"})
	defer langfuse.Get().TraceEnd(ctx)
	recordReadyBackgroundWait(ctx, "session", 1)
	for _, span := range recorder.Ended() {
		if span.Name() != "background_wait" {
			continue
		}
		foundMS, foundReason := false, false
		for _, attr := range span.Attributes() {
			switch attr.Key {
			case "langfuse.observation.metadata.suspended_ms":
				foundMS = true
				if attr.Value.AsFloat64() != 0 {
					t.Fatalf("ready report was counted as suspension: %v", attr)
				}
			case "langfuse.observation.metadata.wake_reason":
				foundReason = attr.Value.AsString() == "background_report"
			}
		}
		if !foundMS || !foundReason {
			t.Fatalf("missing ready report metadata: %v", span.Attributes())
		}
		return
	}
	t.Fatal("missing ready report observation")
}

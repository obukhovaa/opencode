package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/message"
)

func TestSummaryStreamStall_BoundedAtEveryEntryPoint(t *testing.T) {
	for _, entry := range []string{"recap", "sync", "manual"} {
		for _, outcome := range []string{"recovered", "exhausted", "other error", "cancelled"} {
			t.Run(entry+"/"+outcome, func(t *testing.T) {
				withAutoCompact(t, false)
				a, _, summary, _ := newCompactingAgent(t, 100, nil)
				const sess = "summary-stall"
				seedHistory(t, a, sess, textMsg(message.User, "old request"), textMsg(message.Assistant, "old answer"))
				seedReport(t, a, sess, 100)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				failures := map[int]error{1: provider.ErrStreamStalled, 2: provider.ErrStreamStalled}
				wantCalls := 3
				var wantErr error
				switch outcome {
				case "exhausted":
					failures[3] = provider.ErrStreamStalled
					wantErr = provider.ErrStreamStalled
				case "other error":
					wantErr = errors.New("provider rejected request")
					failures = map[int]error{1: wantErr}
					wantCalls = 1
				case "cancelled":
					wantErr = provider.ErrStreamStalled
					wantCalls = 1
				}
				p := &midStreamResetProvider{compactionProvider: summary, resets: failures}
				if outcome == "cancelled" {
					p.onReset = cancel
				}
				a.summarizeProvider = p
				var err error
				switch entry {
				case "recap":
					a.messages = &stubMessageService{msgs: makeMessages(recapMinMessages)}
					var result string
					result, err = a.GenerateRecap(ctx, sess)
					if wantErr == nil && result != testSummaryText {
						t.Fatalf("recap=%q", result)
					}
				case "sync":
					err = a.SummarizeSync(ctx, sess)
				case "manual":
					// The subscription context must outlive cancellation of the request.
					watchCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
					defer stop()
					events := a.Subscribe(watchCtx)
					if err = a.Summarize(ctx, sess); err != nil {
						t.Fatal(err)
					}
					done := false
					for event := range events {
						if event.Payload.Done {
							err = event.Payload.Error
							done = true
							break
						}
					}
					if !done {
						t.Fatal("manual summary did not finish")
					}
				}
				if !errors.Is(err, wantErr) {
					t.Fatalf("error=%v, want %v", err, wantErr)
				}
				if p.requests != wantCalls {
					t.Fatalf("calls=%d, want %d", p.requests, wantCalls)
				}
				for _, req := range summary.requests[1:] {
					if !reflect.DeepEqual(req, summary.requests[0]) {
						t.Fatal("summary retry changed input or retained partial output")
					}
				}
			})
		}
	}
}

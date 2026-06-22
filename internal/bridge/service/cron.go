package service

import (
	"context"
	"fmt"

	"github.com/opencode-ai/opencode/internal/cron"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// CronOutputRouter subscribes to cron.Service events and forwards each
// successful run's result back to every peer bound to the cron's session.
//
// Without this, the synthetic tool_call/tool_result pair the cron
// scheduler writes via messages.CreatePair lands in session history but
// never reaches the chat surface — the bridge dispatcher only attaches
// a messages.SubscribeParts subscription for the lifetime of a single
// inbound turn (see dispatch.go::handleInbound). Background-generated
// messages from cron, scheduled flows, or any other out-of-turn writer
// have no path out without a persistent subscriber like this one.
//
// Implementation note: this mirrors PermissionRouter / QuestionRouter —
// subscribe to a service's pubsub broker, filter to bridge-owned
// sessions, and forward via the existing per-binding adapter Send path.
type CronOutputRouter struct {
	svc *Service
}

// newCronOutputRouter constructs the router and launches its subscriber
// goroutine when the cron service is available. A nil app or nil
// app.Crons (e.g. OPENCODE_DISABLE_CRON is set) leaves the router
// dormant — there are no cron events to consume.
func (s *Service) newCronOutputRouter() *CronOutputRouter {
	r := &CronOutputRouter{svc: s}
	if s.app == nil || s.app.Crons == nil {
		return r
	}
	s.launchSupervised("cron-output-router", r.run)
	return r
}

// run subscribes to cron.Service.Subscribe and dispatches each
// successful-completion event. Exits when the service ctx ends.
func (r *CronOutputRouter) run(ctx context.Context) {
	sub := r.svc.app.Crons.Subscribe(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub:
			if !ok {
				return
			}
			// The cron service publishes UpdatedEvent only from
			// UpdateAfterRun (successful completion) — error/firing-flag
			// flips do not republish. Created/Deleted events for the
			// listing UI are not interesting to chat forwarding.
			if ev.Type != pubsub.UpdatedEvent {
				continue
			}
			r.handleUpdate(ctx, ev.Payload)
		}
	}
}

// handleUpdate forwards a successful cron run to every peer bound to
// the job's session. Multiple peers (group chat fan-out) get a copy
// each, mirroring QuestionRouter's fan-out behaviour. Sessions with no
// bindings are silently skipped — the cron belongs to a TUI/API user,
// not the chat surface.
func (r *CronOutputRouter) handleUpdate(ctx context.Context, job cron.CronJob) {
	// Defensive: only act when the row has a fresh result. A future
	// publisher that emits UpdatedEvent without a fresh run would
	// otherwise re-deliver a stale result. LastRunAt + non-empty
	// LastResult together mean "the most recent run wrote output".
	if job.LastRunAt == 0 || job.LastResult == "" {
		return
	}
	bindings, err := r.svc.store.ListBindingsBySession(ctx, r.svc.projectID, job.SessionID)
	if err != nil {
		logging.Warn("bridge: cron output binding lookup",
			"session", job.SessionID, "id", job.ID, "err", err)
		return
	}
	if len(bindings) == 0 {
		return
	}
	title := job.TaskTitle
	if title == "" {
		title = job.ID
	}
	body := fmt.Sprintf("⏲ %s\n\n%s", title, job.LastResult)
	for _, b := range bindings {
		r.svc.replyToPeer(ctx, b.AsPeerRef(), body)
	}
	logging.Info("bridge: cron output forwarded",
		"session", job.SessionID, "id", job.ID, "peers", len(bindings))
}

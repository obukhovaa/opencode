package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
)

// QueuedMessage is a user message waiting to be delivered to the agent after
// the current run completes. It lives in memory only; it is never persisted to
// the database before delivery. Persisting before delivery would make
// agent-loop compaction (agent.go ~line 945) and the non-interactive reload
// (~line 1244) sweep the message into the in-flight run non-deterministically.
type QueuedMessage struct {
	Text        string
	Attachments []message.Attachment
}

// DrainEvent is emitted by the drain worker and EnqueueMessage to inform the
// TUI of queue-state changes. The TUI receives it via the registered
// DrainNotifier (see SetDrainNotifier).
type DrainEvent struct {
	// SessionID identifies the affected session.
	SessionID string
	// QueueLen is the current queue length after the event.
	QueueLen int
	// Err is non-nil when the drain worker encountered a non-retryable error.
	// When non-nil the drain worker has halted; remaining messages are still
	// in the queue and visible via QueueLen.
	Err error
}

// SetDrainNotifier registers the callback that the drain worker calls to push
// DrainEvents to the TUI. Must be called once, from the TUI goroutine, before
// the first EnqueueMessage. The notifier is called from drain-worker goroutines
// and must be safe for concurrent use (e.g. program.Send).
func (app *App) SetDrainNotifier(fn func(DrainEvent)) {
	app.queueMu.Lock()
	defer app.queueMu.Unlock()
	app.drainNotify = fn
}

// notify delivers a DrainEvent to the registered notifier (if any). Must be
// called without queueMu held.
func (app *App) notify(e DrainEvent) {
	app.queueMu.Lock()
	fn := app.drainNotify
	app.queueMu.Unlock()
	if fn != nil {
		fn(e)
	}
}

// EnqueueMessage appends msg to sessionID's in-memory queue and starts a drain
// worker if one is not already running. Goroutine-safe.
func (app *App) EnqueueMessage(sessionID string, msg QueuedMessage) {
	app.queueMu.Lock()
	app.queues[sessionID] = append(app.queues[sessionID], msg)
	qLen := len(app.queues[sessionID])
	if _, running := app.queueCancels[sessionID]; !running {
		app.startDrainWorker(sessionID)
	}
	app.queueMu.Unlock()
	app.notify(DrainEvent{SessionID: sessionID, QueueLen: qLen})
}

// DequeueMessage pops the head of sessionID's queue. Returns (msg, true) when
// a message was available, (zero, false) when the queue is empty.
// Goroutine-safe.
func (app *App) DequeueMessage(sessionID string) (QueuedMessage, bool) {
	app.queueMu.Lock()
	defer app.queueMu.Unlock()
	q := app.queues[sessionID]
	if len(q) == 0 {
		return QueuedMessage{}, false
	}
	msg := q[0]
	app.queues[sessionID] = q[1:]
	return msg, true
}

// QueueLen returns the current queue depth for sessionID. Goroutine-safe.
func (app *App) QueueLen(sessionID string) int {
	app.queueMu.Lock()
	defer app.queueMu.Unlock()
	return len(app.queues[sessionID])
}

// DiscardQueue empties the queue for sessionID and notifies the TUI.
// Goroutine-safe.
func (app *App) DiscardQueue(sessionID string) {
	app.queueMu.Lock()
	app.queues[sessionID] = nil
	app.queueMu.Unlock()
	app.notify(DrainEvent{SessionID: sessionID, QueueLen: 0})
}

// prepend re-inserts msg at the head of sessionID's queue. Must be called
// under queueMu.
func (app *App) prepend(sessionID string, msg QueuedMessage) {
	app.queues[sessionID] = append([]QueuedMessage{msg}, app.queues[sessionID]...)
}

// startDrainWorker spawns a new drain goroutine for sessionID. Must be called
// under queueMu; the caller must have verified no worker is running.
func (app *App) startDrainWorker(sessionID string) {
	ctx, cancel := context.WithCancel(app.ctx)
	app.queueCancels[sessionID] = cancel
	app.queueWg.Add(1)
	go func() {
		defer app.queueWg.Done()
		app.drainLoop(ctx, sessionID)
	}()
}

// drainLoop is the body of the per-session drain worker goroutine. It dequeues
// messages one at a time and delivers each via agent.Run. It exits when:
//   - the queue empties (normal completion);
//   - a non-ErrSessionBusy error is returned by Run (halt after surfacing error);
//   - its context is cancelled (app shutdown or explicit stop).
//
// ErrSessionBusy is the only swallowed error — the message is re-prepended and
// the worker backs off for 100 ms before retrying. All other errors are surfaced
// through the TUI via the registered DrainNotifier and halt the worker, leaving
// the remaining queue intact so the user can discard or allow a fresh drain.
func (app *App) drainLoop(ctx context.Context, sessionID string) {
	const busyBackoff = 100 * time.Millisecond

	for {
		// Respect context cancellation between attempts.
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, ok := app.DequeueMessage(sessionID)
		if !ok {
			// Queue is empty — exit worker and clean up.
			app.queueMu.Lock()
			delete(app.queueCancels, sessionID)
			app.queueMu.Unlock()
			app.notify(DrainEvent{SessionID: sessionID, QueueLen: 0})
			return
		}

		ag := app.ActiveAgent()
		if ag == nil {
			// No agent available yet — re-prepend and wait briefly.
			app.queueMu.Lock()
			app.prepend(sessionID, msg)
			app.queueMu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(busyBackoff):
			}
			continue
		}

		// Non-authoritative optimisation: skip a pointless Run call when the
		// session is observably busy. The authoritative exclusivity mechanism
		// is the atomic LoadOrStore inside RunWith; ErrSessionBusy from Run is
		// the correct retry signal — not this check.
		if ag.IsSessionBusy(sessionID) {
			app.queueMu.Lock()
			app.prepend(sessionID, msg)
			app.queueMu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(busyBackoff):
			}
			continue
		}

		events, err := ag.Run(ctx, sessionID, msg.Text, 0, msg.Attachments...)
		if err != nil {
			if errors.Is(err, agent.ErrSessionBusy) {
				// Lost the acquire race — re-prepend and back off.
				app.queueMu.Lock()
				app.prepend(sessionID, msg)
				app.queueMu.Unlock()
				select {
				case <-ctx.Done():
					return
				case <-time.After(busyBackoff):
				}
				continue
			}

			// Non-retryable error: surface with attribution, halt worker,
			// preserve remaining queue (including the failed message at head).
			app.queueMu.Lock()
			app.prepend(sessionID, msg)
			remaining := len(app.queues[sessionID])
			delete(app.queueCancels, sessionID)
			app.queueMu.Unlock()
			app.notify(DrainEvent{
				SessionID: sessionID,
				QueueLen:  remaining,
				Err:       fmt.Errorf("queued message could not be delivered: %w", err),
			})
			logging.Warn("drain worker halted after error",
				"session", sessionID, "error", err, "remaining", remaining)
			return
		}

		// Drain the events channel so the agent's panic-recover path can
		// complete and release the busy lock. Select on ctx.Done() so a
		// shutdown signal is not missed while the channel is open.
	drainEvents:
		for {
			select {
			case _, ok := <-events:
				if !ok {
					break drainEvents
				}
			case <-ctx.Done():
				return
			}
		}

		qLen := app.QueueLen(sessionID)
		app.notify(DrainEvent{SessionID: sessionID, QueueLen: qLen})
	}
}

// ShutdownQueues cancels all live drain workers and blocks until they exit.
// Called from App.Shutdown to prevent goroutine leaks.
func (app *App) ShutdownQueues() {
	app.queueMu.Lock()
	for _, cancel := range app.queueCancels {
		cancel()
	}
	app.queueMu.Unlock()
	app.queueWg.Wait()
}

// NewForTest creates a minimal App for unit tests. The returned App has the
// queue subsystem and active agent initialized; all other services are nil.
// It is only for use in tests and must not be called in production code.
func NewForTest(ctx context.Context, ag agent.Service) *App {
	return &App{
		ctx:          ctx,
		queues:       make(map[string][]QueuedMessage),
		queueCancels: make(map[string]context.CancelFunc),
		activeAgent:  ag,
	}
}

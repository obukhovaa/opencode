package cmd

import (
	"sync"

	tea "charm.land/bubbletea/v2"
	appPkg "github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/logging"
)

// drainEventBuffer is the depth of the forwarder queue. Drain events are
// low-rate (one per enqueue / delivery / discard), so this is generous enough
// that the out-of-band spill path below is effectively unreachable.
const drainEventBuffer = 128

// newDrainForwarder adapts App.SetDrainNotifier to tea.Program.Send.
//
// It exists because Program.Send MUST NOT be called from the Bubble Tea update
// goroutine: p.msgs is unbuffered and is only read by the event loop, which is
// itself blocked inside Model.Update while the update runs. A bare
// `program.Send(e)` notifier therefore deadlocks the entire TUI permanently the
// first time a user submits a message while the agent is busy — the editor's
// enqueue path (chat/editor.go send) and the ctrl+x discard path both call into
// App.EnqueueMessage / App.DiscardQueue synchronously from Update, and those
// notify the registered callback before returning. Bubble Tea itself always
// wraps internal self-sends in `go p.Send(...)` for the same reason.
//
// The returned notify never blocks its caller and preserves event order: events
// are handed to a single forwarder goroutine over a buffered channel, mirroring
// how service subscriptions reach the TUI (see setupSubscriptions). stop halts
// the forwarder and waits for it to exit.
func newDrainForwarder(send func(tea.Msg)) (notify func(appPkg.DrainEvent), stop func()) {
	events := make(chan appPkg.DrainEvent, drainEventBuffer)
	stopped := make(chan struct{})
	exited := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		defer close(exited)
		defer logging.RecoverPanic("TUI-drain-forwarder", nil)

		for {
			select {
			case <-stopped:
				return
			case e := <-events:
				send(e)
			}
		}
	}()

	notify = func(e appPkg.DrainEvent) {
		select {
		case events <- e:
		case <-stopped:
		default:
			// Buffer full: hand off to a goroutine rather than blocking the
			// caller, which may be the Bubble Tea update goroutine. Delivery
			// is preserved (error events are the only signal a halted drain
			// worker ever emits) at the cost of ordering under backpressure —
			// harmless, since the queue banner re-reads QueueLen in View.
			logging.Warn("drain event forwarder buffer full, delivering out of band",
				"session", e.SessionID)
			go func() {
				select {
				case events <- e:
				case <-stopped:
				}
			}()
		}
	}

	stop = func() {
		stopOnce.Do(func() { close(stopped) })
		<-exited
	}

	return notify, stop
}

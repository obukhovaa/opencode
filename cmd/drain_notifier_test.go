package cmd

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	appPkg "github.com/opencode-ai/opencode/internal/app"
)

// Regression test for the queue-message freeze: the notifier registered on the
// App is invoked synchronously from the Bubble Tea update goroutine (editor
// enqueue, ctrl+x discard). If it forwards straight to tea.Program.Send it
// blocks forever, because Send writes to an unbuffered channel that only the
// event loop reads — and the event loop is inside Update, waiting for the
// notifier to return. The forwarder must never block its caller.
func TestDrainForwarder_NotifyNeverBlocksCaller(t *testing.T) {
	release := make(chan struct{})
	var sent []appPkg.DrainEvent
	var mu sync.Mutex

	blockingSend := func(msg tea.Msg) {
		<-release // stands in for the wedged event loop
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, msg.(appPkg.DrainEvent))
	}

	notify, stop := newDrainForwarder(blockingSend)
	defer func() {
		close(release)
		stop()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < drainEventBuffer+8; i++ {
			notify(appPkg.DrainEvent{SessionID: "s1", QueueLen: i})
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("notify blocked while the consumer was stalled — the TUI would freeze")
	}
}

// Once the event loop is free again, buffered events are delivered in order.
func TestDrainForwarder_DeliversInOrder(t *testing.T) {
	const n = 16

	got := make(chan int, n)
	notify, stop := newDrainForwarder(func(msg tea.Msg) {
		got <- msg.(appPkg.DrainEvent).QueueLen
	})
	defer stop()

	for i := 0; i < n; i++ {
		notify(appPkg.DrainEvent{SessionID: "s1", QueueLen: i})
	}

	for i := 0; i < n; i++ {
		select {
		case v := <-got:
			if v != i {
				t.Fatalf("event %d delivered out of order: got QueueLen=%d", i, v)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("event %d never delivered", i)
		}
	}
}

// stop is idempotent and safe to call while notify is still being used, and
// post-stop notifies are dropped rather than panicking on a closed channel.
func TestDrainForwarder_StopIsSafe(t *testing.T) {
	notify, stop := newDrainForwarder(func(tea.Msg) {})

	notify(appPkg.DrainEvent{SessionID: "s1"})
	stop()
	stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		notify(appPkg.DrainEvent{SessionID: "s1"})
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("notify blocked after stop")
	}
}

// End-to-end version of the freeze against a real tea.Program: notifying from
// inside Model.Update (what EnqueueMessage / DiscardQueue do) must return. With
// a raw `program.Send` notifier this test hangs until the timeout, which is the
// exact user-visible bug — an unrecoverable TUI freeze.
func TestDrainForwarder_NotifyFromUpdateDoesNotDeadlock(t *testing.T) {
	m := &forwarderProbeModel{
		entered:  make(chan struct{}, 1),
		returned: make(chan struct{}, 1),
	}
	program := tea.NewProgram(m,
		tea.WithInput(strings.NewReader("")),
		tea.WithOutput(io.Discard),
	)
	notify, stop := newDrainForwarder(program.Send)
	defer stop()
	m.notify = notify

	go func() { _, _ = program.Run() }()
	// Quit is itself a Send, so it deadlocks too when the bug is present —
	// fire it off-goroutine so a regression fails on the timeouts below
	// instead of hanging the whole test binary.
	defer func() { go program.Quit() }()

	select {
	case <-m.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Update never ran")
	}
	select {
	case <-m.returned:
	case <-time.After(3 * time.Second):
		t.Fatal("notify from inside Update never returned — the TUI is deadlocked")
	}
}

type forwarderProbeMsg struct{}

type forwarderProbeModel struct {
	notify   func(appPkg.DrainEvent)
	entered  chan struct{}
	returned chan struct{}
}

func (m *forwarderProbeModel) Init() tea.Cmd {
	return func() tea.Msg { return forwarderProbeMsg{} }
}

func (m *forwarderProbeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(forwarderProbeMsg); ok {
		m.entered <- struct{}{}
		m.notify(appPkg.DrainEvent{SessionID: "s1", QueueLen: 1})
		m.returned <- struct{}{}
	}
	return m, nil
}

func (m *forwarderProbeModel) View() tea.View { return tea.NewView("") }

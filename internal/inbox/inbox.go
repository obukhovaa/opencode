// Package inbox holds reviewer input that reached an interactive flow
// step's session while its agent was busy, until the agent loop drains it
// into the session as user messages.
//
// It is a leaf package so that both its producer (the chat bridge) and its
// consumer (the agent loop) can use it without importing each other. The
// bridge pushes input and registers a per-session delivery hook; the agent
// loop drains the queue at its tool and turn boundaries and reports what
// it stored through Delivered, which fires that hook.
package inbox

import "sync"

// DefaultCap bounds one session's queue. A push past it drops the oldest
// items, which Push returns so the producer can tell their senders.
const DefaultCap = 16

// Item is one piece of queued input.
type Item struct {
	// ID is the sender's message id, empty for input that has none (a chat
	// message typed on Slack or Mattermost). Input with an id is
	// acknowledged once delivered; input without one is not.
	ID string
	// Text is the input as the agent reads it, attribution included.
	Text string
	// Meta is the producer's own record of the input (the bridge stores the
	// original inbound there). The inbox never reads it.
	Meta any
}

// DeliveredFunc is told which items became part of the session, in order.
type DeliveredFunc func(sessionID string, items []Item)

// Box is a set of per-session FIFO queues. The zero value is not usable;
// call New.
type Box struct {
	mu     sync.Mutex
	cap    int
	queues map[string][]Item
	hooks  map[string]DeliveredFunc
	drains map[string]map[*drainBatch]struct{}
}

type drainBatch struct {
	items []Item
}

// New returns an empty Box whose queues hold at most capacity items each
// (DefaultCap when capacity < 1).
func New(capacity int) *Box {
	if capacity < 1 {
		capacity = DefaultCap
	}
	return &Box{
		cap:    capacity,
		queues: make(map[string][]Item),
		hooks:  make(map[string]DeliveredFunc),
		drains: make(map[string]map[*drainBatch]struct{}),
	}
}

var defaultBox = New(DefaultCap)

// Default is the process-wide Box shared by the bridge and the agent loop.
func Default() *Box { return defaultBox }

// Push appends items to the session's queue as one unit, so no concurrent
// push can interleave with them. When the queue would exceed its cap the
// oldest items are dropped and returned. onDelivered, when non-nil,
// replaces the session's delivery hook.
func (b *Box) Push(sessionID string, onDelivered DeliveredFunc, items ...Item) (evicted []Item) {
	if len(items) == 0 && onDelivered == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if onDelivered != nil {
		b.hooks[sessionID] = onDelivered
	}
	if len(items) == 0 {
		return nil
	}
	q := append(b.queues[sessionID], items...)
	if over := len(q) - b.cap; over > 0 {
		evicted = append([]Item(nil), q[:over]...)
		q = q[over:]
	}
	b.queues[sessionID] = append([]Item(nil), q...)
	return evicted
}

// PopFirst removes and returns the oldest item of the session's queue.
func (b *Box) PopFirst(sessionID string) (Item, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.queues[sessionID]
	if len(q) == 0 {
		return Item{}, false
	}
	it := q[0]
	if len(q) == 1 {
		delete(b.queues, sessionID)
	} else {
		b.queues[sessionID] = q[1:]
	}
	return it, true
}

// DrainAll removes and returns every item of the session's queue, oldest
// first. The session's delivery hook stays registered.
func (b *Box) DrainAll(sessionID string) []Item {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.queues[sessionID]
	delete(b.queues, sessionID)
	return q
}

// DrainForDelivery removes the queue while keeping its items visible to
// AnyPending. The caller must call finish after persistence and Delivered
// bookkeeping, including when persistence fails, so failed items can be retried.
func (b *Box) DrainForDelivery(sessionID string) (items []Item, finish func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	items = b.queues[sessionID]
	delete(b.queues, sessionID)
	if len(items) == 0 {
		return nil, func() {}
	}
	batch := &drainBatch{items: items}
	if b.drains[sessionID] == nil {
		b.drains[sessionID] = make(map[*drainBatch]struct{})
	}
	b.drains[sessionID][batch] = struct{}{}
	return items, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.drains[sessionID], batch)
		if len(b.drains[sessionID]) == 0 {
			delete(b.drains, sessionID)
		}
	}
}

// Clear drops the session's queue and its delivery hook and returns the
// items that were still waiting. Nothing it returns is ever acknowledged.
func (b *Box) Clear(sessionID string) []Item {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.queues[sessionID]
	delete(b.queues, sessionID)
	delete(b.hooks, sessionID)
	delete(b.drains, sessionID)
	return q
}

// Len reports how many items wait for the session.
func (b *Box) Len(sessionID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queues[sessionID])
}

// Any reports whether an item waiting for the session satisfies pred.
func (b *Box) Any(sessionID string, pred func(Item) bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, it := range b.queues[sessionID] {
		if pred(it) {
			return true
		}
	}
	return false
}

// AnyPending checks queued items and items being persisted by a delivery
// drain. In-flight items do not count towards Len or answer new questions.
func (b *Box) AnyPending(sessionID string, pred func(Item) bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, it := range b.queues[sessionID] {
		if pred(it) {
			return true
		}
	}
	for batch := range b.drains[sessionID] {
		for _, it := range batch.items {
			if pred(it) {
				return true
			}
		}
	}
	return false
}

// Delivered tells the session's delivery hook that items became part of
// the session. It is a no-op for an empty list or a session with no hook.
// The hook runs on the caller's goroutine, outside the Box's lock.
func (b *Box) Delivered(sessionID string, items []Item) {
	if len(items) == 0 {
		return
	}
	b.mu.Lock()
	hook := b.hooks[sessionID]
	b.mu.Unlock()
	if hook != nil {
		hook(sessionID, items)
	}
}

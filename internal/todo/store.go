package todo

import "sync"

// Item represents a single task in the todo list.
type Item struct {
	Content  string `json:"content"`
	Status   string `json:"status"`   // pending, in_progress, completed, cancelled
	Priority string `json:"priority"` // high, medium, low
}

// Store is an in-memory, session-scoped todo list store.
// Thread-safe for concurrent access.
type Store struct {
	mu    sync.RWMutex
	items map[string][]Item // sessionID -> items
}

// NewStore creates a new empty Store.
func NewStore() *Store {
	return &Store{
		items: make(map[string][]Item),
	}
}

// Set replaces the entire todo list for a session.
func (s *Store) Set(sessionID string, items []Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(items) == 0 {
		delete(s.items, sessionID)
		return
	}
	cp := make([]Item, len(items))
	copy(cp, items)
	s.items[sessionID] = cp
}

// Get returns a copy of the todo list for a session.
// Returns nil if no items exist.
func (s *Store) Get(sessionID string) []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items, ok := s.items[sessionID]
	if !ok {
		return nil
	}
	cp := make([]Item, len(items))
	copy(cp, items)
	return cp
}

// Delete removes all todos for a session.
func (s *Store) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, sessionID)
}

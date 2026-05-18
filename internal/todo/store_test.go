package todo

import (
	"sync"
	"testing"
)

func TestStore_SetGet(t *testing.T) {
	s := NewStore()

	items := []Item{
		{Content: "task 1", Status: "pending", Priority: "high"},
		{Content: "task 2", Status: "in_progress", Priority: "medium"},
	}

	s.Set("sess1", items)
	got := s.Get("sess1")

	if len(got) != 2 {
		t.Fatalf("expected 2 items, got %d", len(got))
	}
	if got[0].Content != "task 1" || got[1].Status != "in_progress" {
		t.Fatalf("unexpected items: %+v", got)
	}

	// Mutation of returned slice should not affect store
	got[0].Content = "mutated"
	fresh := s.Get("sess1")
	if fresh[0].Content != "task 1" {
		t.Fatal("store was mutated via returned slice")
	}
}

func TestStore_GetEmpty(t *testing.T) {
	s := NewStore()
	if got := s.Get("nonexistent"); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestStore_SetEmpty(t *testing.T) {
	s := NewStore()
	s.Set("sess1", []Item{{Content: "x", Status: "pending", Priority: "low"}})
	s.Set("sess1", nil)
	if got := s.Get("sess1"); got != nil {
		t.Fatalf("expected nil after clearing, got %v", got)
	}
}

func TestStore_Delete(t *testing.T) {
	s := NewStore()
	s.Set("sess1", []Item{{Content: "x", Status: "pending", Priority: "low"}})
	s.Delete("sess1")
	if got := s.Get("sess1"); got != nil {
		t.Fatalf("expected nil after delete, got %v", got)
	}
}

func TestStore_Concurrent(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			s.Set("sess", []Item{{Content: "task", Status: "pending", Priority: "medium"}})
		}()
		go func() {
			defer wg.Done()
			_ = s.Get("sess")
		}()
		go func() {
			defer wg.Done()
			s.Delete("sess")
		}()
	}

	wg.Wait()
}

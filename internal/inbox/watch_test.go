package inbox

import (
	"sync"
	"testing"
)

func assertWatchState(t *testing.T, ch <-chan struct{}, closed bool) {
	t.Helper()
	if ch == nil {
		t.Fatal("nil watch")
	}
	select {
	case <-ch:
		if !closed {
			t.Fatal("watch closed unexpectedly")
		}
	default:
		if closed {
			t.Fatal("watch did not close")
		}
	}
}

func TestBox_WatchBroadcast(t *testing.T) {
	for _, front := range []bool{false, true} {
		name := "Push"
		if front {
			name = "PushFront"
		}
		t.Run(name, func(t *testing.T) {
			b := New(0)
			first, second := b.Watch("s"), b.Watch("s")
			other := b.Watch("other")
			if first != second {
				t.Fatal("listeners did not share the same broadcast")
			}
			for i := 0; i < 2; i++ {
				if front {
					b.PushFront("s", Item{Text: "input"})
				} else {
					b.Push("s", nil, Item{Text: "input"})
				}
				assertWatchState(t, first, true)
				assertWatchState(t, second, true)
				assertWatchState(t, other, false)
				next := b.Watch("s")
				if next == first {
					t.Fatal("broadcast was not replaced")
				}
				assertWatchState(t, next, false)
				first, second = next, b.Watch("s")
			}
		})
	}
}

func TestBox_WatchEmptyPush(t *testing.T) {
	b := New(0)
	b.Push("s", nil, Item{Text: "already queued"})
	b.PushFront("s")
	b.Clear("absent")
	if b.watches != nil {
		t.Fatal("operations without listeners allocated watches")
	}
	ch := b.Watch("s")
	b.Push("s", nil)
	b.Push("s", func(string, []Item) {})
	b.PushFront("s")
	assertWatchState(t, ch, false)
	if b.Watch("s") != ch {
		t.Fatal("empty push replaced the watch")
	}
}

func TestBox_WatchQueueCheckOrdering(t *testing.T) {
	for _, arrival := range []string{"before watch", "before check", "after check"} {
		t.Run(arrival, func(t *testing.T) {
			b := New(0)
			push := func() { b.Push("s", nil, Item{Text: "input"}) }
			if arrival == "before watch" {
				push()
			}
			ch := b.Watch("s")
			if arrival == "before check" {
				push()
			}
			queued := b.Len("s") > 0
			if arrival == "after check" {
				if queued {
					t.Fatal("input unexpectedly queued before arrival")
				}
				push()
			} else if !queued {
				t.Fatal("queue check missed prior input")
			}
			if !queued {
				assertWatchState(t, ch, true)
			}
			if it, ok := b.PopFirst("s"); !ok || it.Text != "input" {
				t.Fatalf("input unavailable after check/wake: %v, %v", it, ok)
			}
		})
	}
}

func TestBox_WatchClear(t *testing.T) {
	b := New(0)
	first, second := b.Watch("s"), b.Watch("s")
	other := b.Watch("other")
	b.Push("s", nil, Item{Text: "queued"})
	current := b.Watch("s")
	if items := b.Clear("s"); len(items) != 1 || items[0].Text != "queued" {
		t.Fatalf("Clear returned %v", items)
	}
	assertWatchState(t, first, true)
	assertWatchState(t, second, true)
	assertWatchState(t, current, true)
	assertWatchState(t, other, false)
	if _, ok := b.watches["s"]; ok {
		t.Fatal("Clear retained the session's watch")
	}
	b.Clear("s")
	next := b.Watch("s")
	assertWatchState(t, next, false)
	if next == current {
		t.Fatal("Watch reused the cleared channel")
	}
	// Clear must also wake listeners when the queue is already empty.
	b.Clear("s")
	assertWatchState(t, next, true)
}

func TestBox_WatchConcurrent(t *testing.T) {
	b := New(0)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				ch := b.Watch("s")
				b.Push("s", nil, Item{Text: "input"})
				assertWatchState(t, ch, true)
				ch = b.Watch("s")
				b.PushFront("s", Item{Text: "retry"})
				assertWatchState(t, ch, true)
				ch = b.Watch("s")
				b.Clear("s")
				assertWatchState(t, ch, true)
			}
		}()
	}
	wg.Wait()
}

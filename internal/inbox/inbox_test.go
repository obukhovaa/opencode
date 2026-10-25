package inbox

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func texts(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Text)
	}
	return out
}

func TestBox_Ordering(t *testing.T) {
	tests := []struct {
		name    string
		pushes  [][]string
		cap     int
		want    []string
		evicted []string
	}{
		{name: "single pushes keep arrival order", pushes: [][]string{{"a"}, {"b"}, {"c"}}, cap: 16, want: []string{"a", "b", "c"}},
		{name: "batch keeps its own order", pushes: [][]string{{"a"}, {"b", "c"}, {"d"}}, cap: 16, want: []string{"a", "b", "c", "d"}},
		{name: "overflow drops oldest", pushes: [][]string{{"a"}, {"b"}, {"c"}}, cap: 2, want: []string{"b", "c"}, evicted: []string{"a"}},
		{name: "oversized batch keeps its tail", pushes: [][]string{{"a", "b", "c"}}, cap: 2, want: []string{"b", "c"}, evicted: []string{"a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := New(tc.cap)
			var evicted []Item
			for _, p := range tc.pushes {
				items := make([]Item, 0, len(p))
				for _, s := range p {
					items = append(items, Item{Text: s})
				}
				evicted = append(evicted, b.Push("s", nil, items...)...)
			}
			if got := texts(b.DrainAll("s")); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("drained %v, want %v", got, tc.want)
			}
			if got := texts(evicted); len(tc.evicted) > 0 && !reflect.DeepEqual(got, tc.evicted) {
				t.Fatalf("evicted %v, want %v", got, tc.evicted)
			}
			if b.Len("s") != 0 {
				t.Fatalf("queue not empty after DrainAll")
			}
		})
	}
}

func TestBox_PopFirstLeavesRemainder(t *testing.T) {
	b := New(0)
	b.Push("s", nil, Item{Text: "m1"}, Item{Text: "m2"})
	it, ok := b.PopFirst("s")
	if !ok || it.Text != "m1" {
		t.Fatalf("PopFirst = %v, %v", it, ok)
	}
	if got := texts(b.DrainAll("s")); !reflect.DeepEqual(got, []string{"m2"}) {
		t.Fatalf("remainder %v", got)
	}
	if _, ok := b.PopFirst("s"); ok {
		t.Fatal("PopFirst on empty queue reported an item")
	}
}

func TestBox_SessionsAreIsolated(t *testing.T) {
	b := New(0)
	b.Push("s1", nil, Item{Text: "a"})
	b.Push("s2", nil, Item{Text: "b"})
	if got := texts(b.DrainAll("s1")); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("s1 drained %v", got)
	}
	if b.Len("s2") != 1 {
		t.Fatal("draining s1 touched s2")
	}
}

func TestBox_DeliveredFiresHookUntilClear(t *testing.T) {
	b := New(0)
	var got [][]string
	hook := func(_ string, items []Item) {
		ids := []string{}
		for _, it := range items {
			ids = append(ids, it.ID)
		}
		got = append(got, ids)
	}
	b.Push("s", hook, Item{ID: "a", Text: "x"}, Item{ID: "b", Text: "y"})
	b.Delivered("s", b.DrainAll("s"))
	if !reflect.DeepEqual(got, [][]string{{"a", "b"}}) {
		t.Fatalf("hook calls %v", got)
	}

	// Cleared input is returned and never acknowledged.
	b.Push("s", nil, Item{ID: "c"})
	left := b.Clear("s")
	if len(left) != 1 || left[0].ID != "c" {
		t.Fatalf("Clear returned %v", left)
	}
	b.Delivered("s", left)
	if len(got) != 1 {
		t.Fatalf("hook fired after Clear: %v", got)
	}
}

func TestBox_Any(t *testing.T) {
	b := New(0)
	b.Push("s", nil, Item{Text: "plain"}, Item{ID: "x", Text: "queued"})
	if !b.Any("s", func(it Item) bool { return it.ID == "x" }) {
		t.Fatal("Any missed a queued id")
	}
	if b.Any("s", func(it Item) bool { return it.ID == "nope" }) {
		t.Fatal("Any matched an absent id")
	}
}

func TestBox_DrainForDelivery(t *testing.T) {
	b := New(0)
	b.Push("s", nil, Item{ID: "a"})
	items, finish := b.DrainForDelivery("s")
	defer finish()
	if len(items) != 1 || items[0].ID != "a" || b.Len("s") != 0 {
		t.Fatalf("drained %v, queued %d", items, b.Len("s"))
	}
	matchA := func(it Item) bool { return it.ID == "a" }
	if b.Any("s", matchA) || !b.AnyPending("s", matchA) || b.AnyPending("other", matchA) {
		t.Fatal("reservation must be pending only for its own session")
	}
	b.Push("s", nil, Item{ID: "b"})
	_, finishB := b.DrainForDelivery("s")
	defer finishB()
	finish()
	if b.AnyPending("s", matchA) || !b.AnyPending("s", func(it Item) bool { return it.ID == "b" }) {
		t.Fatal("finishing one drain affected another drain's reservation")
	}
	b.Clear("s")
	b.Push("s", nil, Item{ID: "a"})
	_, finishNew := b.DrainForDelivery("s")
	defer finishNew()
	finishB()
	if !b.AnyPending("s", matchA) {
		t.Fatal("finishing a cleared drain removed the new reservation")
	}
	finishNew()
	if b.AnyPending("s", func(Item) bool { return true }) {
		t.Fatal("reservation leaked")
	}
}

// TestBox_ConcurrentBatchesNeverInterleave pushes two-item batches from
// many goroutines while another drains, and checks that every batch comes
// out contiguous. Run under -race.
func TestBox_ConcurrentBatchesNeverInterleave(t *testing.T) {
	b := New(1 << 20)
	const writers, batches = 8, 200
	var drained []Item
	var dmu sync.Mutex
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				dmu.Lock()
				drained = append(drained, b.DrainAll("s")...)
				dmu.Unlock()
				return
			default:
				items := b.DrainAll("s")
				dmu.Lock()
				drained = append(drained, items...)
				dmu.Unlock()
			}
		}
	}()
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < batches; i++ {
				id := fmt.Sprintf("%d-%d", w, i)
				b.Push("s", nil, Item{ID: id, Text: "1"}, Item{ID: id, Text: "2"})
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	<-done
	if len(drained) != writers*batches*2 {
		t.Fatalf("drained %d items, want %d", len(drained), writers*batches*2)
	}
	for i := 0; i < len(drained); i += 2 {
		if drained[i].ID != drained[i+1].ID || drained[i].Text != "1" || drained[i+1].Text != "2" {
			t.Fatalf("batch split at %d: %v %v", i, drained[i], drained[i+1])
		}
	}
}

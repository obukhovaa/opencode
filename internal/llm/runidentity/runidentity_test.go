package runidentity

import (
	"reflect"
	"sync"
	"testing"
)

// Every test clears the holder afterwards: it is process-global by
// design, so a leaked value would silently re-key a later test.
func clear(t *testing.T) { t.Cleanup(func() { Set(nil) }) }

func TestSetAndClear(t *testing.T) {
	clear(t)
	if got := Get(); got != nil {
		t.Fatalf("Get on a fresh holder = %+v, want nil", got)
	}
	Set(&Identity{APIKey: "k", UserID: "u", Tags: []string{"team:acme"}})
	if APIKey() != "k" || UserID() != "u" {
		t.Errorf("APIKey/UserID = %q/%q, want k/u", APIKey(), UserID())
	}
	if got := Tags(); !reflect.DeepEqual(got, []string{"team:acme"}) {
		t.Errorf("Tags = %v", got)
	}
	Set(nil)
	if Get() != nil || APIKey() != "" || UserID() != "" || Tags() != nil {
		t.Errorf("clear left residue: %+v %q %q %v", Get(), APIKey(), UserID(), Tags())
	}
}

// A partially-populated identity must leave the other fields as "no
// override" rather than as an empty override — the three POST /flow
// fields are independent on the wire.
func TestPartialIdentityLeavesOtherFieldsEmpty(t *testing.T) {
	clear(t)
	Set(&Identity{UserID: "only-user"})
	if APIKey() != "" {
		t.Errorf("APIKey = %q, want empty", APIKey())
	}
	if UserID() != "only-user" {
		t.Errorf("UserID = %q", UserID())
	}
}

// Set copies, so a caller reusing its Identity (or the slice backing its
// tags) cannot retroactively change what an in-flight run observes.
func TestSetCopiesTheCaller(t *testing.T) {
	clear(t)
	tags := []string{"team:acme"}
	id := &Identity{APIKey: "k", Tags: tags}
	Set(id)
	id.APIKey = "mutated"
	tags[0] = "team:evil"
	if APIKey() != "k" {
		t.Errorf("APIKey = %q, want the value at Set time", APIKey())
	}
	if got := Tags(); !reflect.DeepEqual(got, []string{"team:acme"}) {
		t.Errorf("Tags = %v, want the slice contents at Set time", got)
	}
}

// Tags returns a copy, so a reader cannot corrupt the published identity
// for every later reader.
func TestTagsReturnsACopy(t *testing.T) {
	clear(t)
	Set(&Identity{Tags: []string{"team:acme"}})
	got := Tags()
	got[0] = "team:evil"
	if again := Tags(); again[0] != "team:acme" {
		t.Errorf("Tags = %v, want the mutation to be confined to the caller", again)
	}
}

func TestMergeTags(t *testing.T) {
	tests := []struct {
		name string
		base []string
		run  []string
		want []string
	}{
		{
			name: "no run tags leaves base untouched",
			base: []string{"env:dev", "team:pool-owner"},
			want: []string{"env:dev", "team:pool-owner"},
		},
		{
			// The case this exists for: the pod's boot config names the
			// pool owner's team; a run for another team must emit one.
			name: "run tag shadows the config tag in the same namespace",
			base: []string{"env:dev", "team:pool-owner"},
			run:  []string{"team:acme"},
			want: []string{"env:dev", "team:acme"},
		},
		{
			name: "unrelated namespaces both survive",
			base: []string{"env:dev"},
			run:  []string{"team:acme"},
			want: []string{"env:dev", "team:acme"},
		},
		{
			name: "shadowing drops every config tag in the namespace",
			base: []string{"team:a", "team:b", "env:dev"},
			run:  []string{"team:acme"},
			want: []string{"env:dev", "team:acme"},
		},
		{
			name: "unnamespaced tags compare whole",
			base: []string{"agent", "env:dev"},
			run:  []string{"agent"},
			want: []string{"env:dev", "agent"},
		},
		{
			name: "an unnamespaced run tag does not shadow a namespaced one",
			base: []string{"team:pool-owner"},
			run:  []string{"team"},
			want: []string{"team:pool-owner", "team"},
		},
		{
			name: "empty base keeps run tags",
			run:  []string{"team:acme"},
			want: []string{"team:acme"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeTags(tt.base, tt.run)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("MergeTags(%v, %v) = %v, want %v", tt.base, tt.run, got, tt.want)
			}
		})
	}
}

// The holder sits under every LLM request, so concurrent reads against a
// publish must not race. Meaningful under -race.
func TestConcurrentReadsDuringSet(t *testing.T) {
	clear(t)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _, _ = APIKey(), UserID(), Tags()
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		Set(&Identity{APIKey: "k", UserID: "u", Tags: []string{"team:acme"}})
		Set(nil)
	}
	close(stop)
	wg.Wait()
}

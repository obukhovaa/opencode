package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/runidentity"
)

// TestGetUserIDPrefersTheRunOverride is the regression guard for the
// defect that made pooled telemetry unattributable: GetUserID memoises
// the process value in a sync.Once, so on a pool pod every run after the
// first reported the FIRST run's user id — or, worse, the pod's static
// boot identity for all of them.
//
// The override is checked before the cache, so a pool pod attributes each
// run to its own team while every non-pool deployment keeps the cached
// process value untouched.
func TestGetUserIDPrefersTheRunOverride(t *testing.T) {
	t.Cleanup(func() { runidentity.Set(nil) })

	runidentity.Set(&runidentity.Identity{UserID: "acme-developer"})
	if got := GetUserID(); got != "acme-developer" {
		t.Fatalf("GetUserID under an override = %q, want the run's id", got)
	}

	// A second run on the same process gets ITS id, not the first's —
	// the sync.Once must not have frozen anything reachable here.
	runidentity.Set(&runidentity.Identity{UserID: "globex-developer"})
	if got := GetUserID(); got != "globex-developer" {
		t.Fatalf("GetUserID after a rebind = %q, want the second run's id", got)
	}

	// Clearing restores the process value rather than yielding "".
	runidentity.Set(nil)
	if got := GetUserID(); got == "" {
		t.Error("GetUserID with no override returned empty; want the process value")
	}
	if got := GetUserID(); got != resolvedUserID {
		t.Errorf("GetUserID with no override = %q, want the cached process value %q", got, resolvedUserID)
	}
}

// An identity that carries no user id must not blank the process value —
// the three POST /flow fields are independent, so a run may send only a
// key or only a team.
func TestGetUserIDIgnoresAnIdentityWithoutAUserID(t *testing.T) {
	t.Cleanup(func() { runidentity.Set(nil) })
	want := GetUserID()
	runidentity.Set(&runidentity.Identity{APIKey: "k"})
	if got := GetUserID(); got != want {
		t.Errorf("GetUserID = %q, want the process value %q", got, want)
	}
}

func TestResolveTagsAppliesTheRunIdentity(t *testing.T) {
	// Same seam the package's other telemetry tests use: load the real
	// config, then overwrite just the block under test.
	config.Reset()
	if _, err := config.Load(".", false); err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	config.Get().Telemetry = &config.TelemetryConfig{
		Tags: []string{"env:dev", "team:pool-owner"},
	}
	t.Cleanup(func() {
		config.Reset()
		runidentity.Set(nil)
	})

	t.Run("no override keeps the config tags", func(t *testing.T) {
		runidentity.Set(nil)
		want := []string{"env:dev", "team:pool-owner"}
		if got := ResolveTags(context.Background()); !reflect.DeepEqual(got, want) {
			t.Errorf("ResolveTags = %v, want %v", got, want)
		}
	})

	// The defect this prevents: appending the run's team would emit BOTH
	// team tags, so a pooled trace claims two owners and neither
	// aggregation is right.
	t.Run("the run team shadows the pod's static team tag", func(t *testing.T) {
		runidentity.Set(&runidentity.Identity{Tags: []string{"team:acme"}})
		want := []string{"env:dev", "team:acme"}
		if got := ResolveTags(context.Background()); !reflect.DeepEqual(got, want) {
			t.Errorf("ResolveTags = %v, want %v", got, want)
		}
	})
}

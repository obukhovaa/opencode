package agent

import "testing"

// TestResetStepCacheClearsTheMap pins the actual clear, not just that the
// flow runner calls it. The cache is keyed on the flow YAML's step ID,
// which recurs across runs; on a process that serves many runs, leaving
// it populated hands run N+1 run N's agents — and their once-resolved
// MCP toolsets, so a single failed discovery would strip the tools from
// every later job on the pod.
func TestResetStepCacheClearsTheMap(t *testing.T) {
	f := &agentFactory{stepCache: map[string]Service{}}

	// Reset on an empty cache is a no-op, not a panic.
	f.ResetStepCache()

	f.stepCache["step-a"] = nil
	f.stepCache["step-b"] = nil
	if len(f.stepCache) != 2 {
		t.Fatalf("seed failed: %d entries", len(f.stepCache))
	}

	f.ResetStepCache()

	if len(f.stepCache) != 0 {
		t.Errorf("stepCache still holds %d entries after reset", len(f.stepCache))
	}
	// The map must still be usable — a nil map would panic on the next
	// NewAgent that caches a step agent.
	f.stepCache["step-c"] = nil
	if len(f.stepCache) != 1 {
		t.Errorf("stepCache is not writable after reset")
	}
}

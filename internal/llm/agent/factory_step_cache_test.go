package agent

import "testing"

// TestResetStepCacheClearsTheMap pins the actual clear, not just that the
// flow runner calls it. The cache is keyed on the flow YAML's step ID,
// which recurs across runs; on a process that serves many runs, leaving
// it populated hands run N+1 run N's agents — and their once-resolved
// MCP toolsets, so a single failed discovery would strip the tools from
// every later job on the pod.
func TestResetStepCacheClearsTheMap(t *testing.T) {
	f := &agentFactory{stepCache: map[stepCacheKey]Service{}}

	// Reset on an empty cache is a no-op, not a panic.
	f.ResetStepCache()

	f.stepCache[stepCacheKey{stepID: "step-a"}] = nil
	f.stepCache[stepCacheKey{stepID: "step-b"}] = nil
	if len(f.stepCache) != 2 {
		t.Fatalf("seed failed: %d entries", len(f.stepCache))
	}

	f.ResetStepCache()

	if len(f.stepCache) != 0 {
		t.Errorf("stepCache still holds %d entries after reset", len(f.stepCache))
	}
	// The map must still be usable — a nil map would panic on the next
	// NewAgent that caches a step agent.
	f.stepCache[stepCacheKey{stepID: "step-c"}] = nil
	if len(f.stepCache) != 1 {
		t.Errorf("stepCache is not writable after reset")
	}
}

// TestStepCacheKey_IsolatesOverrides pins that the per-step cache is keyed
// on the (step, agent, model override) tuple, not the step ID alone. A step
// re-entered with a different `model: ${args.tier}` (escalation) or
// `agent: ${args.x}` must build a new agent; the same tuple must hit.
func TestStepCacheKey_IsolatesOverrides(t *testing.T) {
	f := &agentFactory{stepCache: map[stepCacheKey]Service{}}
	standard := stepCacheKey{stepID: "implement", agentID: "piano-developer", model: "bedrock.eu-claude-sonnet-5", effort: "high"}
	f.stepCache[standard] = nil

	tests := []struct {
		name string
		key  stepCacheKey
		hit  bool
	}{
		{"same tuple hits", standard, true},
		{"different model misses", stepCacheKey{stepID: "implement", agentID: "piano-developer", model: "bedrock.eu-claude-opus-5", effort: "high"}, false},
		{"different effort misses", stepCacheKey{stepID: "implement", agentID: "piano-developer", model: "bedrock.eu-claude-sonnet-5", effort: "max"}, false},
		{"different agent misses", stepCacheKey{stepID: "implement", agentID: "coder", model: "bedrock.eu-claude-sonnet-5", effort: "high"}, false},
		{"no override misses", stepCacheKey{stepID: "implement", agentID: "piano-developer"}, false},
		{"different step misses", stepCacheKey{stepID: "salvage", agentID: "piano-developer", model: "bedrock.eu-claude-sonnet-5", effort: "high"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := f.stepCache[tt.key]; ok != tt.hit {
				t.Errorf("cache hit = %v, want %v for %+v", ok, tt.hit, tt.key)
			}
		})
	}
}

package agent

import "testing"

func TestRuntimeToolPolicyIsScopedAndImmutable(t *testing.T) {
	r := &registry{agents: map[string]AgentInfo{"one": {ID: "one"}, "two": {ID: "two"}}}
	enabled := map[string]bool{"server_*": false}
	if !SetRuntimeTools(r, "one", enabled, map[string]bool{"other_*": true}) {
		t.Fatal("set failed")
	}
	enabled["server_*"] = true
	if r.IsToolEnabled("one", "server_read") {
		t.Fatal("disabled tool exposed")
	}
	if !r.IsToolEnabled("two", "server_read") {
		t.Fatal("other agent changed")
	}
	info, _ := r.Get("one")
	if !info.DeferredTools["other_*"] {
		t.Fatal("deferral lost")
	}
}

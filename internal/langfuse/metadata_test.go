package langfuse

import "testing"

func TestNamespaceMetadata(t *testing.T) {
	tests := []struct {
		name      string
		metadata  map[string]any
		namespace string
		wantKeys  []string // expected keys in result
		wantLen   int
	}{
		{
			name:      "empty namespace returns original map",
			metadata:  map[string]any{"flow_id": "abc", "agent_id": "coder"},
			namespace: "",
			wantKeys:  []string{"flow_id", "agent_id"},
			wantLen:   2,
		},
		{
			name:      "nil metadata returns nil",
			metadata:  nil,
			namespace: "app",
			wantKeys:  nil,
			wantLen:   0,
		},
		{
			name:      "empty metadata returns empty",
			metadata:  map[string]any{},
			namespace: "app",
			wantKeys:  nil,
			wantLen:   0,
		},
		{
			name:      "namespace prefixes all keys",
			metadata:  map[string]any{"flow_id": "deploy", "agent_id": "coder", "ticket": "PROJ-123"},
			namespace: "app",
			wantKeys:  []string{"app.flow_id", "app.agent_id", "app.ticket"},
			wantLen:   3,
		},
		{
			name:      "single key",
			metadata:  map[string]any{"version": "1.0"},
			namespace: "opencode",
			wantKeys:  []string{"opencode.version"},
			wantLen:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NamespaceMetadata(tt.metadata, tt.namespace)
			if len(got) != tt.wantLen {
				t.Fatalf("len = %d, want %d", len(got), tt.wantLen)
			}
			for _, k := range tt.wantKeys {
				if _, ok := got[k]; !ok {
					t.Errorf("missing key %q in result: %v", k, got)
				}
			}
		})
	}
}

func TestNamespaceMetadataPreservesValues(t *testing.T) {
	meta := map[string]any{
		"flow_id":  "deploy",
		"agent_id": "coder",
		"count":    42,
	}
	got := NamespaceMetadata(meta, "ns")
	if got["ns.flow_id"] != "deploy" {
		t.Errorf("ns.flow_id = %v, want deploy", got["ns.flow_id"])
	}
	if got["ns.agent_id"] != "coder" {
		t.Errorf("ns.agent_id = %v, want coder", got["ns.agent_id"])
	}
	if got["ns.count"] != 42 {
		t.Errorf("ns.count = %v, want 42", got["ns.count"])
	}
}

func TestNamespaceMetadataDoesNotMutateOriginal(t *testing.T) {
	meta := map[string]any{"key": "value"}
	got := NamespaceMetadata(meta, "prefix")
	// Original should still have "key", not "prefix.key"
	if _, ok := meta["key"]; !ok {
		t.Error("original map was mutated: 'key' missing")
	}
	if _, ok := meta["prefix.key"]; ok {
		t.Error("original map was mutated: 'prefix.key' present")
	}
	if _, ok := got["prefix.key"]; !ok {
		t.Error("result missing 'prefix.key'")
	}
}

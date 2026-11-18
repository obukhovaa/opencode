package agent

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/permission"
)

func TestPatchRequiresNamedOptIn(t *testing.T) {
	for _, tc := range []struct {
		name string
		info AgentInfo
		want bool
	}{
		{name: "default"},
		{name: "tools wildcard", info: AgentInfo{Tools: map[string]bool{"*": true}}},
		{name: "tools pattern", info: AgentInfo{Tools: map[string]bool{"p*": true}}},
		{name: "tools exact", info: AgentInfo{Tools: map[string]bool{"patch": true}}, want: true},
		{name: "tools exact overrides deny wildcard", info: AgentInfo{Tools: map[string]bool{"*": false, "patch": true}}, want: true},
		{name: "tools deny overrides wildcard", info: AgentInfo{Tools: map[string]bool{"*": true, "patch": false}}},
		{name: "allowlist wildcard", info: AgentInfo{AllowTools: []string{"*"}}},
		{name: "allowlist pattern", info: AgentInfo{AllowTools: []string{"p*"}}},
		{name: "allowlist exact", info: AgentInfo{AllowTools: []string{"patch"}}, want: true},
		{name: "allowlist exact with wildcard", info: AgentInfo{AllowTools: []string{"*", "patch"}}, want: true},
		{name: "allowlist takes precedence", info: AgentInfo{AllowTools: []string{"edit"}, Tools: map[string]bool{"patch": true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &registry{agents: map[string]AgentInfo{"test": tc.info}, globalPerms: map[string]any{"patch": "allow"}}
			if got := r.IsToolEnabled("test", "patch"); got != tc.want {
				t.Fatalf("IsToolEnabled = %v, want %v", got, tc.want)
			}
			if got := r.IsToolExplicitlyEnabled("test", "patch"); got != tc.want {
				t.Fatalf("IsToolExplicitlyEnabled = %v, want %v", got, tc.want)
			}
			if !tc.want {
				if got := r.EvaluatePermission("test", "patch", "/file"); got != permission.ActionDeny {
					t.Fatalf("permission = %v, want deny", got)
				}
				if got := r.EvaluateReadPermission("test", "patch", "/file"); got != permission.ActionDeny {
					t.Fatalf("read permission = %v, want deny", got)
				}
			} else if got := r.EvaluatePermission("test", "patch", "/file"); got != permission.ActionAllow {
				t.Fatalf("explicit patch permission = %v, want allow", got)
			}
		})
	}
}

func TestPatchUnknownAgentDefaultsToDenied(t *testing.T) {
	r := &registry{globalPerms: map[string]any{"patch": "allow"}}
	if r.IsToolEnabled("missing", "patch") || r.IsToolExplicitlyEnabled("missing", "patch") {
		t.Fatal("unknown agent received patch")
	}
	if r.EvaluatePermission("missing", "patch", "/file") != permission.ActionDeny ||
		r.EvaluateReadPermission("missing", "patch", "/file") != permission.ActionDeny {
		t.Fatal("unknown agent received patch permission")
	}
	if !r.IsToolEnabled("missing", "edit") {
		t.Fatal("edit default changed")
	}
}

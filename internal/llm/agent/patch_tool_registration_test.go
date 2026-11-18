package agent

import (
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// Exercise the construction gate with the production AgentInfo policy,
// keeping unrelated tools and their service dependencies out of this test.
type patchOptInRegistry struct {
	agentregistry.Registry
	info agentregistry.AgentInfo
}

func (r patchOptInRegistry) IsToolEnabled(_, name string) bool {
	return name == tools.PatchToolName && r.info.ToolEnabled(name)
}

func (r patchOptInRegistry) IsToolExplicitlyEnabled(string, string) bool { return false }

func TestPatchToolRegistrationRequiresNamedOptIn(t *testing.T) {
	for _, tc := range []struct {
		name string
		info agentregistry.AgentInfo
		want bool
	}{
		{name: "default"},
		{name: "tools wildcard", info: agentregistry.AgentInfo{Tools: map[string]bool{"*": true}}},
		{name: "tools pattern", info: agentregistry.AgentInfo{Tools: map[string]bool{"p*": true}}},
		{name: "tools named", info: agentregistry.AgentInfo{Tools: map[string]bool{"patch": true}}, want: true},
		{name: "allowlist wildcard", info: agentregistry.AgentInfo{AllowTools: []string{"*"}}},
		{name: "allowlist pattern", info: agentregistry.AgentInfo{AllowTools: []string{"p*"}}},
		{name: "allowlist named", info: agentregistry.AgentInfo{AllowTools: []string{"patch"}}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.info.ID = "patch-test"
			found := false
			reg := patchOptInRegistry{info: tc.info}
			for tool := range NewToolSet(&tc.info, reg, nil, nil, nil, nil, nil, noMCP{}, nil, "") {
				if tool.Info().Name == tools.PatchToolName {
					found = true
				}
			}
			if found != tc.want {
				t.Fatalf("patch registered = %v, want %v", found, tc.want)
			}
		})
	}
}

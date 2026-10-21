package agent

import (
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

type memoryOptInRegistry struct {
	agentregistry.Registry
	explicit bool
}

func (r memoryOptInRegistry) IsToolEnabled(_, name string) bool { return name == tools.MemoryToolName }
func (r memoryOptInRegistry) IsToolExplicitlyEnabled(_, name string) bool {
	return r.explicit && name == tools.MemoryToolName
}
func (r memoryOptInRegistry) HasTools(string) bool { return true }

func TestMemoryToolRequiresConfigAndGrant(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	cfg := config.Get()
	previous := cfg.Memory
	t.Cleanup(func() { cfg.Memory = previous })
	for _, enabled := range []bool{false, true} {
		for _, grant := range []bool{false, true} {
			cfg.Memory = &config.MemoryConfig{Enabled: enabled, Directory: t.TempDir()}
			info := &agentregistry.AgentInfo{ID: "memory-agent", Mode: config.AgentModeSubagent}
			found := false
			for tool := range NewToolSet(info, memoryOptInRegistry{explicit: grant}, nil, nil, nil, nil, nil, noMCP{}, &lateHeartbeatFactory{}, "") {
				found = found || tool.Info().Name == tools.MemoryToolName
			}
			if found != (enabled && grant) {
				t.Fatalf("enabled=%v grant=%v found=%v", enabled, grant, found)
			}
		}
	}
}

package agent

import (
	"encoding/json"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

type memoryFileRegistry struct{ agentregistry.Registry }

func (memoryFileRegistry) IsToolEnabled(_, name string) bool {
	return name == tools.ReadToolName || name == tools.WriteToolName
}
func (memoryFileRegistry) IsToolExplicitlyEnabled(string, string) bool { return false }
func (memoryFileRegistry) HasTools(string) bool                        { return true }

func TestMemoryDoesNotAddOrChangeToolSchemas(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	cfg := config.Get()
	var inventories []string
	for _, enabled := range []bool{false, true} {
		cfg.Memory = &config.MemoryConfig{Enabled: enabled, Directory: t.TempDir()}
		info := &agentregistry.AgentInfo{ID: "memory-test", Mode: config.AgentModeSubagent}
		var definitions []tools.ToolInfo
		for tool := range NewToolSet(info, memoryFileRegistry{}, nil, nil, nil, nil, nil, noMCP{}, &lateHeartbeatFactory{}, "") {
			definitions = append(definitions, tool.Info())
		}
		if len(definitions) != 2 {
			t.Fatalf("unexpected tools: %+v", definitions)
		}
		data, err := json.Marshal(definitions)
		if err != nil {
			t.Fatal(err)
		}
		inventories = append(inventories, string(data))
	}
	if inventories[0] != inventories[1] {
		t.Fatal("enabling memory changed the tool definitions")
	}
}

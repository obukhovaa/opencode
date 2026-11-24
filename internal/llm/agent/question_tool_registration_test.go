package agent

import (
	"encoding/json"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/question"
)

// Question availability follows the process service and agent role/grants,
// not the flow step's Interactive flag. Changing the latter must not change
// the serialized tool definitions and invalidate the provider's tools prefix.
func TestQuestionToolSetStableAcrossFlowModes(t *testing.T) {
	loadConfigIn(t, t.TempDir())
	cfg := config.Get()
	cfg.Agents["question-probe"] = config.Agent{
		Mode: config.AgentModeAgent, AllowTools: []string{tools.YieldToolName, tools.QuestionToolName},
	}
	agentregistry.InvalidateRegistry()
	t.Cleanup(agentregistry.InvalidateRegistry)
	reg := agentregistry.GetRegistry()
	info, ok := reg.Get("question-probe")
	if !ok {
		t.Fatal("missing test agent")
	}
	factory := &agentFactory{}
	factory.SetQuestionService(question.NewService())
	var prefix []byte
	for _, interactive := range []bool{false, true, false} {
		info.Interactive = interactive
		var resolved []tools.BaseTool
		for tool := range NewToolSet(&info, reg, nil, nil, nil, nil, nil, noMCP{}, factory, "") {
			resolved = append(resolved, tool)
		}
		var declarations []tools.ToolInfo
		found := false
		for _, tool := range OrderTools(resolved) {
			declarations = append(declarations, tool.Info())
			found = found || tool.Info().Name == tools.QuestionToolName
		}
		if !found {
			t.Fatalf("question absent with enabled service/grant, interactive=%v", interactive)
		}
		raw, err := json.Marshal(declarations)
		if err != nil {
			t.Fatal(err)
		}
		if prefix != nil && string(raw) != string(prefix) {
			t.Fatalf("tool prefix changed with interactive=%v", interactive)
		}
		prefix = raw
	}

	for _, tc := range []struct {
		name    string
		mode    config.AgentMode
		service bool
		grant   bool
	}{
		{"service disabled", config.AgentModeAgent, false, true},
		{"grant absent", config.AgentModeAgent, true, false},
		{"subagent role", config.AgentModeSubagent, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			infoCopy := info
			infoCopy.Mode = tc.mode
			infoCopy.AllowTools = []string{tools.YieldToolName}
			if tc.grant {
				infoCopy.AllowTools = append(infoCopy.AllowTools, tools.QuestionToolName)
			}
			cfg.Agents["question-probe"] = config.Agent{Mode: infoCopy.Mode, AllowTools: infoCopy.AllowTools}
			agentregistry.InvalidateRegistry()
			registry := agentregistry.GetRegistry()
			factory.SetQuestionService(nil)
			if tc.service {
				factory.SetQuestionService(question.NewService())
			}
			for tool := range NewToolSet(&infoCopy, registry, nil, nil, nil, nil, nil, noMCP{}, factory, "") {
				if tool.Info().Name == tools.QuestionToolName {
					t.Fatal("question registered without its service, role or grant")
				}
			}
		})
	}
}

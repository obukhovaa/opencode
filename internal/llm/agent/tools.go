package agent

import (
	"context"
	"strings"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/session"
)

var (
	viewerToolNames = []string{
		tools.LSToolName,
		tools.GlobToolName,
		tools.GrepToolName,
		tools.ViewToolName,
		tools.ViewImageToolName,
		tools.FetchToolName,
		tools.SkillToolName,
		tools.SourcegraphToolName,
	}
	editorToolNames = []string{
		tools.WriteToolName,
		tools.EditToolName,
		tools.MultiEditToolName,
		tools.DeleteToolName,
		tools.PatchToolName,
		tools.BashToolName,
	}
	managerToolNames = []string{
		TaskToolName,
	}
)

// NewToolSet dynamically builds the tool slice for an agent based on its
// registry info. Only tools that pass registry.IsToolEnabled are included.
func NewToolSet(
	info *agentregistry.AgentInfo,
	reg agentregistry.Registry,
	permissions permission.Service,
	historyService history.Service,
	lspClients map[string]*lsp.Client,
	sessions session.Service,
	messages message.Service,
) []tools.BaseTool {
	agentID := info.ID
	var result []tools.BaseTool

	createTool := func(name string) tools.BaseTool {
		switch name {
		case tools.LSToolName:
			return tools.NewLsTool(config.Get())
		case tools.GlobToolName:
			return tools.NewGlobTool()
		case tools.GrepToolName:
			return tools.NewGrepTool()
		case tools.ViewToolName:
			return tools.NewViewTool(lspClients)
		case tools.ViewImageToolName:
			return tools.NewViewImageTool()
		case tools.FetchToolName:
			return tools.NewFetchTool(permissions)
		case tools.SkillToolName:
			return tools.NewSkillTool(permissions, reg)
		case tools.SourcegraphToolName:
			return tools.NewSourcegraphTool()
		case tools.WriteToolName:
			return tools.NewWriteTool(lspClients, permissions, historyService, reg)
		case tools.EditToolName:
			return tools.NewEditTool(lspClients, permissions, historyService, reg)
		case tools.MultiEditToolName:
			return tools.NewMultiEditTool(lspClients, permissions, historyService, reg)
		case tools.DeleteToolName:
			return tools.NewDeleteTool(permissions, historyService, reg)
		case tools.PatchToolName:
			return tools.NewPatchTool(lspClients, permissions, historyService, reg)
		case tools.BashToolName:
			return tools.NewBashTool(permissions, reg)
		case TaskToolName:
			return NewAgentTool(sessions, messages, lspClients, permissions, historyService, reg)
		default:
			return nil
		}
	}

	for _, name := range viewerToolNames {
		if reg.IsToolEnabled(agentID, name) {
			if t := createTool(name); t != nil {
				result = append(result, t)
			}
		}
	}

	if len(lspClients) > 0 && reg.IsToolEnabled(agentID, tools.LspToolName) {
		result = append(result, tools.NewLspTool(lspClients))
	}

	for _, name := range editorToolNames {
		if reg.IsToolEnabled(agentID, name) {
			if t := createTool(name); t != nil {
				result = append(result, t)
			}
		}
	}

	for _, name := range managerToolNames {
		if reg.IsToolEnabled(agentID, name) {
			if info.Mode == config.AgentModeAgent {
				if t := createTool(name); t != nil {
					result = append(result, t)
				}
			} else {
				logging.Warn("Subagent can't have manager tools enabled, tool will be ignored", "agent", agentID, "tool", name)
			}
		}
	}

	// MCP tools — shared instances, filter per agent
	ctx := context.Background()
	mcpTools := GetMcpTools(ctx, permissions, reg)
	for _, mt := range mcpTools {
		if reg.IsToolEnabled(agentID, mt.Info().Name) {
			result = append(result, mt)
		}
	}

	names := make([]string, len(result))
	for i, t := range result {
		names[i] = t.Info().Name
	}
	logging.Info("Resolved tool set", "agent", agentID, "tools", strings.Join(names, ", "))

	return result
}

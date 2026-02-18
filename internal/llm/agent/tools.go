package agent

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/format"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/install"
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
	// TODO: add todo tool
	managerToolNames = []string{
		TaskToolName,
	}
)

// NewToolSet dynamically builds the tool slice for an agent based on its
// registry info. Only tools that pass registry.IsToolEnabled are included.
func NewToolSet(
	ctx context.Context,
	info *agentregistry.AgentInfo,
	reg agentregistry.Registry,
	permissions permission.Service,
	historyService history.Service,
	lspClients map[string]*lsp.Client,
	sessions session.Service,
	messages message.Service,
	mcpRegistry MCPRegistry,
) <-chan tools.BaseTool {
	agentID := info.ID
	result := make(chan tools.BaseTool, 100)

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
			return NewAgentTool(sessions, messages, lspClients, permissions, historyService, reg, mcpRegistry)
		default:
			return nil
		}
	}

	for _, name := range viewerToolNames {
		if reg.IsToolEnabled(agentID, name) {
			if t := createTool(name); t != nil {
				result <- t
			}
		}
	}

	for _, name := range editorToolNames {
		if reg.IsToolEnabled(agentID, name) {
			if t := createTool(name); t != nil {
				result <- t
			}
		}
	}

	for _, name := range managerToolNames {
		if reg.IsToolEnabled(agentID, name) {
			if info.Mode == config.AgentModeAgent {
				if t := createTool(name); t != nil {
					result <- t
				}
			} else {
				logging.Warn("Subagent can't have manager tools enabled, tool will be ignored", "agent", agentID, "tool", name)
			}
		}
	}

	// Inject struct_output tool if the agent has an output schema configured
	if info.Output != nil && info.Output.Schema != nil {
		if reg.IsToolEnabled(agentID, tools.StructOutputToolName) {
			schema := info.Output.Schema
			// Resolve $ref if present, using agent's markdown location for relative paths
			baseDir := ""
			if info.Location != "" {
				baseDir = filepath.Dir(info.Location)
			}
			resolved, err := format.ResolveSchemaRef(schema, baseDir)
			if err != nil {
				logging.Error("Failed to resolve output schema $ref", "agent", agentID, "error", err)
			} else {
				logging.Info("Using structured output", "agent", agentID, "schema", resolved)
				result <- tools.NewStructOutputTool(resolved)
			}
		}
	}

	wg := sync.WaitGroup{}

	// MCP tools — shared instances, filter per agent
	wg.Add(1)
	go func() {
		defer logging.RecoverPanic("MCP-goroutine", nil)
		defer wg.Done()
		for mt := range mcpRegistry.LoadTools(ctx, nil) {
			if reg.IsToolEnabled(agentID, mt.Info().Name) {
				result <- mt
			}
		}
	}()

	// LSP tools – can be properly initialised only after servers up and running
	wg.Add(1)
	go func() {
		defer logging.RecoverPanic("LSP-goroutine", nil)
		defer wg.Done()
		cfg := config.Get()
		if len(install.ResolveServers(cfg)) > 0 && reg.IsToolEnabled(agentID, tools.LSPToolName) {
			result <- tools.NewLspTool(lspClients)
		}
	}()

	go func() {
		wg.Wait()
		close(result)
	}()

	return result
}

func (a *agent) resolveTools() []tools.BaseTool {
	a.toolsOnce.Do(func() {
		toolSet := make([]tools.BaseTool, 0, 20)
		toolNames := make([]string, 0, 20)
		for t := range a.toolsCh {
			toolSet = append(toolSet, t)
			toolNames = append(toolNames, t.Info().Name)
		}
		a.tools = toolSet
		logging.Info("Resolved tool set", "agent", a.AgentID(), "tools", strings.Join(toolNames, ", "))
	})
	return a.tools
}

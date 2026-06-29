package agent

import (
	"context"
	"path/filepath"
	"sort"
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
		tools.ReadToolName,
		tools.ViewImageToolName,
		tools.WebFetchToolName,
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
		// Background-task tools spawn/kill subprocesses or subagents and are
		// available to both agents and subagents (subagents may want to
		// monitor or kill their own background work too).
		tools.MonitorToolName,
		tools.TaskListToolName,
		tools.TaskStopToolName,
	}
	managerToolNames = []string{
		TaskToolName,
		tools.QuestionToolName,
		tools.CronCreateToolName,
		tools.CronDeleteToolName,
		tools.CronListToolName,
		tools.TodoWriteToolName,
		tools.RouterSendToolName,
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
	lspService lsp.LspService,
	sessions session.Service,
	messages message.Service,
	mcpRegistry MCPRegistry,
	factory AgentFactory,
) <-chan tools.BaseTool {
	agentID := info.ID
	result := make(chan tools.BaseTool, 100)

	createTool := func(name string) tools.BaseTool {
		switch name {
		case tools.LSToolName:
			return tools.NewLsTool(config.Get(), reg, permissions)
		case tools.GlobToolName:
			return tools.NewGlobTool(reg, permissions)
		case tools.GrepToolName:
			return tools.NewGrepTool(reg, permissions)
		case tools.ReadToolName:
			return tools.NewReadTool(lspService, reg, permissions)
		case tools.ViewImageToolName:
			return tools.NewViewImageTool()
		case tools.WebFetchToolName:
			return tools.NewFetchTool(reg, permissions)
		case tools.SkillToolName:
			return tools.NewSkillTool(permissions, reg)
		case tools.SourcegraphToolName:
			return tools.NewSourcegraphTool()
		case tools.WebSearchToolName:
			return tools.NewWebSearchTool(reg, tools.NewSearchProviderRegistry(config.Get()), permissions)
		case tools.WriteToolName:
			return tools.NewWriteTool(lspService, permissions, historyService, reg)
		case tools.EditToolName:
			return tools.NewEditTool(lspService, permissions, historyService, reg)
		case tools.MultiEditToolName:
			return tools.NewMultiEditTool(lspService, permissions, historyService, reg)
		case tools.DeleteToolName:
			return tools.NewDeleteTool(permissions, historyService, reg)
		case tools.PatchToolName:
			return tools.NewPatchTool(lspService, permissions, historyService, reg)
		case tools.BashToolName:
			return tools.NewBashTool(permissions, reg)
		case TaskToolName:
			return NewAgentTool(sessions, permissions, reg, factory)
		case tools.CronCreateToolName:
			if svc, helper := factory.CronServices(); svc != nil {
				return tools.NewCronCreateTool(svc, helper)
			}
			return nil
		case tools.CronDeleteToolName:
			if svc, _ := factory.CronServices(); svc != nil {
				return tools.NewCronDeleteTool(svc)
			}
			return nil
		case tools.CronListToolName:
			if svc, helper := factory.CronServices(); svc != nil {
				return tools.NewCronListTool(svc, helper)
			}
			return nil
		case tools.QuestionToolName:
			if qSvc := factory.QuestionService(); qSvc != nil {
				return tools.NewQuestionTool(qSvc, permissions)
			}
			return nil
		case tools.TodoWriteToolName:
			if store := factory.TodoStore(); store != nil {
				return tools.NewTodoWriteTool(store)
			}
			return nil
		case tools.MonitorToolName:
			return tools.NewMonitorTool(permissions, reg)
		case tools.TaskListToolName:
			return tools.NewTaskListTool()
		case tools.TaskStopToolName:
			return tools.NewTaskStopTool(permissions, reg)
		case tools.RouterSendToolName:
			// Conditional registration per chat-bridge-agent-tool spec:
			// (a) agent mode (enforced by managerToolNames branch's
			//     info.Mode == AgentModeAgent gate) and
			// (b) at least one configured + enabled channel.
			sender, cfg, mediaRoot := factory.BridgeSender()
			if sender == nil || !tools.ShouldRegisterRouterSend(cfg) {
				return nil
			}
			return tools.NewRouterSendTool(tools.RouterSendDeps{
				Sender:    sender,
				Cfg:       cfg,
				MediaRoot: mediaRoot,
			})
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

	// Only add websearch tool if providers are configured
	cfg := config.Get()
	if cfg != nil && cfg.WebSearch != nil && len(cfg.WebSearch.Providers) > 0 {
		if reg.IsToolEnabled(agentID, tools.WebSearchToolName) {
			if t := createTool(tools.WebSearchToolName); t != nil {
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
		// Cron tools are default-deny: an agent must opt in by setting the
		// tool to true in its config. Without this hivemind would inherit
		// "enabled" for any tool not explicitly listed in its Tools map.
		isCronTool := name == tools.CronCreateToolName ||
			name == tools.CronDeleteToolName ||
			name == tools.CronListToolName

		var enabled bool
		if isCronTool {
			enabled = reg.IsToolExplicitlyEnabled(agentID, name)
		} else {
			enabled = reg.IsToolEnabled(agentID, name)
		}

		if enabled {
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
			result <- tools.NewLspTool(lspService)
		}
	}()

	go func() {
		wg.Wait()
		close(result)
	}()

	return result
}

func (a *agent) Tools() []tools.BaseTool {
	return a.resolveTools()
}

// ResolvedTools returns the current tool set without blocking.
// The bool is true once tools have finished loading.
func (a *agent) ResolvedTools() ([]tools.BaseTool, bool) {
	if a.toolsResolved.Load() {
		return a.tools, true
	}
	return nil, false
}

func (a *agent) resolveTools() []tools.BaseTool {
	a.toolsOnce.Do(func() {
		toolSet := make([]tools.BaseTool, 0, 20)
		toolNames := make([]string, 0, 20)
		for t := range a.toolsCh {
			toolSet = append(toolSet, t)
		}
		toolSet = OrderTools(toolSet)
		for _, t := range toolSet {
			toolNames = append(toolNames, t.Info().Name)
		}
		a.tools = toolSet
		a.toolsResolved.Store(true)
		logging.Info("Resolved tool set", "agent", a.AgentID(), "tools", strings.Join(toolNames, ", "))
	})
	return a.tools
}

// OrderTools partitions tools into baseline (preserving original order) followed
// by external/MCP tools (sorted by name). This guarantees a deterministic tool
// list for stable LLM cache prefixes.
func OrderTools(toolSet []tools.BaseTool) []tools.BaseTool {
	var baseline, external []tools.BaseTool
	for _, t := range toolSet {
		if t.IsBaseline() {
			baseline = append(baseline, t)
		} else {
			external = append(external, t)
		}
	}
	sort.Slice(external, func(i, j int) bool {
		return external[i].Info().Name < external[j].Info().Name
	})
	return append(baseline, external...)
}

package agent

import (
	"context"
	"errors"
	"slices"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
)

func mcpCompositionEnabled(info agentregistry.AgentInfo, cfg *config.Config) bool {
	if cfg == nil || cfg.MCPComposition == nil || len(cfg.MCPComposition.ReadTools) == 0 {
		return false
	}
	if info.AllowTools != nil {
		return slices.Contains(info.AllowTools, tools.MCPComposeToolName)
	}
	return info.Tools[tools.MCPComposeToolName]
}

func filterCompositionNameCollision(all []tools.BaseTool) []tools.BaseTool {
	builtin := false
	count := 0
	for _, tool := range all {
		if tool.Info().Name == tools.MCPComposeToolName {
			count++
			builtin = builtin || tools.IsMCPCompositionTool(tool)
		}
	}
	if !builtin || count < 2 {
		return all
	}
	logging.Warn("mcp_compose disabled because its name conflicts with another tool")
	return slices.DeleteFunc(all, func(tool tools.BaseTool) bool { return tool.Info().Name == tools.MCPComposeToolName })
}

func (a *agent) compositionChildTool(ctx context.Context, name string) (tools.BaseTool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tools.IsToolDenied(ctx, name) || tools.IsToolDenied(ctx, tools.MCPComposeToolName) {
		return nil, errors.New("MCP tool is denied by the flow step")
	}
	sessionID, messageID := tools.GetContextValues(ctx)
	if a.reg == nil || sessionID == "" || messageID == "" || tools.GetAgentID(ctx) != a.agentID {
		return nil, errors.New("composition context is unavailable")
	}
	info, ok := a.reg.Get(string(a.agentID))
	cfg := config.Get()
	if !ok || !mcpCompositionEnabled(info, cfg) || !a.reg.IsToolEnabled(string(a.agentID), tools.MCPComposeToolName) ||
		!slices.Contains(cfg.MCPComposition.ReadTools, name) || !a.reg.IsToolEnabled(string(a.agentID), name) {
		return nil, errors.New("MCP tool is not eligible for composition")
	}
	var found tools.BaseTool
	for _, candidate := range a.resolveTools() {
		if candidate.Info().Name != name {
			continue
		}
		if found != nil {
			return nil, errors.New("MCP tool name is ambiguous")
		}
		found = candidate
	}
	inner := found
	if wrapped, ok := inner.(*tools.DeferredWrapper); ok {
		// Discovery controls schema delivery, not authorization. The plan
		// already names an exact operator-reviewed, agent-granted read.
		// Resolve it internally without activating the wrapper or expanding
		// the model's tool catalog/cache prefix.
		inner = wrapped.Unwrap()
	}
	leaf, ok := inner.(*mcpTool)
	if !ok || leaf.tool.Annotations.ReadOnlyHint != nil && !*leaf.tool.Annotations.ReadOnlyHint {
		return nil, errors.New("MCP tool is not eligible for composition")
	}
	server, ok := cfg.MCPServers[leaf.mcpName]
	if !ok || server.Disabled {
		return nil, errors.New("MCP server is unavailable for composition")
	}
	return found, nil
}

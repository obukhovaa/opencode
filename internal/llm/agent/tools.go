package agent

import (
	"context"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/session"
)

func CoderAgentTools(
	permissions permission.Service,
	sessions session.Service,
	messages message.Service,
	history history.Service,
	lspClients map[string]*lsp.Client,
	reg agentregistry.Registry,
) []tools.BaseTool {
	ctx := context.Background()
	// TODO: allow to disable mcp per agent and then load them for other agents too
	otherTools := GetMcpTools(ctx, permissions, reg)
	if len(lspClients) > 0 {
		otherTools = append(otherTools, tools.NewLspTool(lspClients))
	}
	coderTools := append(
		[]tools.BaseTool{
			tools.NewEditTool(lspClients, permissions, history, reg),
			tools.NewMultiEditTool(lspClients, permissions, history, reg),
			tools.NewFetchTool(permissions),
			tools.NewGlobTool(),
			tools.NewGrepTool(),
			tools.NewLsTool(config.Get()),
			tools.NewSkillTool(permissions, reg),
			tools.NewSourcegraphTool(),
			tools.NewViewTool(lspClients),
			tools.NewViewImageTool(),
			tools.NewPatchTool(lspClients, permissions, history, reg),
			tools.NewWriteTool(lspClients, permissions, history, reg),
			tools.NewDeleteTool(permissions, history, reg),
			tools.NewBashTool(permissions, reg),
			NewAgentTool(sessions, messages, lspClients, permissions, history, reg),
		}, otherTools...,
	)
	return coderTools
}

func HivemindAgentTools(
	sessions session.Service,
	messages message.Service,
	lspClients map[string]*lsp.Client,
	permissions permission.Service,
	history history.Service,
	reg agentregistry.Registry,
) []tools.BaseTool {
	return []tools.BaseTool{
		tools.NewGlobTool(),
		tools.NewGrepTool(),
		tools.NewLsTool(config.Get()),
		tools.NewViewTool(lspClients),
		tools.NewViewImageTool(),
		tools.NewFetchTool(permissions),
		tools.NewSkillTool(permissions, reg),
		NewAgentTool(sessions, messages, lspClients, permissions, history, reg),
	}
}

func ExplorerAgentTools(lspClients map[string]*lsp.Client, permissions permission.Service, reg agentregistry.Registry) []tools.BaseTool {
	return []tools.BaseTool{
		tools.NewGlobTool(),
		tools.NewGrepTool(),
		tools.NewLsTool(config.Get()),
		tools.NewSourcegraphTool(),
		tools.NewSkillTool(permissions, reg),
		tools.NewViewTool(lspClients),
		tools.NewViewImageTool(),
		tools.NewFetchTool(permissions),
	}
}

func WorkhorseAgentTools(
	lspClients map[string]*lsp.Client,
	permissions permission.Service,
	sessions session.Service,
	messages message.Service,
	history history.Service,
	reg agentregistry.Registry,
) []tools.BaseTool {
	var workhorse []tools.BaseTool
	workhorse = []tools.BaseTool{
		tools.NewEditTool(lspClients, permissions, history, reg),
		tools.NewMultiEditTool(lspClients, permissions, history, reg),
		tools.NewFetchTool(permissions),
		tools.NewGlobTool(),
		tools.NewGrepTool(),
		tools.NewLsTool(config.Get()),
		tools.NewSkillTool(permissions, reg),
		tools.NewSourcegraphTool(),
		tools.NewViewTool(lspClients),
		tools.NewViewImageTool(),
		tools.NewPatchTool(lspClients, permissions, history, reg),
		tools.NewWriteTool(lspClients, permissions, history, reg),
		tools.NewDeleteTool(permissions, history, reg),
		tools.NewBashTool(permissions, reg),
	}
	if len(lspClients) > 0 {
		workhorse = append(workhorse, tools.NewLspTool(lspClients))
	}
	return workhorse
}

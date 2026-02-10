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
	otherTools := GetMcpTools(ctx, permissions)
	if len(lspClients) > 0 {
		otherTools = append(otherTools, tools.NewLspTool(lspClients))
	}
	coderTools := append(
		[]tools.BaseTool{
			tools.NewBashTool(permissions),
			tools.NewEditTool(lspClients, permissions, history),
			tools.NewMultiEditTool(lspClients, permissions, history),
			tools.NewFetchTool(permissions),
			tools.NewGlobTool(),
			tools.NewGrepTool(),
			tools.NewLsTool(config.Get()),
			tools.NewSkillTool(permissions),
			tools.NewSourcegraphTool(),
			tools.NewViewTool(lspClients),
			tools.NewViewImageTool(),
			tools.NewPatchTool(lspClients, permissions, history),
			tools.NewWriteTool(lspClients, permissions, history),
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
	reg agentregistry.Registry,
) []tools.BaseTool {
	return []tools.BaseTool{
		tools.NewGlobTool(),
		tools.NewGrepTool(),
		tools.NewLsTool(config.Get()),
		tools.NewViewTool(lspClients),
		tools.NewViewImageTool(),
		tools.NewFetchTool(permissions),
		tools.NewSkillTool(permissions),
		NewAgentTool(sessions, messages, lspClients, permissions, nil, reg),
	}
}

func ExplorerAgentTools(lspClients map[string]*lsp.Client, permissions permission.Service) []tools.BaseTool {
	return []tools.BaseTool{
		tools.NewGlobTool(),
		tools.NewGrepTool(),
		tools.NewLsTool(config.Get()),
		tools.NewSourcegraphTool(),
		tools.NewSkillTool(permissions),
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
) []tools.BaseTool {
	var workhorse []tools.BaseTool
	if history != nil {
		workhorse = []tools.BaseTool{
			tools.NewBashTool(permissions),
			tools.NewEditTool(lspClients, permissions, history),
			tools.NewMultiEditTool(lspClients, permissions, history),
			tools.NewPatchTool(lspClients, permissions, history),
			tools.NewWriteTool(lspClients, permissions, history),
		}
	} else {
		workhorse = []tools.BaseTool{
			tools.NewBashTool(permissions),
		}
	}
	workhorse = append(workhorse,
		tools.NewFetchTool(permissions),
		tools.NewGlobTool(),
		tools.NewGrepTool(),
		tools.NewLsTool(config.Get()),
		tools.NewSkillTool(permissions),
		tools.NewSourcegraphTool(),
		tools.NewViewTool(lspClients),
		tools.NewViewImageTool(),
	)
	if len(lspClients) > 0 {
		workhorse = append(workhorse, tools.NewLspTool(lspClients))
	}
	return workhorse
}

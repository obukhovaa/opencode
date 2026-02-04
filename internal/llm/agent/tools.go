package agent

import (
	"context"
	"sync"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/session"
)

var (
	coderToolsOnce = new(sync.Once)
	coderTools     []tools.BaseTool
	taskToolsOnce  = new(sync.Once)
	taskTools      []tools.BaseTool
)

func CoderAgentTools(
	permissions permission.Service,
	sessions session.Service,
	messages message.Service,
	history history.Service,
	lspClients map[string]*lsp.Client,
) []tools.BaseTool {
	coderToolsOnce.Do(func() {
		ctx := context.Background()
		otherTools := GetMcpTools(ctx, permissions)
		if len(lspClients) > 0 {
			otherTools = append(otherTools, tools.NewDiagnosticsTool(lspClients))
		}
		coderTools = append(
			[]tools.BaseTool{
				tools.NewBashTool(permissions),
				tools.NewEditTool(lspClients, permissions, history),
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
				NewAgentTool(sessions, messages, lspClients, permissions),
			}, otherTools...,
		)
	})
	return coderTools
}

func TaskAgentTools(lspClients map[string]*lsp.Client, permissions permission.Service) []tools.BaseTool {
	taskToolsOnce.Do(func() {
		taskTools = []tools.BaseTool{
			tools.NewGlobTool(),
			tools.NewGrepTool(),
			tools.NewLsTool(config.Get()),
			tools.NewSourcegraphTool(),
			tools.NewSkillTool(permissions),
			tools.NewViewTool(lspClients),
			tools.NewViewImageTool(),
		}
	})
	return taskTools
}

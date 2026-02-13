package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/version"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

type mcpTool struct {
	mcpName     string
	tool        mcp.Tool
	mcpConfig   config.MCPServer
	permissions permission.Service
	reg         agentregistry.Registry
}

type MCPClient interface {
	Initialize(
		ctx context.Context,
		request mcp.InitializeRequest,
	) (*mcp.InitializeResult, error)
	ListTools(ctx context.Context, request mcp.ListToolsRequest) (*mcp.ListToolsResult, error)
	CallTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error)
	Close() error
}

func (b *mcpTool) Info() tools.ToolInfo {
	required := b.tool.InputSchema.Required
	if required == nil {
		required = make([]string, 0)
	}
	return tools.ToolInfo{
		Name:        fmt.Sprintf("%s_%s", b.mcpName, b.tool.Name),
		Description: b.tool.Description,
		Parameters:  b.tool.InputSchema.Properties,
		Required:    required,
	}
}

func runTool(ctx context.Context, c MCPClient, toolName string, input string) (tools.ToolResponse, error) {
	defer c.Close()
	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    "OpenCode",
		Version: version.Version,
	}

	_, err := c.Initialize(ctx, initRequest)
	if err != nil {
		return tools.NewTextErrorResponse(err.Error()), nil
	}

	toolRequest := mcp.CallToolRequest{}
	toolRequest.Params.Name = toolName
	var args map[string]any
	if err = json.Unmarshal([]byte(input), &args); err != nil {
		return tools.NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}
	toolRequest.Params.Arguments = args
	result, err := c.CallTool(ctx, toolRequest)
	if err != nil {
		return tools.NewTextErrorResponse(err.Error()), nil
	}

	output := ""
	for _, v := range result.Content {
		if v, ok := v.(mcp.TextContent); ok {
			output = v.Text
		} else {
			output = fmt.Sprintf("%v", v)
		}
	}

	return tools.NewTextResponse(output), nil
}

func (b *mcpTool) Run(ctx context.Context, params tools.ToolCall) (tools.ToolResponse, error) {
	sessionID, messageID := tools.GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return tools.ToolResponse{}, fmt.Errorf("session ID and message ID are required for creating a new file")
	}

	action := b.reg.EvaluatePermission(string(tools.GetAgentID(ctx)), b.Info().Name, params.Input)
	switch action {
	case permission.ActionAllow:
	case permission.ActionDeny:
		return tools.NewEmptyResponse(), permission.ErrorPermissionDenied
	default:
		permissionDescription := fmt.Sprintf("execute %s with the following parameters: %s", b.Info().Name, params.Input)
		p := b.permissions.Request(
			permission.CreatePermissionRequest{
				SessionID:   sessionID,
				Path:        config.WorkingDirectory(),
				ToolName:    b.Info().Name,
				Action:      "execute",
				Description: permissionDescription,
				Params:      params.Input,
			},
		)
		if !p {
			return tools.NewEmptyResponse(), permission.ErrorPermissionDenied
		}
	}

	switch b.mcpConfig.Type {
	case config.MCPStdio:
		c, err := client.NewStdioMCPClient(
			b.mcpConfig.Command,
			b.mcpConfig.Env,
			b.mcpConfig.Args...,
		)
		if err != nil {
			return tools.NewTextErrorResponse(err.Error()), nil
		}
		return runTool(ctx, c, b.tool.Name, params.Input)
	case config.MCPSse:
		c, err := client.NewSSEMCPClient(
			b.mcpConfig.URL,
			client.WithHeaders(b.mcpConfig.Headers),
		)
		if err != nil {
			return tools.NewTextErrorResponse(err.Error()), nil
		}
		if err := c.Start(ctx); err != nil {
			return tools.NewTextErrorResponse(fmt.Sprintf("failed to start SSE transport: %s", err)), nil
		}
		return runTool(ctx, c, b.tool.Name, params.Input)
	case config.MCPHttp:
		c, err := client.NewStreamableHttpClient(
			b.mcpConfig.URL,
			transport.WithHTTPHeaders(b.mcpConfig.Headers),
		)
		if err != nil {
			return tools.NewTextErrorResponse(err.Error()), nil
		}
		return runTool(ctx, c, b.tool.Name, params.Input)
	}

	return tools.NewTextErrorResponse("invalid mcp type"), nil
}

func NewMcpTool(
	name string,
	tool mcp.Tool,
	permissions permission.Service,
	mcpConfig config.MCPServer,
	reg agentregistry.Registry,
) tools.BaseTool {
	return &mcpTool{
		mcpName:     name,
		tool:        tool,
		mcpConfig:   mcpConfig,
		permissions: permissions,
		reg:         reg,
	}
}

var (
	mcpToolsOnce = new(sync.Once)
	mcpTools     []tools.BaseTool
)

func getTools(ctx context.Context, name string, m config.MCPServer, permissions permission.Service, c MCPClient, reg agentregistry.Registry) []tools.BaseTool {
	var toolsToAdd []tools.BaseTool
	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    "OpenCode",
		Version: version.Version,
	}

	_, err := c.Initialize(ctx, initRequest)
	if err != nil {
		logging.Error("error initializing mcp client", "error", err)
		return toolsToAdd
	}
	toolsRequest := mcp.ListToolsRequest{}
	tools, err := c.ListTools(ctx, toolsRequest)
	if err != nil {
		logging.Error("error listing tools", "error", err)
		return toolsToAdd
	}
	for _, t := range tools.Tools {
		toolsToAdd = append(toolsToAdd, NewMcpTool(name, t, permissions, m, reg))
	}
	defer c.Close()
	return toolsToAdd
}

func GetMcpTools(ctx context.Context, permissions permission.Service, reg agentregistry.Registry) []tools.BaseTool {
	mcpToolsOnce.Do(func() {
		for name, m := range config.Get().MCPServers {
			switch m.Type {
			case config.MCPStdio:
				c, err := client.NewStdioMCPClient(
					m.Command,
					m.Env,
					m.Args...,
				)
				if err != nil {
					logging.Error("error creating mcp client", "error", err)
					continue
				}

				mcpTools = append(mcpTools, getTools(ctx, name, m, permissions, c, reg)...)
			case config.MCPSse:
				c, err := client.NewSSEMCPClient(
					m.URL,
					client.WithHeaders(m.Headers),
				)
				if err != nil {
					logging.Error("error creating mcp client", "error", err)
					continue
				}
				if err := c.Start(ctx); err != nil {
					logging.Error("error starting SSE transport", "error", err)
					continue
				}
				mcpTools = append(mcpTools, getTools(ctx, name, m, permissions, c, reg)...)
			case config.MCPHttp:
				c, err := client.NewStreamableHttpClient(
					m.URL,
					transport.WithHTTPHeaders(m.Headers),
				)
				if err != nil {
					logging.Error("error creating mcp client", "error", err)
					continue
				}
				mcpTools = append(mcpTools, getTools(ctx, name, m, permissions, c, reg)...)
			}
		}
	})
	return mcpTools
}

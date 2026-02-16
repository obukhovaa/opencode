package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

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

type (
	MCPClient interface {
		Initialize(
			ctx context.Context,
			request mcp.InitializeRequest,
		) (*mcp.InitializeResult, error)
		ListTools(ctx context.Context, request mcp.ListToolsRequest) (*mcp.ListToolsResult, error)
		CallTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error)
		Close() error
	}

	MCPRegistry interface {
		// GetTools return tools matching filter, if registry is not loaded it will begin loading
		LoadTools(ctx context.Context, filter *MCPRegistryFiler) <-chan tools.BaseTool
		// StartClient starts a new MCPClient, caller have to properly close when done
		StartClient(ctx context.Context, name string) (c *client.Client, err error)
	}
	MCPRegistryFiler struct {
		ToolNames   []string
		ServerNames []string
	}
	mcpRegistry struct {
		// *mcp.ListToolsResult by MCP server name
		mcpTools sync.Map

		permissions   permission.Service
		agentRegistry agentregistry.Registry
	}

	mcpTool struct {
		mcpName     string
		tool        mcp.Tool
		mcpConfig   config.MCPServer
		permissions permission.Service
		reg         agentregistry.Registry
		mcpReg      MCPRegistry
	}
)

func NewMCPRegistry(permissions permission.Service, agentRegistry agentregistry.Registry) MCPRegistry {
	return &mcpRegistry{
		mcpTools:      sync.Map{},
		permissions:   permissions,
		agentRegistry: agentRegistry,
	}
}

func (r *mcpRegistry) StartClient(ctx context.Context, name string) (c *client.Client, err error) {
	m, ok := config.Get().MCPServers[name]
	if !ok {
		return nil, fmt.Errorf("no mcp found with name %s", name)
	}

	startCtx, cancelStart := context.WithTimeout(ctx, 20*time.Second)
	defer cancelStart()
	switch m.Type {
	case config.MCPStdio:
		c, err = client.NewStdioMCPClient(
			m.Command,
			m.Env,
			m.Args...,
		)
	case config.MCPSse:
		c, err = client.NewSSEMCPClient(
			m.URL,
			client.WithHeaders(m.Headers),
		)
	case config.MCPHttp:
		c, err = client.NewStreamableHttpClient(
			m.URL,
			transport.WithHTTPHeaders(m.Headers),
		)
	}
	if err != nil {
		logging.Error("Error creating MCP client", "server", name, "cause", err)
		return nil, err
	}
	if err = c.Start(startCtx); err != nil {
		logging.Error("Error starting MCP client", "server", m.Command, "cause", err)
		return nil, err
	}
	return c, nil
}

func (r *mcpRegistry) LoadTools(ctx context.Context, filter *MCPRegistryFiler) <-chan tools.BaseTool {
	toolsCh := make(chan tools.BaseTool, 100)

	go func(ctx context.Context, filter *MCPRegistryFiler) {
		wg := sync.WaitGroup{}
		for name, m := range config.Get().MCPServers {
			if filter != nil && len(filter.ServerNames) != 0 && !slices.Contains(filter.ServerNames, name) {
				continue
			}

			wg.Add(1)
			go func(ctx context.Context, filter *MCPRegistryFiler) {
				defer wg.Done()

				toolsCtx, cancelTools := context.WithTimeout(ctx, 30*time.Second)
				defer cancelTools()
				for _, t := range r.getTools(toolsCtx, name, m) {
					if filter != nil && len(filter.ToolNames) != 0 && !slices.Contains(filter.ToolNames, t.Info().Name) {
						continue
					}
					toolsCh <- t
				}
			}(ctx, filter)
		}
		wg.Wait()
		close(toolsCh)
	}(ctx, filter)
	return toolsCh
}

const (
	ttl = 30 * time.Minute
)

type toolsCacheEntry struct {
	done chan bool
	data *mcp.ListToolsResult
	ts   int64
	err  error
	del  atomic.Bool
}

func (entry *toolsCacheEntry) expired() bool {
	now := time.Now().UnixMilli()
	return now > entry.ts+ttl.Milliseconds()
}

func (r *mcpRegistry) getTools(ctx context.Context, name string, m config.MCPServer) []tools.BaseTool {
	toolsToAdd := []tools.BaseTool{}
	entry := &toolsCacheEntry{done: make(chan bool)}
	value, loaded := r.mcpTools.LoadOrStore(name, entry)

	if loaded {
		entry = value.(*toolsCacheEntry)
	}

	if loaded {
		// cache/reuse
		select {
		case <-ctx.Done():
			return toolsToAdd
		case <-entry.done:
			// entry is a pointer, and close(done) provides
			// happens-before: entry.data and entry.err are
			// visible directly
			if entry.expired() && entry.del.CompareAndSwap(false, true) {
				logging.Debug("MCP client cache expired", "server", name, "ts", entry.ts)
				r.mcpTools.Delete(name)
			} else {
				logging.Debug("MCP client cache is used", "server", name, "ts", entry.ts)
			}
		}
	} else {
		// fetch
		defer close(entry.done)

		var c *client.Client
		c, entry.err = r.StartClient(ctx, name)
		if entry.err != nil {
			logging.Error("Error starting mcp client", "server", name, "cause", entry.err.Error())
			r.mcpTools.Delete(name)
			return toolsToAdd
		}
		defer c.Close()

		initRequest := mcp.InitializeRequest{}
		initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		initRequest.Params.ClientInfo = mcp.Implementation{
			Name:    "opencode",
			Version: version.Version,
		}

		_, entry.err = c.Initialize(ctx, initRequest)
		if entry.err != nil {
			logging.Error("Error initializing mcp client", "server", name, "cause", entry.err.Error())
			r.mcpTools.Delete(name)
			return toolsToAdd
		}
		toolsRequest := mcp.ListToolsRequest{}
		entry.data, entry.err = c.ListTools(ctx, toolsRequest)
		if entry.err != nil {
			logging.Error("Error listing mcp tools", "server", name, "cause", entry.err.Error())
			r.mcpTools.Delete(name)
			return toolsToAdd
		}
		entry.ts = time.Now().UnixMilli()
		logging.Debug("MCP client cache is updated", "server", name, "ts", entry.ts)
	}

	if entry.err != nil {
		return toolsToAdd
	}

	if entry.data != nil {
		for _, t := range entry.data.Tools {
			toolsToAdd = append(toolsToAdd, newMCPTool(name, t, r.permissions, m, r.agentRegistry, r))
		}
	}
	return toolsToAdd
}

func newMCPTool(
	name string,
	tool mcp.Tool,
	permissions permission.Service,
	mcpConfig config.MCPServer,
	reg agentregistry.Registry,
	mcpReg MCPRegistry,
) tools.BaseTool {
	return &mcpTool{
		mcpName:     name,
		tool:        tool,
		mcpConfig:   mcpConfig,
		permissions: permissions,
		reg:         reg,
		mcpReg:      mcpReg,
	}
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

	c, err := b.mcpReg.StartClient(ctx, b.mcpName)
	if err != nil {
		return tools.NewTextErrorResponse(err.Error()), nil
	}
	defer c.Close()
	return runTool(ctx, c, b.tool.Name, params.Input)
}

func runTool(ctx context.Context, c MCPClient, toolName string, input string) (tools.ToolResponse, error) {
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

// Package mcpserve exposes workspace CLI tool manifests over the Model
// Context Protocol (stdio), so Claude Code and any other MCP client consume
// the same definitions — and the same policy and executor — as opencode's
// native tools. `opencode tools serve` is the command around it.
package mcpserve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/opencode-ai/opencode/internal/clitool"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/permission"
)

// ServerName is the MCP implementation name announced on initialize.
const ServerName = "opencode-cli-tools"

// Source names the scratch-file prefix for spilled output in serve mode.
const Source = "clitool-mcp"

// New builds an MCP server with one tool per manifest.
func New(manifests []*clitool.Manifest, version string) *server.MCPServer {
	s := server.NewMCPServer(ServerName, version, server.WithToolCapabilities(false))
	for _, m := range manifests {
		s.AddTool(toolFor(m), handlerFor(m))
	}
	return s
}

// toolFor renders the manifest's input schema as a raw JSON Schema object.
func toolFor(m *clitool.Manifest) mcp.Tool {
	props, required := m.InputSchema()
	schema, _ := json.Marshal(map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	})
	return mcp.NewToolWithRawSchema(m.Name, m.FullDescription(), schema)
}

// handlerFor runs one call through the shared path: Prepare (input
// validation, template rendering, argument policy) → manifest default
// permission (deny refuses; ask and allow run — the MCP client owns the
// human prompt) → Exec → FormatResult → output cap.
func handlerFor(m *clitool.Manifest) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		if args == nil {
			args = map[string]any{}
		}
		inv, err := m.Prepare(args)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		joined := clitool.JoinArgs(inv.Args)
		if act := permission.EvaluateToolPermissionWithDefault(m.Name, joined, nil, nil, m.PermissionDefault()); act == permission.ActionDeny {
			return mcp.NewToolResultError(fmt.Sprintf("denied by the tool's default permission for %q (manifest %s)", joined, m.Location)), nil
		}
		res := m.Exec(ctx, inv)
		text, isError := clitool.FormatResult(res)
		preview, _ := tools.PersistLargeOutput(text, m.Name, Source, m.MaxOutput())
		if isError {
			return mcp.NewToolResultError(preview), nil
		}
		return mcp.NewToolResultText(preview), nil
	}
}

// Serve runs the server over the given streams until ctx is cancelled or in
// is closed. Logs go to errLog (stderr in the command) so out carries only
// protocol messages.
func Serve(ctx context.Context, s *server.MCPServer, in io.Reader, out io.Writer, errLog *log.Logger) error {
	std := server.NewStdioServer(s)
	if errLog != nil {
		std.SetErrorLogger(errLog)
	}
	return std.Listen(ctx, in, out)
}

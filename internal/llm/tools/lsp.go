package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/protocol"
)

type LspParams struct {
	Operation string `json:"operation"`
	FilePath  string `json:"filePath"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

type lspTool struct {
	lspClients map[string]*lsp.Client
}

const (
	LspToolName    = "lsp"
	lspDescription = `Interact with Language Server Protocol (LSP) servers to get code intelligence features.

Supported operations:
- goToDefinition: Find where a symbol is defined
- findReferences: Find all references to a symbol
- hover: Get hover information (documentation, type info) for a symbol
- documentSymbol: Get all symbols (functions, classes, variables) in a document
- workspaceSymbol: Search for symbols across the entire workspace
- goToImplementation: Find implementations of an interface or abstract method
- prepareCallHierarchy: Get call hierarchy item at a position (functions/methods)
- incomingCalls: Find all functions/methods that call the function at a position
- outgoingCalls: Find all functions/methods called by the function at a position

All operations require:
- filePath: The file to operate on
- line: The line number (1-based, as shown in editors)
- character: The character offset (1-based, as shown in editors)

Note: LSP servers must be running for the file type. If no server is available, an error will be returned.
`
)

var validOperations = map[string]bool{
	"goToDefinition":       true,
	"findReferences":       true,
	"hover":                true,
	"documentSymbol":       true,
	"workspaceSymbol":      true,
	"goToImplementation":   true,
	"prepareCallHierarchy": true,
	"incomingCalls":        true,
	"outgoingCalls":        true,
}

func NewLspTool(lspClients map[string]*lsp.Client) BaseTool {
	return &lspTool{lspClients}
}

func (t *lspTool) Info() ToolInfo {
	return ToolInfo{
		Name:        LspToolName,
		Description: lspDescription,
		Parameters: map[string]any{
			"operation": map[string]any{
				"type":        "string",
				"description": "The LSP operation to perform",
				"enum":        []string{"goToDefinition", "findReferences", "hover", "documentSymbol", "workspaceSymbol", "goToImplementation", "prepareCallHierarchy", "incomingCalls", "outgoingCalls"},
			},
			"filePath": map[string]any{
				"type":        "string",
				"description": "The absolute or relative path to the file",
			},
			"line": map[string]any{
				"type":        "integer",
				"description": "The line number (1-based, as shown in editors)",
			},
			"character": map[string]any{
				"type":        "integer",
				"description": "The character offset (1-based, as shown in editors)",
			},
		},
		Required: []string{"operation", "filePath", "line", "character"},
	}
}

func (t *lspTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params LspParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}

	if !validOperations[params.Operation] {
		return NewTextErrorResponse(fmt.Sprintf("invalid operation: %s", params.Operation)), nil
	}

	file := params.FilePath
	if !filepath.IsAbs(file) {
		file = filepath.Join(config.WorkingDirectory(), file)
	}

	if _, err := os.Stat(file); os.IsNotExist(err) {
		return NewTextErrorResponse(fmt.Sprintf("file not found: %s", file)), nil
	}

	// Find LSP clients that handle this file type
	clients := findClientsForFile(file, t.lspClients)
	if len(clients) == 0 {
		return NewTextErrorResponse("no LSP server available for this file type"), nil
	}

	// Ensure the file is open in all matching clients
	for _, client := range clients {
		if err := client.OpenFile(ctx, file); err != nil {
			continue
		}
	}

	// Convert 1-based to 0-based positions
	line := uint32(params.Line - 1)
	character := uint32(params.Character - 1)
	uri := protocol.DocumentUri("file://" + file)

	textDocPos := protocol.TextDocumentPositionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
		Position:     protocol.Position{Line: line, Character: character},
	}

	relPath, _ := filepath.Rel(config.WorkingDirectory(), file)
	title := fmt.Sprintf("%s %s:%d:%d", params.Operation, relPath, params.Line, params.Character)

	// Try each client until one succeeds
	var lastErr error
	for _, client := range clients {
		result, err := executeLspOperation(ctx, client, params.Operation, uri, textDocPos)
		if err != nil {
			lastErr = err
			continue
		}

		output := formatLspResult(params.Operation, result)
		return WithResponseMetadata(NewTextResponse(output), map[string]string{"title": title}), nil
	}

	if lastErr != nil {
		return NewTextErrorResponse(fmt.Sprintf("LSP operation failed: %s", lastErr)), nil
	}
	return NewTextResponse("No results found"), nil
}

func findClientsForFile(filePath string, clients map[string]*lsp.Client) []*lsp.Client {
	ext := strings.ToLower(filepath.Ext(filePath))
	var matched []*lsp.Client

	for _, client := range clients {
		if slices.Contains(client.GetExtensions(), ext) {
			matched = append(matched, client)
		}
	}
	return matched
}

func executeLspOperation(ctx context.Context, client *lsp.Client, operation string, uri protocol.DocumentUri, pos protocol.TextDocumentPositionParams) (any, error) {
	switch operation {
	case "goToDefinition":
		return client.Definition(ctx, protocol.DefinitionParams{TextDocumentPositionParams: pos})

	case "findReferences":
		return client.References(ctx, protocol.ReferenceParams{
			TextDocumentPositionParams: pos,
			Context: protocol.ReferenceContext{
				IncludeDeclaration: true,
			},
		})

	case "hover":
		return client.Hover(ctx, protocol.HoverParams{TextDocumentPositionParams: pos})

	case "documentSymbol":
		return client.DocumentSymbol(ctx, protocol.DocumentSymbolParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
		})

	case "workspaceSymbol":
		return client.Symbol(ctx, protocol.WorkspaceSymbolParams{Query: ""})

	case "goToImplementation":
		return client.Implementation(ctx, protocol.ImplementationParams{TextDocumentPositionParams: pos})

	case "prepareCallHierarchy":
		return client.PrepareCallHierarchy(ctx, protocol.CallHierarchyPrepareParams{TextDocumentPositionParams: pos})

	case "incomingCalls":
		items, err := client.PrepareCallHierarchy(ctx, protocol.CallHierarchyPrepareParams{TextDocumentPositionParams: pos})
		if err != nil || len(items) == 0 {
			return nil, fmt.Errorf("no call hierarchy item found at position")
		}
		return client.IncomingCalls(ctx, protocol.CallHierarchyIncomingCallsParams{Item: items[0]})

	case "outgoingCalls":
		items, err := client.PrepareCallHierarchy(ctx, protocol.CallHierarchyPrepareParams{TextDocumentPositionParams: pos})
		if err != nil || len(items) == 0 {
			return nil, fmt.Errorf("no call hierarchy item found at position")
		}
		return client.OutgoingCalls(ctx, protocol.CallHierarchyOutgoingCallsParams{Item: items[0]})

	default:
		return nil, fmt.Errorf("unsupported operation: %s", operation)
	}
}

func formatLspResult(operation string, result any) string {
	if result == nil {
		return fmt.Sprintf("No results found for %s", operation)
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Sprintf("Error formatting result: %s", err)
	}

	output := string(data)
	if output == "null" || output == "[]" || output == "{}" {
		return fmt.Sprintf("No results found for %s", operation)
	}

	return output
}

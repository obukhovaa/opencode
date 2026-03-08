package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/diff"
)

type ToolInfo struct {
	Name        string
	Description string
	Parameters  map[string]any
	Required    []string
	// TODO: Consider to add Output parameters: https://modelcontextprotocol.io/specification/2025-06-18/server/tools#output-schema
}

type toolResponseType string

type (
	sessionIDContextKey   string
	messageIDContextKey   string
	isTaskAgentContextKey string
	agentIDContextKey     string
)

const (
	ToolResponseTypeText  toolResponseType = "text"
	ToolResponseTypeImage toolResponseType = "image"

	SessionIDContextKey   sessionIDContextKey   = "session_id"
	MessageIDContextKey   messageIDContextKey   = "message_id"
	IsTaskAgentContextKey isTaskAgentContextKey = "is_task_agent"
	AgentIDContextKey     agentIDContextKey     = "agent_id"

	// MaxToolResponseTokens is the maximum number of tokens allowed in a tool response
	// to prevent context overflow. ~1200KB of text content.
	MaxToolResponseTokens = 300_000
)

type toolResponse struct {
	Type     toolResponseType `json:"type"`
	Content  string           `json:"content"`
	Metadata string           `json:"metadata,omitempty"`
	IsError  bool             `json:"is_error"`
}

// ToolResponse is the public interface for tool responses
type ToolResponse = toolResponse

// validateAndTruncate validates the tool response size and truncates if necessary.
// Truncation is line-aligned to avoid cutting mid-line or mid-UTF-8 character.
func validateAndTruncate(response toolResponse) toolResponse {
	// Rough estimation: ~4 characters per token
	estimatedTokens := len(response.Content) / 4

	if estimatedTokens > MaxToolResponseTokens {
		maxChars := MaxToolResponseTokens * 4
		truncated := truncateToMaxChars(response.Content, maxChars)
		response.Content = truncated + "\n\n[Output truncated due to size limit. Consider using more specific search parameters or viewing smaller sections.]"
	}

	return response
}

func NewTextResponse(content string) toolResponse {
	return validateAndTruncate(toolResponse{
		Type:    ToolResponseTypeText,
		Content: content,
	})
}

func NewImageResponse(content string) toolResponse {
	return toolResponse{
		Type:    ToolResponseTypeImage,
		Content: content,
	}
}

func NewEmptyResponse() toolResponse {
	return toolResponse{
		Type:    ToolResponseTypeText,
		Content: "",
	}
}

func WithResponseMetadata(response toolResponse, metadata any) toolResponse {
	if metadata != nil {
		metadataBytes, err := json.Marshal(metadata)
		if err != nil {
			return response
		}
		response.Metadata = string(metadataBytes)
	}
	return response
}

func NewTextErrorResponse(content string) toolResponse {
	return validateAndTruncate(toolResponse{
		Type:    ToolResponseTypeText,
		Content: content,
		IsError: true,
	})
}

type ToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
}

type BaseTool interface {
	Info() ToolInfo
	Run(ctx context.Context, params ToolCall) (ToolResponse, error)
	AllowParallelism(call ToolCall, allCalls []ToolCall) bool
}

var mutatingToolNames = map[string]bool{
	EditToolName:      true,
	WriteToolName:     true,
	MultiEditToolName: true,
	DeleteToolName:    true,
	PatchToolName:     true,
}

func IsMutatingTool(name string) bool {
	return mutatingToolNames[name]
}

func ExtractPathsFromCall(call ToolCall) []string {
	switch call.Name {
	case PatchToolName:
		var params struct {
			PatchText string `json:"patch_text"`
		}
		if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
			return nil
		}
		paths := diff.IdentifyFilesNeeded(params.PatchText)
		paths = append(paths, diff.IdentifyFilesAdded(params.PatchText)...)
		return paths
	default:
		var common struct {
			FilePath string `json:"file_path"`
			Path     string `json:"path"`
		}
		if err := json.Unmarshal([]byte(call.Input), &common); err != nil {
			return nil
		}
		var paths []string
		if common.FilePath != "" {
			paths = append(paths, common.FilePath)
		}
		if common.Path != "" {
			paths = append(paths, common.Path)
		}
		return paths
	}
}

func hasFileConflict(call ToolCall, myPaths []string, allCalls []ToolCall) bool {
	for _, other := range allCalls {
		if other.ID == call.ID {
			continue
		}
		if !IsMutatingTool(other.Name) {
			continue
		}
		otherPaths := ExtractPathsFromCall(other)
		for _, mp := range myPaths {
			if slices.Contains(otherPaths, mp) {
				return true
			}
		}
	}
	return false
}

func IsSafeReadOnlyCommand(command string) bool {
	cmdLower := strings.ToLower(command)
	for _, safe := range safeReadOnlyCommands {
		if strings.HasPrefix(cmdLower, strings.ToLower(safe)) {
			if len(cmdLower) == len(safe) || cmdLower[len(safe)] == ' ' || cmdLower[len(safe)] == '-' {
				return true
			}
		}
	}
	return false
}

func GetContextValues(ctx context.Context) (string, string) {
	sessionID := ctx.Value(SessionIDContextKey)
	messageID := ctx.Value(MessageIDContextKey)
	if sessionID == nil {
		return "", ""
	}
	if messageID == nil {
		return sessionID.(string), ""
	}
	return sessionID.(string), messageID.(string)
}

// IsTaskAgent returns true if the context indicates this is a task agent
func IsTaskAgent(ctx context.Context) bool {
	isTaskAgent := ctx.Value(IsTaskAgentContextKey)
	if isTaskAgent == nil {
		return false
	}
	if val, ok := isTaskAgent.(bool); ok {
		return val
	}
	return false
}

// GetAgentID returns the agent name from context, or empty string if not set
func GetAgentID(ctx context.Context) config.AgentName {
	agentName := ctx.Value(AgentIDContextKey)
	if agentName == nil {
		return ""
	}
	if val, ok := agentName.(config.AgentName); ok {
		return val
	}
	return ""
}

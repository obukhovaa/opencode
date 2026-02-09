package tools

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/permission"
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
	agentNameContextKey   string
)

const (
	ToolResponseTypeText  toolResponseType = "text"
	ToolResponseTypeImage toolResponseType = "image"

	SessionIDContextKey   sessionIDContextKey   = "session_id"
	MessageIDContextKey   messageIDContextKey   = "message_id"
	IsTaskAgentContextKey isTaskAgentContextKey = "is_task_agent"
	AgentNameContextKey   agentNameContextKey   = "agent_name"

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

// validateAndTruncate validates the tool response size and truncates if necessary
func validateAndTruncate(response toolResponse) toolResponse {
	// Rough estimation: ~4 characters per token
	estimatedTokens := len(response.Content) / 4

	if estimatedTokens > MaxToolResponseTokens {
		maxChars := MaxToolResponseTokens * 4
		truncated := response.Content[:maxChars]
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
	return validateAndTruncate(toolResponse{
		Type:    ToolResponseTypeImage,
		Content: content,
	})
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

// GetAgentName returns the agent name from context, or empty string if not set
func GetAgentName(ctx context.Context) config.AgentName {
	agentName := ctx.Value(AgentNameContextKey)
	if agentName == nil {
		return ""
	}
	if val, ok := agentName.(config.AgentName); ok {
		return val
	}
	return ""
}

// evaluateToolPermission evaluates config-based permissions for a tool+input.
// Returns the resolved action (allow/deny/ask) based on agent-specific and global rules.
func evaluateToolPermission(ctx context.Context, toolName, input string) permission.Action {
	cfg := config.Get()
	agentName := GetAgentName(ctx)

	var agentPerms map[string]any
	if agentName != "" && cfg.Agents != nil {
		if agentCfg, ok := cfg.Agents[agentName]; ok {
			if !permission.IsToolEnabled(toolName, agentCfg.Tools) {
				return permission.ActionDeny
			}
			agentPerms = agentCfg.Permission
		}
	}

	globalPerms := make(map[string]any)
	if cfg.Permission != nil {
		if cfg.Permission.Skill != nil {
			globalPerms["skill"] = cfg.Permission.Skill
		}
		maps.Copy(globalPerms, cfg.Permission.Rules)
	}

	return permission.EvaluateToolPermission(toolName, input, agentPerms, globalPerms)
}

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/session"
)

type agentTool struct {
	sessions    session.Service
	messages    message.Service
	lspClients  map[string]*lsp.Client
	permissions permission.Service
}

const (
	TaskToolName = "task"
	// Deprecated: use TaskToolName instead
	AgentToolName = TaskToolName
)

type TaskParams struct {
	Prompt       string `json:"prompt"`
	SubagentType string `json:"subagent_type,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
}

// Deprecated: use TaskParams instead
type AgentParams = TaskParams

func (b *agentTool) Info() tools.ToolInfo {
	cfg := config.Get()
	var agentDescs []string
	for name, agentCfg := range cfg.Agents {
		if agentCfg.Mode == config.AgentModeSubagent {
			desc := agentCfg.Description
			if desc == "" {
				desc = "No description available"
			}
			agentDescs = append(agentDescs, fmt.Sprintf("- %s: %s", name, desc))
		}
	}
	if len(agentDescs) == 0 {
		for _, tool := range TaskAgentTools(b.lspClients, b.permissions) {
			agentDescs = append(agentDescs, tool.Info().Name)
		}
	}

	availableAgents := strings.Join(agentDescs, "\n")

	description := "Launch a new agent to handle complex, multistep tasks autonomously.\n\n" +
		"Available subagent types:\n" + availableAgents + "\n\n" +
		"When to use the Task tool:\n" +
		"- When you have to coordinate work across different subagents with or without explicitly provided Flow.\n" +
		"- When you are searching for a keyword or file and are not confident that you will find the right match on the first try.\n" +
		"- When you need to inspect and analyze images, use the agent tool to perform the search and inspection for you.\n\n" +
		"When NOT to use the Task tool:\n" +
		"- If you want to read a specific file path, use the view or glob tool instead of the Task tool, to find the match more quickly\n" +
		"- If you are searching for a specific class definition like \"class Foo\", use the glob tool instead, to find the match more quickly\n\n" +
		"Usage notes:\n" +
		"1. Launch multiple agents concurrently whenever possible, to maximize performance; to do that, use a single message with multiple tool uses\n" +
		"2. When the agent is done, it will return a single message back to you. The result returned by the agent is not visible to the user. To show the user the result, you should send a text message back to the user with a concise summary of the result.\n" +
		"3. Each agent invocation starts with a fresh context unless you provide task_id to resume the same subagent session (which continues with its previous messages and tool outputs). When starting fresh, your prompt should contain a highly detailed task description for the agent to perform autonomously and you should specify exactly what information the agent should return back to you in its final and only message to you.\n" +
		"4. The agent's outputs should generally be trusted\n" +
		"5. Clearly tell the agent whether you expect it to write code or just to do research (search, file reads, web fetches, etc.), since it is not aware of the user's intent."

	return tools.ToolInfo{
		Name:        TaskToolName,
		Description: description,
		Parameters: map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "The task for the agent to perform",
			},
			"subagent_type": map[string]any{
				"type":        "string",
				"description": "The type of subagent to use (e.g., 'explorer', 'workhorse'). Defaults to 'explorer' if not specified.",
			},
			"task_id": map[string]any{
				"type":        "string",
				"description": "Optional. Provide a task_id from a previous invocation to resume that subagent session with its prior context.",
			},
		},
		Required: []string{"prompt"},
	}
}

func (b *agentTool) Run(ctx context.Context, call tools.ToolCall) (tools.ToolResponse, error) {
	var params TaskParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return tools.NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}
	if params.Prompt == "" {
		return tools.NewTextErrorResponse("prompt is required"), nil
	}

	sessionID, messageID := tools.GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return tools.ToolResponse{}, fmt.Errorf("session_id and message_id are required")
	}

	subagentType := config.AgentExplorer
	if params.SubagentType != "" {
		subagentType = params.SubagentType
	}

	var agentTools []tools.BaseTool
	switch subagentType {
	case config.AgentWorkhorse:
		agentTools = WorkhorseAgentTools(b.lspClients, b.permissions, b.sessions, b.messages, nil)
	default:
		agentTools = TaskAgentTools(b.lspClients, b.permissions)
	}

	a, err := NewAgent(subagentType, b.sessions, b.messages, agentTools)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("error creating agent: %s", err)
	}

	var taskSession session.Session
	isResumed := false
	if params.TaskID != "" {
		existing, getErr := b.sessions.Get(ctx, params.TaskID)
		if getErr == nil {
			taskSession = existing
			isResumed = true
		}
	}
	if !isResumed {
		taskSession, err = b.sessions.CreateTaskSession(ctx, call.ID, sessionID, fmt.Sprintf("%s task", subagentType))
		if err != nil {
			return tools.ToolResponse{}, fmt.Errorf("error creating session: %s", err)
		}
	}

	done, err := a.Run(ctx, taskSession.ID, params.Prompt)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("error generating agent: %s", err)
	}
	result := <-done
	if result.Error != nil {
		return tools.ToolResponse{}, fmt.Errorf("error generating agent: %s", result.Error)
	}

	response := result.Message
	if response.Role != message.Assistant {
		return tools.NewTextErrorResponse("no response"), nil
	}

	updatedSession, err := b.sessions.Get(ctx, taskSession.ID)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("error getting session: %s", err)
	}
	parentSession, err := b.sessions.Get(ctx, sessionID)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("error getting parent session: %s", err)
	}

	parentSession.Cost += updatedSession.Cost

	_, err = b.sessions.Save(ctx, parentSession)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("error saving parent session: %s", err)
	}

	metadata := map[string]string{
		"task_id":       taskSession.ID,
		"subagent_type": subagentType,
		"subagent_name": resolveSubagentName(subagentType),
		"is_resumed":    fmt.Sprintf("%v", isResumed),
	}

	return tools.WithResponseMetadata(tools.NewTextResponse(response.Content().String()), metadata), nil
}

func resolveSubagentName(agentType string) string {
	cfg := config.Get()
	if agentCfg, ok := cfg.Agents[agentType]; ok && agentCfg.Name != "" {
		return agentCfg.Name
	}
	switch agentType {
	case config.AgentExplorer:
		return "Explorer Agent"
	case config.AgentWorkhorse:
		return "Workhorse Agent"
	default:
		return agentType
	}
}

func NewAgentTool(
	Sessions session.Service,
	Messages message.Service,
	LspClients map[string]*lsp.Client,
	Permissions permission.Service,
) tools.BaseTool {
	return &agentTool{
		sessions:    Sessions,
		messages:    Messages,
		lspClients:  LspClients,
		permissions: Permissions,
	}
}

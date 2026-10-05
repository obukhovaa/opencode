package tools

import (
	"context"
	"encoding/json"
	"fmt"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/clitool"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/permission"
)

// CLIToolSource names the scratch-file prefix for spilled CLI tool output.
const CLIToolSource = "clitool"

// CLIToolPermissionParams is what a permission prompt for a CLI tool shows:
// the exact process that would run.
type CLIToolPermissionParams struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Workdir string   `json:"workdir"`
}

// CLIToolResponseMetadata rides on every CLI tool response (TUI cards,
// bridge, telemetry).
type CLIToolResponseMetadata struct {
	Tool         string   `json:"tool"`
	Command      string   `json:"command,omitempty"`
	Args         []string `json:"args"`
	ExitCode     int      `json:"exit_code"`
	DurationMs   int64    `json:"duration_ms"`
	TimedOut     bool     `json:"timed_out,omitempty"`
	TempFilePath string   `json:"temp_file_path,omitempty"`
}

// cliTool adapts a workspace manifest to BaseTool. Everything that decides
// what runs (input validation, template rendering, the argument policy, the
// executor) lives in internal/clitool so the MCP bridge shares it; this type
// adds the agent-side permission step and the output spill.
type cliTool struct {
	m           *clitool.Manifest
	info        ToolInfo
	permissions permission.Service
	registry    agentregistry.Registry
}

// NewCLITool wraps a manifest. info is computed once: Info() is called on
// every request serialization and the description embeds the help capture.
func NewCLITool(m *clitool.Manifest, permissions permission.Service, registry agentregistry.Registry) BaseTool {
	props, required := m.InputSchema()
	return &cliTool{
		m: m,
		info: ToolInfo{
			Name:        m.Name,
			Description: m.FullDescription(),
			Parameters:  props,
			Required:    required,
		},
		permissions: permissions,
		registry:    registry,
	}
}

func (t *cliTool) Info() ToolInfo { return t.info }

func (t *cliTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var input map[string]any
	if err := json.Unmarshal([]byte(call.Input), &input); err != nil {
		return NewTextErrorResponse("invalid parameters: expected a JSON object"), nil
	}
	inv, err := t.m.Prepare(input)
	if err != nil {
		// Policy and input errors are the model's to fix; they must not end
		// the run the way a permission denial does.
		return NewTextErrorResponse(err.Error()), nil
	}

	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return NewEmptyResponse(), fmt.Errorf("session ID and message ID are required to run a CLI tool")
	}

	joined := clitool.JoinArgs(inv.Args)
	switch t.evaluatePermission(ctx, joined) {
	case permission.ActionAllow:
	case permission.ActionDeny:
		return NewEmptyResponse(), permission.ErrorPermissionDenied
	default:
		workdir := t.m.ResolvedCwd
		granted := t.permissions.Request(ctx, permission.CreatePermissionRequest{
			SessionID:   sessionID,
			Path:        workdir,
			ToolName:    t.m.Name,
			Action:      "execute",
			Description: fmt.Sprintf("Run %s %s", t.m.Command, joined),
			Params: CLIToolPermissionParams{
				Command: t.m.Command,
				Args:    append(append([]string{}, t.m.PrefixArgs...), inv.Args...),
				Workdir: workdir,
			},
		})
		if !granted {
			return NewEmptyResponse(), permission.ErrorPermissionDenied
		}
	}

	res := t.m.Exec(ctx, inv)
	text, isError := clitool.FormatResult(res)
	preview, filePath := PersistLargeOutput(text, t.m.Name, CLIToolSource, t.m.MaxOutput())
	if filePath != "" {
		logging.Info("CLI tool output capped",
			"tool", t.m.Name, "totalBytes", len(text), "maxOutputBytes", t.m.MaxOutput(), "file", filePath)
	}
	meta := CLIToolResponseMetadata{
		Tool:         t.m.Name,
		Command:      res.Command,
		Args:         res.Args,
		ExitCode:     res.ExitCode,
		DurationMs:   res.Duration.Milliseconds(),
		TimedOut:     res.TimedOut,
		TempFilePath: filePath,
	}
	if isError {
		return WithResponseMetadata(NewTextErrorResponse(preview), meta), nil
	}
	return WithResponseMetadata(NewTextResponse(preview), meta), nil
}

// evaluatePermission layers the manifest default under the agent's and the
// global rules for this tool name (see permission.EvaluateToolPermissionWithDefault).
func (t *cliTool) evaluatePermission(ctx context.Context, input string) permission.Action {
	var agentPerms, globalPerms map[string]any
	if t.registry != nil {
		agentID := string(GetAgentID(ctx))
		if info, ok := t.registry.Get(agentID); ok {
			if !info.ToolEnabled(t.m.Name) {
				return permission.ActionDeny
			}
			agentPerms = info.Permission
		}
		globalPerms = t.registry.GlobalPermissions()
	}
	return permission.EvaluateToolPermissionWithDefault(t.m.Name, input, agentPerms, globalPerms, t.m.PermissionDefault())
}

// AllowParallelism: each call is an independent process; the manifest's
// policy, not the scheduler, decides what may run.
func (t *cliTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool { return true }

// IsBaseline is false: CLI tools sit in the name-sorted external tail with
// MCP tools so the builtin cache prefix is untouched.
func (t *cliTool) IsBaseline() bool { return false }

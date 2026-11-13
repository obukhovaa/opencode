package app

import (
	"context"
	"fmt"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/llm/tools/shell"
	"github.com/opencode-ai/opencode/internal/permission"
)

// Named templates are operator-authored, but invoking them through a model's
// flow tool must not grant shell access absent from the initiating agent.
func (app *App) runManagedPromptShell(ctx context.Context, conversation, primary, kind, command string) (string, error) {
	if kind == "generated" {
		return "", fmt.Errorf("%w: generated prompts must use the bash tool instead of shell markup", ErrFlowPermission)
	}
	if app.Registry == nil || !app.Registry.IsToolEnabled(primary, tools.BashToolName) {
		return "", fmt.Errorf("%w: initiating agent has bash disabled", ErrFlowPermission)
	}
	switch app.Registry.EvaluatePermission(primary, "bash", command) {
	case permission.ActionDeny:
		return "", fmt.Errorf("%w: named flow shell command denied", ErrFlowPermission)
	case permission.ActionAsk:
		if app.Permissions == nil || !app.Permissions.Request(ctx, permission.CreatePermissionRequest{SessionID: conversation, ToolName: tools.BashToolName, Action: command, Description: "Run shell markup from the selected YAML flow", Params: map[string]any{"command": command}}) {
			return "", ErrFlowPermission
		}
	case permission.ActionAllow:
	default:
		return "", ErrFlowPermission
	}
	if err := tools.CheckFlowOwnership(ctx); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return shell.ExecuteMarkup(ctx, command, config.WorkingDirectory()), nil
}

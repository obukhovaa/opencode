package tui

import (
	"context"
	"fmt"
	"github.com/opencode-ai/opencode/internal/question"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/page"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

// Capture the App and session value, never the value-receiver model pointer.
func (a *appModel) startDynamicFlow(prompt string) tea.Cmd {
	if strings.TrimSpace(prompt) == "" {
		return a.moveToPage(page.FlowsPage)
	}
	application, root, agentName := a.app, a.selectedSession, a.app.ActiveAgentName()
	return func() tea.Msg {
		ctx := context.Background()
		if root.ID == "" {
			var err error
			root, err = application.Sessions.Create(ctx, "Dynamic flow")
			if err != nil {
				return util.InfoMsg{Type: util.InfoTypeError, Msg: err.Error()}
			}
			if err = application.Sessions.SetAgentName(ctx, root.ID, string(agentName)); err != nil {
				return util.InfoMsg{Type: util.InfoTypeError, Msg: err.Error()}
			}
			root.AgentName = string(agentName)
		}
		if err := ensureFlowPrerequisite(ctx, application.Questions, root.ID); err != nil {
			return util.InfoMsg{Type: util.InfoTypeError, Msg: err.Error()}
		}
		if err := application.StartDynamicFlow(ctx, root.ID, prompt); err != nil {
			return util.InfoMsg{Type: util.InfoTypeError, Msg: err.Error()}
		}
		return chat.SessionSelectedMsg(root)
	}
}

// Installation is a user choice, never an implicit side effect of /flow.
func ensureFlowPrerequisite(ctx context.Context, questions question.Service, root string) error {
	if _, err := exec.LookPath("openspec"); err == nil {
		return nil
	}
	if questions == nil {
		return fmt.Errorf("install OpenSpec with npm install -g @fission-ai/openspec, then retry /flow")
	}
	answers, err := questions.Ask(ctx, root, []question.Prompt{{Question: "Dynamic flows require OpenSpec. Install it now with npm?", Options: []question.Option{{Label: "Install OpenSpec", Description: "Run npm install -g @fission-ai/openspec"}, {Label: "Cancel", Description: "Leave the machine unchanged"}}}})
	if err != nil {
		return err
	}
	if len(answers) == 0 || len(answers[0]) == 0 || answers[0][0] != "Install OpenSpec" {
		return fmt.Errorf("OpenSpec installation canceled")
	}
	installCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(installCtx, "npm", "install", "-g", "@fission-ai/openspec").CombinedOutput()
	if err != nil {
		return fmt.Errorf("OpenSpec installation failed: %w: %.2000s", err, output)
	}
	if _, err = exec.LookPath("openspec"); err != nil {
		return fmt.Errorf("OpenSpec installed but is not on PATH; update PATH and retry /flow")
	}
	return nil
}

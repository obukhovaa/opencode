package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/format"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/skill"
	"github.com/opencode-ai/opencode/internal/slashcmd"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
)

var namedArgPattern = regexp.MustCompile(`\$([A-Z][A-Z0-9_]*)`)

func runNonInteractive(ctx context.Context, a *app.App, prompt string, outputFormat format.OutputFormat, quiet bool) error {
	logging.Info("Running in non-interactive mode")

	// Resolve slash commands before sending to agent
	prompt, err := resolveSlashPrompt(prompt)
	if err != nil {
		return err
	}

	var spinner *format.Spinner
	if !quiet {
		spinner = format.NewSpinner("Thinking...")
		spinner.Start()
		defer spinner.Stop()
	}

	const maxPromptLengthForTitle = 100
	titlePrefix := "Non-interactive: "
	var titleSuffix string

	if len(prompt) > maxPromptLengthForTitle {
		titleSuffix = prompt[:maxPromptLengthForTitle] + "..."
	} else {
		titleSuffix = prompt
	}
	title := titlePrefix + titleSuffix

	var sess session.Session
	if a.InitialSession != nil {
		sess = *a.InitialSession
		logging.Info("Resuming existing session for non-interactive run", "session_id", sess.ID)
	} else if a.InitialSessionID != "" {
		var createErr error
		sess, createErr = a.Sessions.CreateWithID(ctx, a.InitialSessionID, title)
		if createErr != nil {
			return fmt.Errorf("failed to create session for non-interactive mode: %w", createErr)
		}
		logging.Info("Created session with provided ID for non-interactive run", "session_id", sess.ID)
	} else {
		var createErr error
		sess, createErr = a.Sessions.Create(ctx, title)
		if createErr != nil {
			return fmt.Errorf("failed to create session for non-interactive mode: %w", createErr)
		}
		logging.Info("Created session for non-interactive run", "session_id", sess.ID)
	}

	a.Permissions.AutoApproveSession(sess.ID)

	done, err := a.ActiveAgent().Run(ctx, sess.ID, prompt)
	if err != nil {
		return fmt.Errorf("failed to start agent processing stream for session %s: %w", sess.ID, err)
	}

	result := <-done
	if result.Error != nil {
		if errors.Is(result.Error, context.Canceled) || errors.Is(result.Error, agent.ErrRequestCancelled) {
			logging.Warn("Agent processing cancelled", "session_id", sess.ID)
			return nil
		}
		return fmt.Errorf("agent processing failed for session %s: %w", sess.ID, result.Error)
	}

	if !quiet && spinner != nil {
		spinner.Stop()
	}

	content := "No content available"

	if outputFormat == format.JSONSchema {
		if result.StructOutput != nil {
			content = result.StructOutput.Content
		} else {
			logging.Error("Failed to get structured output response for a provided schema", "error", content)
			content = `{"error": "no structured output result foind"}`
		}
	} else if result.Message.Content().String() != "" {
		content = result.Message.Content().String()
	}

	fmt.Println(format.FormatOutput(content, outputFormat))

	logging.Info("Non-interactive run completed", "session_id", sess.ID)
	return nil
}

func runFlowNonInteractive(ctx context.Context, a *app.App, flowID, prompt, sessionID string, fresh bool, argPairs []string, argsFile string, quiet bool) error {
	var spinner *format.Spinner
	if !quiet {
		title := fmt.Sprintf("Running %s flow...", flowID)
		if fresh {
			title = fmt.Sprintf("Running %s flow from scratch...", flowID)
		}
		spinner = format.NewSpinner(title)
		spinner.Start()
		defer spinner.Stop()
	}

	args := map[string]any{}
	if prompt != "" {
		args["prompt"] = prompt
	}

	for _, pair := range argPairs {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid --arg format %q, expected key=value", pair)
		}
		args[parts[0]] = parts[1]
	}

	if argsFile != "" {
		data, err := os.ReadFile(argsFile)
		if err != nil {
			return fmt.Errorf("reading args file: %w", err)
		}
		var fileArgs map[string]any
		if err := json.Unmarshal(data, &fileArgs); err != nil {
			return fmt.Errorf("parsing args file: %w", err)
		}
		maps.Copy(args, fileArgs)
	}

	agentEvents, flowStates, err := a.Flows.Run(ctx, sessionID, flowID, args, fresh)
	if err != nil {
		return fmt.Errorf("flow execution failed: %w", err)
	}

	startedAt := time.Now()
	type stepResult struct {
		StepID         string  `json:"step_id"`
		SessionID      string  `json:"session_id"`
		Status         string  `json:"status"`
		Output         any     `json:"output,omitempty"`
		IsStructOutput bool    `json:"is_struct_output,omitempty"`
		FinishedAt     int64   `json:"finished_at,omitempty"`
		ContextSize    int64   `json:"context_size,omitempty"`
		Cost           float64 `json:"cost,omitempty"`
	}

	var orderedSteps []stepResult
	stepIndex := map[string]int{}
	var rootSessionID string

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range agentEvents {
		}
	}()

	for state := range flowStates {
		if rootSessionID == "" {
			rootSessionID = state.RootSessionID
		}
		var output any
		if state.IsStructOutput && state.Output != "" {
			var parsed map[string]any
			if jsonErr := json.Unmarshal([]byte(state.Output), &parsed); jsonErr == nil {
				output = parsed
			} else {
				output = state.Output
			}
		} else if state.Output != "" {
			output = state.Output
		}
		sr := stepResult{
			StepID:         state.StepID,
			SessionID:      state.SessionID,
			Status:         string(state.Status),
			Output:         output,
			IsStructOutput: state.IsStructOutput,
			FinishedAt:     state.UpdatedAt,
		}
		if idx, exists := stepIndex[state.StepID]; exists {
			orderedSteps[idx] = sr
		} else {
			stepIndex[state.StepID] = len(orderedSteps)
			orderedSteps = append(orderedSteps, sr)
		}
	}
	wg.Wait()
	finishedAt := time.Now()

	if spinner != nil {
		spinner.Stop()
	}

	var totalCost float64
	if rootSessionID != "" {
		stepsBySessionID := make(map[string]*stepResult, len(orderedSteps))
		for i := range orderedSteps {
			stepsBySessionID[orderedSteps[i].SessionID] = &orderedSteps[i]
		}
		children, listErr := a.Sessions.ListChildren(ctx, rootSessionID)
		if listErr != nil {
			logging.Warn("Failed to list child sessions for metrics", "root_session_id", rootSessionID, "error", listErr)
		} else {
			for _, sess := range children {
				if step, ok := stepsBySessionID[sess.ID]; !ok {
					continue
				} else {
					step.ContextSize = sess.PromptTokens + sess.CompletionTokens
					step.Cost = sess.Cost
					totalCost += sess.Cost
				}
			}
		}
	}

	result := map[string]any{
		"flow_id": flowID,
		"steps":   orderedSteps,
		"metrics": map[string]any{
			"cost":  totalCost,
			"gauge": finishedAt.Sub(startedAt).Milliseconds(),
		},
	}

	output, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling result: %w", err)
	}

	fmt.Println(string(output))
	return nil
}

func resolveSlashPrompt(prompt string) (string, error) {
	parsed := slashcmd.Parse(prompt)
	if parsed == nil {
		return prompt, nil
	}

	commands := buildCLICommands()
	skills := skill.All()

	action, err := slashcmd.Resolve(parsed, commands, skills, false)
	if err != nil {
		return "", err
	}

	switch action.Type {
	case slashcmd.ActionCommand:
		content := action.Command.Content
		if content == "" {
			return prompt, nil
		}
		content = slashcmd.SubstituteArgs(content, action.Args)
		// Substitute any remaining named placeholders with empty string
		content = namedArgPattern.ReplaceAllString(content, "")
		// Expand !`cmd` shell markup
		content = format.ExpandShellMarkup(context.Background(), content, config.WorkingDirectory())
		return content, nil

	case slashcmd.ActionSkill:
		return slashcmd.BuildPrompt(action), nil

	default:
		return prompt, nil
	}
}

func buildCLICommands() []dialog.Command {
	commands := []dialog.Command{
		{ID: "commit", Title: "Commit and Push", Content: readEmbeddedCommand("commands/commit.md")},
		{ID: "init", Title: "Initialize Project", Content: readEmbeddedCommand("commands/init.md")},
		{ID: "review", Title: "Review Code", Content: readEmbeddedCommand("commands/review.md")},
		{ID: "compact", Title: "Compact Session"},
		{ID: "agents", Title: "List Agents"},
	}

	customCommands, err := dialog.LoadCustomCommands()
	if err != nil {
		logging.Warn("Failed to load custom commands", "error", err)
	} else {
		commands = append(commands, customCommands...)
	}

	return commands
}

func readEmbeddedCommand(path string) string {
	data, err := dialog.CommandPrompts.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

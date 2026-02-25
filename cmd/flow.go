package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
	"sync"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/format"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/session"
)

func runNonInteractive(ctx context.Context, a *app.App, prompt string, outputFormat format.OutputFormat, quiet bool) error {
	logging.Info("Running in non-interactive mode")

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
		spinner = format.NewSpinner("Running flow...")
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

	type stepResult struct {
		StepID         string `json:"step_id"`
		SessionID      string `json:"session_id"`
		Status         string `json:"status"`
		Output         string `json:"output,omitempty"`
		IsStructOutput bool   `json:"is_struct_output,omitempty"`
		FinishedAt     int64  `json:"finished_at,omitempty"`
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
		sr := stepResult{
			StepID:         state.StepID,
			SessionID:      state.SessionID,
			Status:         string(state.Status),
			Output:         state.Output,
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

	if spinner != nil {
		spinner.Stop()
	}

	var totalInputTokens int64
	var totalOutputTokens int64
	var totalCost float64
	if rootSessionID != "" {
		stepSessionIDs := make(map[string]bool, len(orderedSteps))
		for _, sr := range orderedSteps {
			stepSessionIDs[sr.SessionID] = true
		}
		children, listErr := a.Sessions.ListChildren(ctx, rootSessionID)
		if listErr != nil {
			logging.Warn("Failed to list child sessions for metrics", "root_session_id", rootSessionID, "error", listErr)
		} else {
			for _, sess := range children {
				if !stepSessionIDs[sess.ID] {
					continue
				}
				totalInputTokens += sess.PromptTokens
				totalOutputTokens += sess.CompletionTokens
				totalCost += sess.Cost
			}
		}
	}

	result := map[string]any{
		"flow_id": flowID,
		"steps":   orderedSteps,
		"metrics": map[string]any{
			"input_tokens":  totalInputTokens,
			"output_tokens": totalOutputTokens,
			"cost":          totalCost,
		},
	}

	output, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling result: %w", err)
	}

	fmt.Println(string(output))
	return nil
}

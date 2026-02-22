package agent

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/prompt"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
)

// Common errors
var (
	ErrRequestCancelled = errors.New("request cancelled by user")
	ErrSessionBusy      = errors.New("session is currently processing another request")

	//go:embed prompts/*.md
	AgentPrompts embed.FS
)

type AgentEventType string

const (
	AgentEventTypeError     AgentEventType = "error"
	AgentEventTypeResponse  AgentEventType = "response"
	AgentEventTypeSummarize AgentEventType = "summarize"
)

const (
	AutoCompactionThreshold = 0.95
)

type AgentEvent struct {
	Type    AgentEventType
	Message message.Message
	Error   error

	// When has structured output
	StructOutput *message.ToolResult

	// When summarizing
	SessionID string
	Progress  string
	Done      bool

	// FlowStepID is set when event originates from a Flow step
	FlowStepID string
}

type Service interface {
	pubsub.Suscriber[AgentEvent]
	AgentID() config.AgentName
	Model() models.Model
	Run(ctx context.Context, sessionID string, content string, attachments ...message.Attachment) (<-chan AgentEvent, error)
	Cancel(sessionID string)
	IsSessionBusy(sessionID string) bool
	IsBusy() bool
	Update(agentName config.AgentName, modelID models.ModelID) (models.Model, error)
	Summarize(ctx context.Context, sessionID string) error
}

type agent struct {
	*pubsub.Broker[AgentEvent]
	sessions session.Service
	messages message.Service

	agentID   config.AgentName
	toolsCh   <-chan tools.BaseTool
	toolsOnce sync.Once
	tools     []tools.BaseTool
	provider  provider.Provider

	titleProvider     provider.Provider
	summarizeProvider provider.Provider

	activeRequests sync.Map
}

func newAgent(
	ctx context.Context,
	agentInfo *agentregistry.AgentInfo,
	sessions session.Service,
	messages message.Service,
	permissions permission.Service,
	historyService history.Service,
	lspClients map[string]*lsp.Client,
	reg agentregistry.Registry,
	mcpReg MCPRegistry,
	factory AgentFactory,
) (Service, error) {
	// BUG: there could be a race with lspClients map, since it may have stale value, consider to improve, make it lazy and block on first usage attempt
	agentTools := NewToolSet(ctx, agentInfo, reg, permissions, historyService, lspClients, sessions, messages, mcpReg, factory)

	agentProvider, err := createAgentProvider(agentInfo.ID)
	if err != nil {
		return nil, err
	}

	var titleProvider, summarizeProvider provider.Provider
	if agentInfo.Mode == config.AgentModeAgent {
		summarizeProvider, err = createAgentProvider(config.AgentSummarizer)
		if err != nil {
			return nil, err
		}
		titleProvider, err = createAgentProvider(config.AgentDescriptor)
		if err != nil {
			return nil, err
		}
	}

	agent := &agent{
		Broker:            pubsub.NewBroker[AgentEvent](),
		agentID:           agentInfo.ID,
		provider:          agentProvider,
		messages:          messages,
		sessions:          sessions,
		toolsCh:           agentTools,
		titleProvider:     titleProvider,
		summarizeProvider: summarizeProvider,
		activeRequests:    sync.Map{},
	}

	return agent, nil
}

func (a *agent) AgentID() config.AgentName {
	return a.agentID
}

func (a *agent) Model() models.Model {
	return a.provider.Model()
}

func (a *agent) Cancel(sessionID string) {
	// Cancel regular requests
	if cancelFunc, exists := a.activeRequests.LoadAndDelete(sessionID); exists {
		if cancel, ok := cancelFunc.(context.CancelFunc); ok {
			logging.InfoPersist(fmt.Sprintf("Request cancellation initiated for session: %s", sessionID))
			cancel()
		}
	}

	// Also check for summarize requests
	if cancelFunc, exists := a.activeRequests.LoadAndDelete(sessionID + "-summarize"); exists {
		if cancel, ok := cancelFunc.(context.CancelFunc); ok {
			logging.InfoPersist(fmt.Sprintf("Summarize cancellation initiated for session: %s", sessionID))
			cancel()
		}
	}
}

func (a *agent) IsBusy() bool {
	busy := false
	a.activeRequests.Range(func(key, value any) bool {
		if cancelFunc, ok := value.(context.CancelFunc); ok {
			if cancelFunc != nil {
				busy = true
				return false // Stop iterating
			}
		}
		return true // Continue iterating
	})
	return busy
}

func (a *agent) IsSessionBusy(sessionID string) bool {
	_, busy := a.activeRequests.Load(sessionID)
	return busy
}

func (a *agent) generateTitle(ctx context.Context, sessionID string, content string) error {
	if content == "" {
		return nil
	}
	if a.titleProvider == nil {
		return nil
	}
	session, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, sessionID)
	parts := []message.ContentPart{message.TextContent{Text: content}}
	response, err := a.titleProvider.SendMessages(
		ctx,
		[]message.Message{
			{
				Role:  message.User,
				Parts: parts,
			},
		},
		make([]tools.BaseTool, 0),
	)
	if err != nil {
		return err
	}

	title := strings.TrimSpace(strings.ReplaceAll(response.Content, "\n", " "))
	if title == "" {
		return nil
	}

	session.Title = title
	_, err = a.sessions.Save(ctx, session)
	return err
}

func (a *agent) err(err error) AgentEvent {
	return AgentEvent{
		Type:  AgentEventTypeError,
		Error: err,
	}
}

func (a *agent) Run(ctx context.Context, sessionID string, content string, attachments ...message.Attachment) (<-chan AgentEvent, error) {
	if !a.provider.Model().SupportsAttachments && attachments != nil {
		attachments = nil
	}
	events := make(chan AgentEvent)
	if a.IsSessionBusy(sessionID) {
		return nil, ErrSessionBusy
	}

	genCtx, cancel := context.WithCancel(ctx)

	a.activeRequests.Store(sessionID, cancel)
	go func() {
		logging.Info("Agent started", "sessionID", sessionID, "agent", a.AgentID())
		now := time.Now()
		defer logging.RecoverPanic("agent.Run", func() {
			events <- a.err(fmt.Errorf("panic while running the agent"))
		})
		var attachmentParts []message.ContentPart
		for _, attachment := range attachments {
			attachmentParts = append(attachmentParts, message.BinaryContent{Path: attachment.FilePath, MIMEType: attachment.MimeType, Data: attachment.Content})
		}

		result := a.processGeneration(genCtx, sessionID, content, attachmentParts)
		gauge := time.Since(now).Milliseconds()
		if result.Error != nil {
			if errors.Is(result.Error, ErrRequestCancelled) || errors.Is(result.Error, context.Canceled) {
				logging.Warn("Agent processing cancelled", "sessionID", sessionID, "agent", a.AgentID(), "gauge", gauge)
			} else {
				logging.Error("Agent processing failed", "sessionID", sessionID, "agent", a.AgentID(), "gauge", gauge)
				logging.ErrorPersist(result.Error.Error())
			}
		} else {
			logging.Info("Agent completed", "sessionID", sessionID, "agent", a.AgentID(), "gauge", gauge)
		}

		a.activeRequests.Delete(sessionID)
		cancel()
		a.Publish(pubsub.CreatedEvent, result)
		events <- result
		close(events)
	}()
	return events, nil
}

func (a *agent) processGeneration(ctx context.Context, sessionID, content string, attachmentParts []message.ContentPart) AgentEvent {
	cfg := config.Get()
	// List existing messages; if none, start title generation asynchronously.
	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return a.err(fmt.Errorf("failed to list messages: %w", err))
	}
	if len(msgs) == 0 {
		go func() {
			defer logging.RecoverPanic("agent.Run", func() {
				logging.ErrorPersist("panic while generating title")
			})
			titleErr := a.generateTitle(context.Background(), sessionID, content)
			if titleErr != nil {
				logging.ErrorPersist(fmt.Sprintf("failed to generate title: %v", titleErr))
			}
		}()
	}
	session, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return a.err(fmt.Errorf("failed to get session: %w", err))
	}
	if session.SummaryMessageID != "" {
		msgs = a.filterMessagesFromSummary(msgs, session.SummaryMessageID)
	}
	if session.ParentSessionID != "" {
		ctx = context.WithValue(ctx, tools.IsTaskAgentContextKey, true)
	}
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, sessionID)
	ctx = context.WithValue(ctx, tools.AgentIDContextKey, a.AgentID())

	userMsg, err := a.createUserMessage(ctx, sessionID, content, attachmentParts)
	if err != nil {
		return a.err(fmt.Errorf("failed to create user message: %w", err))
	}
	// Append the new user message to the conversation history.
	msgHistory := append(msgs, userMsg)
	var agentMessage message.Message
	var toolResults *message.Message
	var structOutput *message.ToolResult
	structOutputIsErr := true
	cycles := 0
	preserveTail := false

	// Susped to get lazy tools
	toolSet := a.resolveTools()

	for {
		cycles += 1
		// Check for cancellation before each iteration
		select {
		case <-ctx.Done():
			return a.err(ctx.Err())
		default:
			// Continue processing
		}

		etaTokens, shouldTriggerAutoCompaction := a.provider.CountTokens(ctx, AutoCompactionThreshold, msgHistory, toolSet)
		// Check if auto-compaction should be triggered before each model call
		// This is crucial for long tool use loops that can exceed context limits
		// NOTE: since tool may provide output exceeding context limit when combined with existing history,
		// we have to do summary, which would "lossy compress" it, providing less context to the following LLM call,
		// but alternative is to fail with context limit, so we do it anyway.
		if cfg.AutoCompact && cycles != 1 && shouldTriggerAutoCompaction {
			logging.Info(
				"Auto-compaction triggered during tool use loop",
				"session_id", sessionID,
				"history_length", len(msgHistory),
				"token_count", etaTokens,
				"cycle", cycles,
			)

			// Perform synchronous compaction to shrink context
			if errSync := a.performSynchronousCompaction(ctx, sessionID); errSync != nil {
				logging.Warn("Failed to perform auto-compaction during tool use", "error", errSync)
				// Continue anyway - better to risk context overflow than stop completely
			} else {
				// After successful compaction, reload messages and rebuild msgHistory
				msgs, errMsg := a.messages.List(ctx, sessionID)
				if err != nil {
					return a.err(fmt.Errorf("failed to reload messages after compaction: %w", errMsg))
				}

				session, errMsg := a.sessions.Get(ctx, sessionID)
				if errMsg != nil {
					return a.err(fmt.Errorf("failed to get session after compaction: %w", errMsg))
				}
				msgs = a.filterMessagesFromSummary(msgs, session.SummaryMessageID)
				// Preserve original problem and result from the last tool iteration to ensure no dead-loop
				if preserveTail {
					preserveTail = false
					msgHistory = append(msgs, agentMessage, *toolResults)
				} else {
					msgHistory = append(msgs, userMsg)
				}

				etaTokens, shouldTriggerAutoCompaction = a.provider.CountTokens(ctx, AutoCompactionThreshold, msgHistory, toolSet)
				if shouldTriggerAutoCompaction {
					logging.Warn(
						"Context compacted, but still exceed context threshold",
						"session_id", sessionID,
						"history_length", len(msgHistory),
						"token_count", etaTokens,
						"cycle", cycles,
					)
				} else {
					logging.Info(
						"Context compacted, continuing with reduced history",
						"session_id", sessionID,
						"history_length", len(msgHistory),
						"token_count", etaTokens,
						"cycle", cycles,
					)
				}
			}
		}

		// Ensure we don't run into API limitation (max_token to be generated + current tokens count)
		a.provider.AdjustMaxTokens(etaTokens)

		agentMessage, toolResults, err = a.streamAndHandleEvents(ctx, sessionID, msgHistory, toolSet)
		if err != nil {
			a.createErrorToolResults(agentMessage)
			if errors.Is(err, context.Canceled) {
				a.finishMessage(ctx, &agentMessage, message.FinishReasonCanceled)
				return a.err(ErrRequestCancelled)
			}
			a.finishMessage(ctx, &agentMessage, message.FinishReasonError)
			return a.err(fmt.Errorf("failed to process events: %w", err))
		}
		if cfg.Debug {
			seqID := (len(msgHistory) + 1) / 2
			toolResultFilepath := logging.WriteToolResultsJson(sessionID, seqID, toolResults)
			logging.Info("Provider stream completed", "reason", agentMessage.FinishReason(), "filepath", toolResultFilepath, "cycle", cycles)
		} else {
			logging.Info("Provider stream completed", "reason", agentMessage.FinishReason(), "cycle", cycles)
		}
		if agentMessage.FinishReason() == message.FinishReasonToolUse {
			if toolResults == nil {
				// Tool results are nil (tool execution failed or returned empty)
				// Create an empty tool results message to allow the LLM to provide a final response
				logging.Warn("Tool results are nil, creating empty tool results message to allow final response", "session_id", sessionID)
				emptyToolMsg, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
					Role:  message.Tool,
					Parts: []message.ContentPart{message.TextContent{Text: "Tool execution completed with no results."}},
				})
				if err != nil {
					logging.Warn("Failed to create empty tool results message", "error", err)
					// If we can't create the message, just return what we have
					return AgentEvent{
						Type:    AgentEventTypeResponse,
						Message: agentMessage,
						Done:    true,
					}
				}
				toolResults = &emptyToolMsg
			} else {
				if structOutput == nil || structOutputIsErr {
					if s, ok := toolResults.StructOutput(); ok || structOutput == nil {
						structOutput = s
					}
				}
			}

			msgHistory = append(msgHistory, agentMessage, *toolResults)
			preserveTail = true
			continue
		}
		return AgentEvent{
			Type:         AgentEventTypeResponse,
			Message:      agentMessage,
			StructOutput: structOutput,
			Done:         true,
		}
	}
}

func (a *agent) createUserMessage(ctx context.Context, sessionID, content string, attachmentParts []message.ContentPart) (message.Message, error) {
	parts := []message.ContentPart{message.TextContent{Text: content}}
	parts = append(parts, attachmentParts...)
	return a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:  message.User,
		Parts: parts,
	})
}

func (a *agent) streamAndHandleEvents(ctx context.Context, sessionID string, msgHistory []message.Message, toolSet []tools.BaseTool) (message.Message, *message.Message, error) {
	eventChan := a.provider.StreamResponse(ctx, msgHistory, toolSet)

	assistantMsg, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:  message.Assistant,
		Parts: []message.ContentPart{},
		Model: a.provider.Model().ID,
	})
	if err != nil {
		return assistantMsg, nil, fmt.Errorf("failed to create assistant message: %w", err)
	}

	ctx = context.WithValue(ctx, tools.MessageIDContextKey, assistantMsg.ID)

	// Process provider response first
	for event := range eventChan {
		if processErr := a.processEvent(ctx, sessionID, &assistantMsg, event); processErr != nil {
			return assistantMsg, nil, processErr
		}
		if ctx.Err() != nil {
			return assistantMsg, nil, ctx.Err()
		}
	}

	// Process tool calls
	toolResults := make([]message.ToolResult, len(assistantMsg.ToolCalls()))
	toolCalls := assistantMsg.ToolCalls()
	for i, toolCall := range toolCalls {
		select {
		case <-ctx.Done():
			a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonCanceled)
			// Make all future tool calls cancelled
			for j := i; j < len(toolCalls); j++ {
				toolResults[j] = message.ToolResult{
					ToolCallID: toolCalls[j].ID,
					Name:       toolCalls[j].Name,
					Content:    "Tool execution canceled by user",
					IsError:    true,
				}
			}
			goto out
		default:
			// Continue processing
			var tool tools.BaseTool
			for _, availableTool := range toolSet {
				if availableTool.Info().Name == toolCall.Name {
					tool = availableTool
					break
				}
			}
			// Tool not found
			if tool == nil {
				toolResults[i] = message.ToolResult{
					ToolCallID: toolCall.ID,
					Name:       toolCall.Name,
					Content:    fmt.Sprintf("Tool not found: %s", toolCall.Name),
					IsError:    true,
				}
				continue
			}

			now := time.Now()

			// TODO: add parallelism so tool calls can run concurrently (at least for Task tool)
			toolResult, toolErr := tool.Run(ctx, tools.ToolCall{
				ID:    toolCall.ID,
				Name:  toolCall.Name,
				Input: toolCall.Input,
			})
			gauge := time.Since(now).Milliseconds()
			if toolErr != nil {
				if errors.Is(toolErr, permission.ErrorPermissionDenied) {
					logging.Warn("Tool call denied", "tool", toolCall.Name,
						"ID", toolCall.ID,
						"input", toolCall.Input,
						"gauge", gauge,
					)
					toolResults[i] = message.ToolResult{
						ToolCallID: toolCall.ID,
						Name:       toolCall.Name,
						Content:    "Permission denied",
						IsError:    true,
					}
					for j := i + 1; j < len(toolCalls); j++ {
						toolResults[j] = message.ToolResult{
							ToolCallID: toolCalls[j].ID,
							Name:       toolCalls[j].Name,
							Content:    "Tool execution canceled by user",
							IsError:    true,
						}
					}
					a.finishMessage(ctx, &assistantMsg, message.FinishReasonPermissionDenied)
					break
				} else {
					logging.Error("Tool call failed", "tool", toolCall.Name,
						"ID", toolCall.ID,
						"input", toolCall.Input,
						"error", toolErr.Error(),
						"gauge", gauge,
					)
					toolResults[i] = message.ToolResult{
						ToolCallID: toolCall.ID,
						Name:       toolCall.Name,
						Content:    fmt.Sprintf("Tool returned error: %s", toolErr.Error()),
						IsError:    true,
					}
					continue
				}
			}
			logging.Debug("Tool call completed", "tool", toolCall.Name,
				"ID", toolCall.ID,
				"input", toolCall.Input,
				"successful", !toolResult.IsError,
				"gauge", gauge,
			)
			toolResults[i] = message.ToolResult{
				Type:       message.ToolResultType(toolResult.Type),
				Name:       toolCall.Name,
				ToolCallID: toolCall.ID,
				Content:    toolResult.Content,
				Metadata:   toolResult.Metadata,
				IsError:    toolResult.IsError,
			}
		}
	}
out:
	if len(toolResults) == 0 {
		return assistantMsg, nil, nil
	}
	parts := make([]message.ContentPart, 0)
	for _, tr := range toolResults {
		parts = append(parts, tr)
	}
	msg, err := a.messages.Create(context.Background(), assistantMsg.SessionID, message.CreateMessageParams{
		Role:  message.Tool,
		Parts: parts,
	})
	if err != nil {
		return assistantMsg, nil, fmt.Errorf("failed to create cancelled tool message: %w", err)
	}

	return assistantMsg, &msg, nil
}

func (a *agent) finishMessage(ctx context.Context, msg *message.Message, finishReson message.FinishReason) {
	msg.AddFinish(finishReson)
	_ = a.messages.Update(ctx, *msg)
}

// createErrorToolResults creates a tool results message with error results for all tool calls
// in the given assistant message. This ensures that every tool_use block in the DB
// has a corresponding tool_result, preventing API errors when the session is resumed.
func (a *agent) createErrorToolResults(assistantMsg message.Message) *message.Message {
	toolCalls := assistantMsg.ToolCalls()
	if len(toolCalls) == 0 {
		return nil
	}
	parts := make([]message.ContentPart, len(toolCalls))
	for i, tc := range toolCalls {
		parts[i] = message.ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Content:    "Tool execution was interrupted",
			IsError:    true,
		}
	}
	msg, err := a.messages.Create(context.Background(), assistantMsg.SessionID, message.CreateMessageParams{
		Role:  message.Tool,
		Parts: parts,
	})
	if err != nil {
		logging.Warn("Failed to create error tool results message", "error", err)
		return nil
	}
	return &msg
}

// mergeToolCalls updates tool call Input from the accumulated response without replacing IDs.
// During streaming, tool calls are registered with IDs from ContentBlockStartEvent.
// The accumulated SDK response may carry different IDs (e.g. through LiteLLM/Vertex proxies).
// Tool results already reference the streaming IDs, so we must preserve them.
func (a *agent) mergeToolCalls(assistantMsg *message.Message, accumulated []message.ToolCall) {
	existing := assistantMsg.ToolCalls()
	if len(existing) == 0 {
		// No streaming tool calls — fall back to using the accumulated ones directly
		assistantMsg.SetToolCalls(accumulated)
		return
	}

	// Match by position: streaming events and accumulated blocks arrive in the same order
	for i, tc := range existing {
		if i < len(accumulated) {
			tc.Input = accumulated[i].Input
			tc.Finished = true
			assistantMsg.UpdateToolCall(tc)
		}
	}
}

func (a *agent) processEvent(ctx context.Context, sessionID string, assistantMsg *message.Message, event provider.ProviderEvent) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		// Continue processing.
	}

	switch event.Type {
	case provider.EventThinkingDelta:
		assistantMsg.AppendReasoningContent(event.Content)
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventContentDelta:
		assistantMsg.AppendContent(event.Content)
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventToolUseStart:
		assistantMsg.AddToolCall(*event.ToolCall)
		return a.messages.Update(ctx, *assistantMsg)
	// TODO: see how to handle this
	// case provider.EventToolUseDelta:
	// 	tm := time.Unix(assistantMsg.UpdatedAt, 0)
	// 	assistantMsg.AppendToolCallInput(event.ToolCall.ID, event.ToolCall.Input)
	// 	if time.Since(tm) > 1000*time.Millisecond {
	// 		err := a.messages.Update(ctx, *assistantMsg)
	// 		assistantMsg.UpdatedAt = time.Now().Unix()
	// 		return err
	// 	}
	case provider.EventToolUseStop:
		assistantMsg.FinishToolCall(event.ToolCall.ID)
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventError:
		if errors.Is(event.Error, context.Canceled) {
			logging.InfoPersist(fmt.Sprintf("Event processing canceled for session: %s", sessionID))
			return context.Canceled
		}
		logging.ErrorPersist(event.Error.Error())
		return event.Error
	case provider.EventComplete:
		// HACK: validate if we really need it
		// Merge tool call data from the accumulated response without replacing IDs.
		// During streaming, tool calls are added via EventToolUseStart with their IDs,
		// and tool results reference those IDs. The accumulated response may carry
		// different IDs (e.g. through proxies), so we must preserve the streaming IDs
		// and only update the Input field which is accumulated by the SDK.
		a.mergeToolCalls(assistantMsg, event.Response.ToolCalls)
		assistantMsg.AddFinish(event.Response.FinishReason)
		if err := a.messages.Update(ctx, *assistantMsg); err != nil {
			return fmt.Errorf("failed to update message: %w", err)
		}
		return a.TrackUsage(ctx, sessionID, a.provider.Model(), event.Response.Usage)
	}

	return nil
}

func (a *agent) TrackUsage(ctx context.Context, sessionID string, model models.Model, usage provider.TokenUsage) error {
	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}

	cost := model.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
		model.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
		model.CostPer1MIn/1e6*float64(usage.InputTokens) +
		model.CostPer1MOut/1e6*float64(usage.OutputTokens)

	sess.Cost += cost
	sess.CompletionTokens = usage.OutputTokens + usage.CacheReadTokens
	sess.PromptTokens = usage.InputTokens + usage.CacheCreationTokens

	logging.Info("Track usage",
		"token_out_total", sess.CompletionTokens,
		"token_in_total", sess.PromptTokens,
		"token_in", usage.InputTokens,
		"token_out", usage.OutputTokens,
		"cache_created", usage.CacheCreationTokens,
		"cache_read", usage.CacheReadTokens,
		"cost", cost,
	)

	_, err = a.sessions.Save(ctx, sess)
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

func (a *agent) Update(agentName config.AgentName, modelID models.ModelID) (models.Model, error) {
	if a.IsBusy() {
		return models.Model{}, fmt.Errorf("cannot change model while processing requests")
	}

	if err := config.UpdateAgentModel(agentName, modelID); err != nil {
		return models.Model{}, fmt.Errorf("failed to update config: %w", err)
	}

	provider, err := createAgentProvider(agentName)
	if err != nil {
		return models.Model{}, fmt.Errorf("failed to create provider for model %s: %w", modelID, err)
	}

	a.provider = provider

	return a.provider.Model(), nil
}

// shouldTriggerAutoCompaction checks if the session should trigger auto-compaction
// based on token usage approaching the context window limit
// filterMessagesFromSummary filters messages to start from the summary message if one exists.
// This reduces context size by excluding messages before the summary.
// It ensures that tool_use/tool_result pairs are not split by the filter boundary.
func (a *agent) filterMessagesFromSummary(msgs []message.Message, summaryMessageID string) []message.Message {
	if summaryMessageID == "" {
		return msgs
	}

	summaryMsgIndex := -1
	for i, msg := range msgs {
		if msg.ID == summaryMessageID {
			summaryMsgIndex = i
			break
		}
	}

	if summaryMsgIndex == -1 {
		return msgs
	}

	filteredMsgs := msgs[summaryMsgIndex:]
	// Convert the summary message role to User so it can be used in conversation
	filteredMsgs[0].Role = message.User

	// Ensure the filtered messages don't start with orphaned tool results
	// (Tool messages whose corresponding Assistant message was before the summary).
	// Skip any Tool messages that immediately follow the summary message.
	result := filteredMsgs[:1] // Always keep the summary message
	skippingOrphanedTools := true
	for _, msg := range filteredMsgs[1:] {
		if skippingOrphanedTools {
			if msg.Role == message.Tool {
				logging.Warn("Skipping orphaned tool result message after summary filter", "message_id", msg.ID)
				continue
			}
			skippingOrphanedTools = false
		}
		result = append(result, msg)
	}
	return result
}

// performSynchronousCompaction performs summarization synchronously and waits for completion
// This is used for auto-compaction in non-interactive mode to shrink context before continuing
func (a *agent) performSynchronousCompaction(ctx context.Context, sessionID string) error {
	if a.summarizeProvider == nil {
		return fmt.Errorf("summarize provider not available")
	}

	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to list messages: %w", err)
	}

	// NOTE: We don't check IsSessionBusy here because this is called from within
	logging.Info("Starting synchronous compaction", "session_id", sessionID, "message_count", len(msgs))

	if len(msgs) == 0 {
		return fmt.Errorf("no messages to summarize")
	}

	summarizeCtx := context.WithValue(ctx, tools.SessionIDContextKey, sessionID)
	summarizePrompt, err := AgentPrompts.ReadFile("prompts/compaction.md")
	if err != nil {
		return fmt.Errorf("failed to load summary prompt: %w", err)
	}

	promptMsg := message.Message{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: string(summarizePrompt)}},
	}

	msgsWithPrompt := append(msgs, promptMsg)
	response, err := a.summarizeProvider.SendMessages(
		summarizeCtx,
		msgsWithPrompt,
		make([]tools.BaseTool, 0),
	)
	if err != nil {
		return fmt.Errorf("failed to summarize: %w", err)
	}

	summary := strings.TrimSpace(response.Content)
	if summary == "" {
		return fmt.Errorf("empty summary returned")
	}

	// Get the session to update
	oldSession, err := a.sessions.Get(summarizeCtx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}

	// Create a new message with the summary
	msg, err := a.messages.Create(summarizeCtx, oldSession.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: summary}},
		Model: a.summarizeProvider.Model().ID,
	})
	if err != nil {
		return fmt.Errorf("failed to create summary message: %w", err)
	}

	// Update the session with the summary message ID
	oldSession.SummaryMessageID = msg.ID
	oldSession.CompletionTokens = response.Usage.OutputTokens
	oldSession.PromptTokens = 0
	model := a.summarizeProvider.Model()
	usage := response.Usage
	cost := model.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
		model.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
		model.CostPer1MIn/1e6*float64(usage.InputTokens) +
		model.CostPer1MOut/1e6*float64(usage.OutputTokens)
	oldSession.Cost += cost

	_, err = a.sessions.Save(summarizeCtx, oldSession)
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}

	logging.Info("Synchronous compaction completed successfully", "session_id", sessionID)
	return nil
}

func (a *agent) Summarize(ctx context.Context, sessionID string) error {
	if a.summarizeProvider == nil {
		return fmt.Errorf("summarize provider not available")
	}

	// Check if session is busy
	if a.IsSessionBusy(sessionID) {
		return ErrSessionBusy
	}

	// Create a new context with cancellation
	summarizeCtx, cancel := context.WithCancel(ctx)

	// Store the cancel function in activeRequests to allow cancellation
	a.activeRequests.Store(sessionID+"-summarize", cancel)

	go func() {
		defer a.activeRequests.Delete(sessionID + "-summarize")
		defer cancel()
		event := AgentEvent{
			Type:     AgentEventTypeSummarize,
			Progress: "Starting summarization...",
		}

		a.Publish(pubsub.CreatedEvent, event)
		// Get all messages from the session
		msgs, err := a.messages.List(summarizeCtx, sessionID)
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to list messages: %w", err),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		summarizeCtx = context.WithValue(summarizeCtx, tools.SessionIDContextKey, sessionID)

		if len(msgs) == 0 {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("no messages to summarize"),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}

		event = AgentEvent{
			Type:     AgentEventTypeSummarize,
			Progress: "Analyzing conversation...",
		}
		a.Publish(pubsub.CreatedEvent, event)

		summarizePrompt, err := AgentPrompts.ReadFile("prompts/compaction.md")
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to load summary prompt: %w", err),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}

		// Create a new message with the summarize prompt
		promptMsg := message.Message{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: string(summarizePrompt)}},
		}

		// Append the prompt to the messages
		msgsWithPrompt := append(msgs, promptMsg)

		event = AgentEvent{
			Type:     AgentEventTypeSummarize,
			Progress: "Generating summary...",
		}

		a.Publish(pubsub.CreatedEvent, event)

		// Send the messages to the summarize provider
		response, err := a.summarizeProvider.SendMessages(
			summarizeCtx,
			msgsWithPrompt,
			make([]tools.BaseTool, 0),
		)
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to summarize: %w", err),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}

		summary := strings.TrimSpace(response.Content)
		if summary == "" {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("empty summary returned"),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		event = AgentEvent{
			Type:     AgentEventTypeSummarize,
			Progress: "Creating new session...",
		}

		a.Publish(pubsub.CreatedEvent, event)
		oldSession, err := a.sessions.Get(summarizeCtx, sessionID)
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to get session: %w", err),
				Done:  true,
			}

			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		// Create a message in the new session with the summary
		msg, err := a.messages.Create(summarizeCtx, oldSession.ID, message.CreateMessageParams{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: summary},
				message.Finish{
					Reason: message.FinishReasonEndTurn,
					Time:   time.Now().Unix(),
				},
			},
			Model: a.summarizeProvider.Model().ID,
		})
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to create summary message: %w", err),
				Done:  true,
			}

			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		oldSession.SummaryMessageID = msg.ID
		oldSession.CompletionTokens = response.Usage.OutputTokens
		oldSession.PromptTokens = 0
		model := a.summarizeProvider.Model()
		usage := response.Usage
		cost := model.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
			model.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
			model.CostPer1MIn/1e6*float64(usage.InputTokens) +
			model.CostPer1MOut/1e6*float64(usage.OutputTokens)
		oldSession.Cost += cost
		_, err = a.sessions.Save(summarizeCtx, oldSession)
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to save session: %w", err),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
		}

		event = AgentEvent{
			Type:      AgentEventTypeSummarize,
			SessionID: oldSession.ID,
			Progress:  "Summary complete",
			Done:      true,
		}
		a.Publish(pubsub.CreatedEvent, event)
		// Send final success event with the new session ID
	}()

	return nil
}

func createAgentProvider(agentName config.AgentName) (agentProvider provider.Provider, err error) {
	defer func() {
		if err == nil {
			logging.Info("Agent provider created", "agent", agentName, "model", agentProvider.Model())
		}
	}()
	cfg := config.Get()
	agentConfig, ok := cfg.Agents[agentName]
	if !ok {
		// Try registry for custom (markdown-defined) agents
		reg := agentregistry.GetRegistry()
		if info, found := reg.Get(agentName); found && info.Model != "" {
			agentConfig = config.Agent{
				Model:           models.ModelID(info.Model),
				MaxTokens:       info.MaxTokens,
				ReasoningEffort: info.ReasoningEffort,
			}
		} else if found {
			// Inherit coder's model if no model specified
			coderCfg, coderOk := cfg.Agents[config.AgentCoder]
			if !coderOk {
				return nil, fmt.Errorf("agent %s has no model and coder agent not configured", agentName)
			}
			agentConfig = config.Agent{
				Model:           coderCfg.Model,
				MaxTokens:       coderCfg.MaxTokens,
				ReasoningEffort: coderCfg.ReasoningEffort,
			}
		} else {
			return nil, fmt.Errorf("agent %s not found", agentName)
		}
	}
	model, ok := models.SupportedModels[agentConfig.Model]
	if !ok {
		return nil, fmt.Errorf("model %s not supported", agentConfig.Model)
	}

	providerCfg, ok := cfg.Providers[model.Provider]
	if !ok {
		return nil, fmt.Errorf("provider %s not supported", model.Provider)
	}
	if providerCfg.Disabled {
		return nil, fmt.Errorf("provider %s is not enabled", model.Provider)
	}
	maxTokens := model.DefaultMaxTokens
	if agentConfig.MaxTokens > 0 {
		maxTokens = agentConfig.MaxTokens
	}

	opts := []provider.ProviderClientOption{
		provider.WithAPIKey(providerCfg.APIKey),
		provider.WithModel(model),
		provider.WithSystemMessage(prompt.GetAgentPrompt(agentName, model.Provider)),
		provider.WithMaxTokens(maxTokens),
	}
	if providerCfg.BaseURL != "" {
		opts = append(opts, provider.WithBaseURL(providerCfg.BaseURL))
	}
	if len(providerCfg.Headers) != 0 {
		opts = append(opts, provider.WithHeaders(providerCfg.Headers))
	}

	if model.Provider == models.ProviderOpenAI || model.Provider == models.ProviderLocal && model.CanReason {
		opts = append(
			opts,
			provider.WithOpenAIOptions(
				provider.WithReasoningEffort(agentConfig.ReasoningEffort),
			),
		)
	} else if (model.Provider == models.ProviderAnthropic || model.Provider == models.ProviderVertexAI || model.Provider == models.ProviderBedrock) && model.CanReason {
		anthropicOpts := []provider.AnthropicOption{
			provider.WithAnthropicShouldThinkFn(provider.DefaultShouldThinkFn),
		}
		if model.SupportsAdaptiveThinking {
			anthropicOpts = append(anthropicOpts, provider.WithAnthropicReasoningEffort(agentConfig.ReasoningEffort))
		}
		opts = append(
			opts,
			provider.WithAnthropicOptions(anthropicOpts...),
		)
	}
	agentProvider, err = provider.NewProvider(
		model.Provider,
		opts...,
	)
	if err != nil {
		return nil, fmt.Errorf("could not create provider: %v", err)
	}

	return agentProvider, nil
}

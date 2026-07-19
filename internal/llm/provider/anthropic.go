package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	toolsPkg "github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
)

const taskBudgetsBeta = "task-budgets-2026-03-13"

// filterBetaHeaders removes beta header values that are incompatible with the
// given model. For example, "context-1m-*" betas are stripped for models whose
// context window is below 1M tokens.
func filterBetaHeaders(value string, model models.Model) string {
	parts := strings.Split(value, ",")
	var kept []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "context-1m") && model.ContextWindow < 1_000_000 {
			continue
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, ",")
}

type anthropicOptions struct {
	useBedrock      bool
	useVertex       bool
	vertexOptions   vertexOptions
	disableCache    bool
	shouldThink     func(userMessage string) bool
	reasoningEffort string
	taskBudget      int64
}

type AnthropicOption func(*anthropicOptions)

type anthropicClient struct {
	providerOptions providerClientOptions
	options         anthropicOptions
	client          anthropic.Client
	// countTokensUnsupported latches after the endpoint answers 404/405 —
	// Anthropic-compatible endpoints (e.g. Moonshot's) may not implement
	// count_tokens, and probing it once per agent-loop iteration would add
	// a wasted HTTP round-trip; the provider layer falls back to the local
	// estimate whenever countTokens errors.
	countTokensUnsupported atomic.Bool
}

type AnthropicClient ProviderClient

func newAnthropicClient(opts providerClientOptions) AnthropicClient {
	anthropicOpts := anthropicOptions{}
	for _, o := range opts.anthropicOptions {
		o(&anthropicOpts)
	}
	resolvedBaseURL := ""

	anthropicClientOptions := []option.RequestOption{
		// Disable the SDK's built-in retry layer (default MaxRetries=2,
		// see anthropic-sdk-go/option/requestoption.go). Opencode owns
		// retry policy via shouldRetry + isTransientStreamError — the
		// SDK retrying first would stack 2 SDK attempts on top of our
		// up-to-8 attempts, producing a worst-case ~8.5 min wall-clock
		// on a single failing request (2s/4s/8s/16s/32s/64s/128s/256s
		// opencode backoff after the SDK's own internal retries). One
		// retry policy, one place to reason about it.
		option.WithMaxRetries(0),
	}
	if anthropicOpts.useBedrock {
		middleware := bedrockMiddleware()
		anthropicClientOptions = append(anthropicClientOptions, option.WithMiddleware(middleware))
		if opts.baseURL != "" {
			resolvedBaseURL = opts.baseURL
		}
	}
	if anthropicOpts.useVertex {
		middleware := vertexMiddleware(
			anthropicOpts.vertexOptions.location,
			anthropicOpts.vertexOptions.locationForCounting,
			anthropicOpts.vertexOptions.projectID,
		)
		anthropicClientOptions = append(
			anthropicClientOptions,
			option.WithMiddleware(middleware),
		)
		if opts.baseURL == "" {
			resolvedBaseURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/", anthropicOpts.vertexOptions.location)
		} else {
			resolvedBaseURL = opts.baseURL
		}
	}

	if opts.headers != nil {
		for k, v := range opts.headers {
			if strings.EqualFold(k, "anthropic-beta") {
				v = filterBetaHeaders(v, opts.model)
				if v == "" {
					continue
				}
			}
			anthropicClientOptions = append(anthropicClientOptions, option.WithHeader(k, v))
		}
	}
	if resolvedBaseURL != "" {
		anthropicClientOptions = append(anthropicClientOptions, option.WithBaseURL(resolvedBaseURL))
		if opts.apiKey != "" {
			anthropicClientOptions = append(anthropicClientOptions, option.WithAuthToken(opts.apiKey))
		}
	} else if opts.baseURL != "" {
		anthropicClientOptions = append(anthropicClientOptions, option.WithBaseURL(opts.baseURL))
		if opts.apiKey != "" {
			anthropicClientOptions = append(anthropicClientOptions, option.WithAuthToken(opts.apiKey))
		}
	} else if opts.apiKey != "" {
		anthropicClientOptions = append(anthropicClientOptions, option.WithAPIKey(opts.apiKey))
	}

	client := anthropic.NewClient(anthropicClientOptions...)
	return &anthropicClient{
		providerOptions: opts,
		options:         anthropicOpts,
		client:          client,
	}
}

func (a *anthropicClient) convertMessages(messages []message.Message) (anthropicMessages []anthropic.MessageParam) {
	for i, msg := range messages {
		cache := !a.options.disableCache && i > len(messages)-3
		switch msg.Role {
		case message.User:
			var contentBlocks []anthropic.ContentBlockParamUnion
			// The API rejects empty text blocks ("String should have at
			// least 1 character") — a caption-less bridge attachment
			// produces exactly that, so only emit text when present.
			if text := msg.Content().String(); strings.TrimSpace(text) != "" {
				contentBlocks = append(contentBlocks, anthropic.NewTextBlock(text))
			}
			for _, binaryContent := range msg.BinaryContent() {
				contentBlocks = append(contentBlocks, convertBinaryContent(binaryContent))
			}
			if len(contentBlocks) == 0 {
				logging.Warn("Skipping user message with no renderable content",
					"message_index", i, "message_id", msg.ID,
				)
				continue
			}
			if cache {
				lastBlock := &contentBlocks[len(contentBlocks)-1]
				switch {
				case lastBlock.OfText != nil:
					lastBlock.OfText.CacheControl = anthropic.NewCacheControlEphemeralParam()
				case lastBlock.OfImage != nil:
					lastBlock.OfImage.CacheControl = anthropic.NewCacheControlEphemeralParam()
				case lastBlock.OfDocument != nil:
					lastBlock.OfDocument.CacheControl = anthropic.NewCacheControlEphemeralParam()
				}
			}
			anthropicMessages = append(anthropicMessages, anthropic.NewUserMessage(contentBlocks...))

		case message.Assistant:
			blocks := []anthropic.ContentBlockParamUnion{}
			// Replay reasoning blocks first, exactly as produced — the API
			// verifies each block's signature over its content and rejects
			// modified blocks, while absent blocks merely forfeit reasoning
			// continuity across tool boundaries. Unsigned parts (legacy rows,
			// streamed previews, non-Anthropic sources) are skipped, which
			// preserves the pre-capability behavior for old data. Redacted
			// blocks carry an opaque payload that round-trips verbatim.
			if a.shouldReplayReasoning(msg) {
				for _, rc := range msg.ReasoningParts() {
					if rc.Redacted {
						if rc.Data != "" {
							blocks = append(blocks, anthropic.NewRedactedThinkingBlock(rc.Data))
						}
						continue
					}
					if rc.Signature != "" {
						blocks = append(blocks, anthropic.NewThinkingBlock(rc.Signature, rc.Thinking))
					}
				}
			}
			if strings.TrimSpace(msg.Content().String()) != "" {
				content := anthropic.NewTextBlock(msg.Content().String())
				blocks = append(blocks, content)
			}

			for _, toolCall := range msg.ToolCalls() {
				var inputMap map[string]any
				// Empty Input is valid on rows persisted before the
				// toolCalls() empty-input normalization (Bedrock zero-delta
				// tool_use blocks). Treat as no-args silently; reserve the
				// WARN for genuinely malformed JSON.
				if strings.TrimSpace(toolCall.Input) == "" {
					inputMap = map[string]any{}
				} else if err := json.Unmarshal([]byte(toolCall.Input), &inputMap); err != nil {
					logging.Warn("Failed to unmarshal tool call input, using empty input",
						"tool_call_id", toolCall.ID,
						"tool_name", toolCall.Name,
						"tool_input", toolCall.Input,
						"error", err,
					)
					inputMap = map[string]any{}
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(toolCall.ID, inputMap, toolCall.Name))
			}

			if len(blocks) == 0 {
				logging.Warn("Unexpected: assistant message with no content blocks reached provider conversion",
					"message_index", i, "message_id", msg.ID,
				)
				continue
			}

			if cache {
				lastBlock := &blocks[len(blocks)-1]
				if lastBlock.OfText != nil {
					lastBlock.OfText.CacheControl = anthropic.NewCacheControlEphemeralParam()
				} else if lastBlock.OfToolUse != nil {
					lastBlock.OfToolUse.CacheControl = anthropic.NewCacheControlEphemeralParam()
				}
			}
			anthropicMessages = append(anthropicMessages, anthropic.NewAssistantMessage(blocks...))

		case message.Tool:
			results := make([]anthropic.ContentBlockParamUnion, len(msg.ToolResults()))
			for i, toolResult := range msg.ToolResults() {
				if toolResult.IsImageToolResponse() {
					imageBlock, err := a.newToolResultImageBlock(toolResult)
					if err != nil {
						// Fallback to text if image parsing fails
						results[i] = anthropic.NewToolResultBlock(
							toolResult.ToolCallID,
							toolResult.Content,
							toolResult.IsError,
						)
					} else {
						results[i] = *imageBlock
					}
				} else {
					results[i] = anthropic.NewToolResultBlock(toolResult.ToolCallID, toolResult.Content, toolResult.IsError)
				}
			}
			if cache && len(results) > 0 {
				lastResult := &results[len(results)-1]
				if lastResult.OfToolResult != nil {
					lastResult.OfToolResult.CacheControl = anthropic.NewCacheControlEphemeralParam()
				}
			}
			anthropicMessages = append(anthropicMessages, anthropic.NewUserMessage(results...))
		}
	}
	return
}

// convertBinaryContent maps a binary attachment to the content block type
// the Anthropic Messages API actually accepts for its MIME type. Wrapping
// everything in an image block (the old behavior) produces an invalid
// request for non-image attachments — a PDF sent through the Telegram
// bridge poisoned its session permanently: Bedrock resets the response
// stream (HTTP/2 INTERNAL_ERROR) instead of returning a 400, and since the
// attachment is persisted in history, every subsequent turn replays it.
func convertBinaryContent(bc message.BinaryContent) anthropic.ContentBlockParamUnion {
	mimeType := strings.ToLower(strings.TrimSpace(bc.MIMEType))
	if i := strings.Index(mimeType, ";"); i >= 0 { // strip parameters, e.g. "; charset=utf-8"
		mimeType = strings.TrimSpace(mimeType[:i])
	}
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return anthropic.NewImageBlockBase64(mimeType, bc.String(models.ProviderAnthropic))
	case "application/pdf":
		return anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
			Data: bc.String(models.ProviderAnthropic),
		})
	}
	// Zero-byte payloads must not become empty content blocks — the API
	// rejects empty strings, and a persisted invalid attachment poisons
	// every subsequent turn of the session.
	if len(bc.Data) > 0 && strings.HasPrefix(mimeType, "text/") && utf8.Valid(bc.Data) {
		return anthropic.NewDocumentBlock(anthropic.PlainTextSourceParam{
			Data: string(bc.Data),
		})
	}
	// Unsupported by the API (audio, video, archives, ...): substitute a
	// text note instead of an invalid block. The bridge saves inbound media
	// to disk before dispatch, so the model can still reach the payload
	// through file tools via the referenced path.
	return anthropic.NewTextBlock(unsupportedAttachmentNote(bc))
}

// unsupportedAttachmentNote renders the placeholder text substituted for
// attachments no provider block type can carry. Shared by the anthropic
// and openai converters.
func unsupportedAttachmentNote(bc message.BinaryContent) string {
	saved := ""
	if bc.Path != "" {
		saved = fmt.Sprintf("; the file is saved at %q and can be inspected with file tools", bc.Path)
	}
	return fmt.Sprintf("[Attachment of unsupported media type %q omitted (%d bytes)%s]", bc.MIMEType, len(bc.Data), saved)
}

func (a *anthropicClient) convertTools(tools []toolsPkg.BaseTool) []anthropic.ToolUnionParam {
	anthropicTools := make([]anthropic.ToolUnionParam, len(tools))

	for i, tool := range tools {
		info := tool.Info()
		toolParam := anthropic.ToolParam{
			Name:        info.Name,
			Description: anthropic.String(info.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: info.Parameters,
				Required:   info.Required,
			},
		}

		// Single cache breakpoint on the last tool definition. The
		// deterministic ordering from OrderTools() ensures a stable prefix.
		if i == len(tools)-1 && !a.options.disableCache {
			toolParam.CacheControl = anthropic.NewCacheControlEphemeralParam()
		}

		anthropicTools[i] = anthropic.ToolUnionParam{OfTool: &toolParam}
	}

	return anthropicTools
}

// cacheControlParam returns an ephemeral cache control parameter unless caching
// is disabled, in which case it returns the zero value (no cache marker).
func cacheControlParam(disabled bool) anthropic.CacheControlEphemeralParam {
	if disabled {
		return anthropic.CacheControlEphemeralParam{}
	}
	return anthropic.NewCacheControlEphemeralParam()
}

func (a *anthropicClient) finishReason(reason string) message.FinishReason {
	switch reason {
	case "end_turn":
		return message.FinishReasonEndTurn
	case "max_tokens":
		return message.FinishReasonMaxTokens
	case "tool_use":
		return message.FinishReasonToolUse
	case "stop_sequence":
		return message.FinishReasonEndTurn
	default:
		return message.FinishReasonUnknown
	}
}

func (a *anthropicClient) preparedMessages(ctx context.Context, messages []anthropic.MessageParam, tools []anthropic.ToolUnionParam) anthropic.MessageNewParams {
	var thinkingParam anthropic.ThinkingConfigParamUnion
	var outputConfig anthropic.OutputConfigParam
	// convertMessages can legitimately return an empty slice — e.g. the
	// only user message had no renderable content and was skipped. Guard
	// the last-message peek so the request fails with the API's own
	// "at least one message required" validation error instead of an
	// index panic swallowed by RecoverPanic.
	isUser := false
	messageContent := ""
	var lastMessage anthropic.MessageParam
	if len(messages) > 0 {
		lastMessage = messages[len(messages)-1]
		isUser = lastMessage.Role == anthropic.MessageParamRoleUser
	}
	// TODO: parameterise temperature via agent config
	// Opus 4.7+ rejects non-default temperature values; omit to let the API use its default (1.0).
	temperature := anthropic.Float(0)
	if a.providerOptions.model.SupportsXHighThinking {
		temperature = param.Opt[float64]{}
	}
	if isUser {
		for _, m := range lastMessage.Content {
			if m.OfText != nil && m.OfText.Text != "" {
				messageContent = m.OfText.Text
			}
		}
		if a.providerOptions.model.SupportsAdaptiveThinking {
			adaptiveParam := anthropic.ThinkingConfigAdaptiveParam{}
			thinkingParam = anthropic.ThinkingConfigParamUnion{OfAdaptive: &adaptiveParam}
			if !a.providerOptions.model.SupportsXHighThinking {
				temperature = anthropic.Float(1)
			}
			effort := a.options.reasoningEffort
			if effort == "" {
				effort = "high"
			}
			outputConfig = anthropic.OutputConfigParam{
				Effort: anthropic.OutputConfigEffort(effort),
			}
			if a.options.taskBudget > 0 {
				budget := map[string]any{
					"type":  "tokens",
					"total": a.options.taskBudget,
				}
				if remaining, ok := ctx.Value(taskBudgetRemainingKey).(int64); ok && remaining > 0 {
					budget["remaining"] = remaining
				}
				outputConfig.SetExtraFields(map[string]any{
					"task_budget": budget,
				})
			}
		} else if messageContent != "" && a.options.shouldThink != nil && a.options.shouldThink(messageContent) {
			thinkingParam = anthropic.ThinkingConfigParamOfEnabled(int64(float64(a.providerOptions.maxTokens) * 0.8))
			temperature = anthropic.Float(1)
		}
	}

	// TODO: Consider adding ToolChoice in case of agent having output schema set, however it limits tool calls
	return anthropic.MessageNewParams{
		Model:        anthropic.Model(a.providerOptions.model.APIModel),
		MaxTokens:    a.providerOptions.maxTokens,
		Temperature:  temperature,
		Messages:     messages,
		Tools:        tools,
		Thinking:     thinkingParam,
		OutputConfig: outputConfig,
		System: []anthropic.TextBlockParam{
			{
				Text:         a.providerOptions.systemMessage,
				CacheControl: cacheControlParam(a.options.disableCache),
			},
		},
	}
}

func (a *anthropicClient) send(ctx context.Context, messages []message.Message, tools []toolsPkg.BaseTool) (resposne *ProviderResponse, err error) {
	preparedMessages := a.preparedMessages(ctx, a.convertMessages(messages), a.convertTools(tools))
	a.applyMetadata(ctx, &preparedMessages)
	cfg := config.Get()
	if cfg.Debug {
		jsonData, _ := json.Marshal(preparedMessages)
		logging.Debug("Prepared messages", "messages", string(jsonData))
	}

	attempts := 0
	for {
		attempts++
		var requestOpts []option.RequestOption
		if a.options.taskBudget > 0 {
			requestOpts = append(requestOpts, option.WithHeaderAdd("anthropic-beta", taskBudgetsBeta))
		}
		anthropicResponse, err := a.client.Messages.New(
			ctx,
			preparedMessages,
			requestOpts...,
		)
		// If there is an error we are going to see if we can retry the call
		if err != nil {
			logging.Error("Error in Anthropic API call", "error", err)
			retry, after, retryErr := a.shouldRetry(attempts, err)
			if retryErr != nil {
				return nil, retryErr
			}
			if retry {
				logging.WarnPersist(fmt.Sprintf("Retrying transient API error... attempt %d of %d", attempts, maxRetries), logging.PersistTimeArg, time.Millisecond*time.Duration(after+100))
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(after) * time.Millisecond):
					continue
				}
			}
			return nil, retryErr
		}

		var sb strings.Builder
		for _, block := range anthropicResponse.Content {
			if text, ok := block.AsAny().(anthropic.TextBlock); ok {
				sb.WriteString(text.Text)
			}
		}

		return &ProviderResponse{
			Content:   sb.String(),
			ToolCalls: a.toolCalls(*anthropicResponse),
			Reasoning: a.reasoningParts(*anthropicResponse),
			Usage:     a.usage(*anthropicResponse),
		}, nil
	}
}

func (a *anthropicClient) stream(ctx context.Context, messages []message.Message, tools []toolsPkg.BaseTool) <-chan ProviderEvent {
	preparedMessages := a.preparedMessages(ctx, a.convertMessages(messages), a.convertTools(tools))
	a.applyMetadata(ctx, &preparedMessages)
	cfg := config.Get()

	var sessionID string
	requestSeqID := (len(messages) + 1) / 2
	if cfg.Debug {
		if sid, ok := ctx.Value(toolsPkg.SessionIDContextKey).(string); ok {
			sessionID = sid
		}
		jsonData, _ := json.Marshal(preparedMessages)
		if sessionID != "" {
			filepath := logging.WriteRequestMessageJson(sessionID, requestSeqID, preparedMessages)
			logging.Debug("Prepared messages", "filepath", filepath)
		} else {
			logging.Debug("Prepared messages", "messages", string(jsonData))
		}
	}
	attempts := 0
	eventChan := make(chan ProviderEvent)
	go func() {
		for {
			attempts++
			var requestOpts []option.RequestOption
			if a.options.taskBudget > 0 {
				requestOpts = append(requestOpts, option.WithHeaderAdd("anthropic-beta", taskBudgetsBeta))
			}
			anthropicStream := a.client.Messages.NewStreaming(
				ctx,
				preparedMessages,
				requestOpts...,
			)
			accumulatedMessage := anthropic.Message{}

			currentToolCallID := ""

			reader := newStreamReader(ctx, func() (anthropic.MessageStreamEventUnion, bool) {
				if !anthropicStream.Next() {
					return anthropic.MessageStreamEventUnion{}, false
				}
				return anthropicStream.Current(), true
			}, func() {
				anthropicStream.Close()
			})

			var streamErr error
			for {
				event, ok, err := reader.Recv()
				if err != nil {
					streamErr = err
					break
				}
				if !ok {
					break
				}
				accErr := accumulatedMessage.Accumulate(event)
				if accErr != nil {
					logging.Warn("Error accumulating message", "error", accErr)
					continue
				}

				switch event := event.AsAny().(type) {
				case anthropic.ContentBlockStartEvent:
					switch event.ContentBlock.Type {
					case "text":
						eventChan <- ProviderEvent{Type: EventContentStart}
					case "tool_use":
						currentToolCallID = event.ContentBlock.ID
						eventChan <- ProviderEvent{
							Type: EventToolUseStart,
							ToolCall: &message.ToolCall{
								ID:       event.ContentBlock.ID,
								Name:     event.ContentBlock.Name,
								Type:     event.ContentBlock.Type,
								Finished: false,
							},
						}
					}

				case anthropic.ContentBlockDeltaEvent:
					if event.Delta.Type == "thinking_delta" && event.Delta.Thinking != "" {
						eventChan <- ProviderEvent{
							Type:     EventThinkingDelta,
							Thinking: event.Delta.Thinking,
						}
					} else if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
						eventChan <- ProviderEvent{
							Type:    EventContentDelta,
							Content: event.Delta.Text,
						}
					} else if event.Delta.Type == "input_json_delta" {
						if currentToolCallID != "" {
							eventChan <- ProviderEvent{
								Type: EventToolUseDelta,
								ToolCall: &message.ToolCall{
									ID:       currentToolCallID,
									Finished: false,
									Input:    event.Delta.JSON.PartialJSON.Raw(),
								},
							}
						}
					}
				case anthropic.ContentBlockStopEvent:
					if currentToolCallID != "" {
						eventChan <- ProviderEvent{
							Type: EventToolUseStop,
							ToolCall: &message.ToolCall{
								ID: currentToolCallID,
							},
						}
						currentToolCallID = ""
					} else {
						eventChan <- ProviderEvent{Type: EventContentStop}
					}

				case anthropic.MessageStopEvent:
					var sb strings.Builder
					for _, block := range accumulatedMessage.Content {
						if text, ok := block.AsAny().(anthropic.TextBlock); ok {
							sb.WriteString(text.Text)
						}
					}

					eventChan <- ProviderEvent{
						Type: EventComplete,
						Response: &ProviderResponse{
							Content:      sb.String(),
							ToolCalls:    a.toolCalls(accumulatedMessage),
							Reasoning:    a.reasoningParts(accumulatedMessage),
							Usage:        a.usage(accumulatedMessage),
							FinishReason: a.finishReason(string(accumulatedMessage.StopReason)),
						},
					}
				}
			}
			reader.Close()

			if errors.Is(streamErr, ErrStreamStalled) {
				logging.Warn("Anthropic stream stalled, will retry", "attempt", attempts)
				if attempts < maxRetries {
					continue
				}
				eventChan <- ProviderEvent{Type: EventError, Error: streamErr}
				close(eventChan)
				return
			}

			err := anthropicStream.Err()
			if streamErr != nil && err == nil {
				err = streamErr
			}
			if err == nil || errors.Is(err, io.EOF) {
				// If the stream closed without a MessageStopEvent (truncated response),
				// we still need to emit EventComplete so the agent loop doesn't hang.
				if accumulatedMessage.StopReason == "" {
					logging.Warn("Anthropic stream closed without MessageStopEvent (truncated response)")
					var sb strings.Builder
					for _, block := range accumulatedMessage.Content {
						if text, ok := block.AsAny().(anthropic.TextBlock); ok {
							sb.WriteString(text.Text)
						}
					}
					eventChan <- ProviderEvent{
						Type: EventComplete,
						Response: &ProviderResponse{
							Content:      sb.String(),
							ToolCalls:    a.toolCalls(accumulatedMessage),
							Reasoning:    a.reasoningParts(accumulatedMessage),
							Usage:        a.usage(accumulatedMessage),
							FinishReason: message.FinishReasonEndTurn,
						},
					}
				}
				close(eventChan)
				return
			}
			// Retry transient transport errors (e.g. unexpected EOF, connection reset)
			if isTransientStreamError(err) {
				logging.Warn("Anthropic stream transport error, will retry", "attempt", attempts, "error", err)
				if attempts < maxRetries {
					backoffMs := 2000 * (1 << (attempts - 1))
					select {
					case <-ctx.Done():
						if ctx.Err() != nil {
							eventChan <- ProviderEvent{Type: EventError, Error: ctx.Err()}
						}
						close(eventChan)
						return
					case <-time.After(time.Duration(backoffMs) * time.Millisecond):
						continue
					}
				}
				eventChan <- ProviderEvent{Type: EventError, Error: err}
				close(eventChan)
				return
			}

			// If there is an error we are going to see if we can retry the call
			retry, after, retryErr := a.shouldRetry(attempts, err)
			if retryErr != nil {
				eventChan <- ProviderEvent{Type: EventError, Error: retryErr}
				close(eventChan)
				return
			}
			if retry {
				logging.WarnPersist(fmt.Sprintf("Retrying transient API error... attempt %d of %d", attempts, maxRetries), logging.PersistTimeArg, time.Millisecond*time.Duration(after+100))
				select {
				case <-ctx.Done():
					// context cancelled
					if ctx.Err() != nil {
						eventChan <- ProviderEvent{Type: EventError, Error: ctx.Err()}
					}
					close(eventChan)
					return
				case <-time.After(time.Duration(after) * time.Millisecond):
					continue
				}
			}
			if ctx.Err() != nil {
				eventChan <- ProviderEvent{Type: EventError, Error: ctx.Err()}
			}

			close(eventChan)
			return
		}
	}()
	return eventChan
}

func (a *anthropicClient) applyMetadata(ctx context.Context, params *anthropic.MessageNewParams) {
	resolved := resolveMetadata(ctx, a.providerOptions.metadata)
	if resolved == nil {
		return
	}
	meta := anthropic.MetadataParam{}
	extraFields := make(map[string]any)
	for fieldName, value := range resolved {
		if fieldName == "user_id" {
			if s, ok := value.(string); ok {
				meta.UserID = param.NewOpt(s)
				continue
			}
		}
		extraFields[fieldName] = value
	}
	if len(extraFields) > 0 {
		meta.SetExtraFields(extraFields)
	}
	params.Metadata = meta
}

// retryableHTTPStatuses are the status codes we treat as transient and
// worth retrying with exponential backoff. Applies to ALL anthropic-SDK
// transports (direct Anthropic API, AWS Bedrock, GCP Vertex) — the SDK
// surfaces upstream HTTP status codes verbatim on `*anthropic.Error`:
//   - 429 Too Many Requests        — rate limit (Retry-After honored)
//   - 503 Service Unavailable      — standard transient-overload signal.
//     Notably surfaces from AWS Bedrock's serviceUnavailableException
//     ("Bedrock is unable to process your request") on pre-stream
//     rejection, but Anthropic's direct API and Vertex also return 503
//     for genuinely transient upstream overload.
//   - 529 Overloaded               — Anthropic's own overload signal.
//
// 500 / 502 / 504 are deliberately excluded — they tend to signal real
// upstream bugs rather than transient blips, and aggressive retry on
// them just amplifies impact during incidents.
//
// The retry path uses 2s/4s/8s/… exponential backoff with 20% jitter,
// capped by maxRetries.
var retryableHTTPStatuses = map[int]struct{}{
	429: {},
	503: {},
	529: {},
}

func (a *anthropicClient) shouldRetry(attempts int, err error) (bool, int64, error) {
	var apierr *anthropic.Error
	if !errors.As(err, &apierr) {
		return false, 0, err
	}

	if _, ok := retryableHTTPStatuses[apierr.StatusCode]; !ok {
		return false, 0, err
	}

	if attempts > maxRetries {
		return false, 0, fmt.Errorf("maximum retry attempts reached for HTTP %d: %d retries", apierr.StatusCode, maxRetries)
	}

	retryMs := 0
	retryAfterValues := apierr.Response.Header.Values("Retry-After")

	backoffMs := 2000 * (1 << (attempts - 1))
	jitterMs := int(float64(backoffMs) * 0.2)
	retryMs = backoffMs + jitterMs
	if len(retryAfterValues) > 0 {
		if _, err := fmt.Sscanf(retryAfterValues[0], "%d", &retryMs); err == nil {
			retryMs = retryMs * 1000
		}
	}
	return true, int64(retryMs), nil
}

// shouldReplayReasoning gates thinking-block replay to messages produced by
// the same provider family this client talks to. Signatures are
// provider-issued: Anthropic documents silently dropping blocks signed by a
// different model, but an Anthropic-compatible endpoint's behavior for a
// cross-vendor signature (e.g. a Moonshot-signed block sent to Anthropic
// after a mid-session model switch, or vice versa) is undocumented — skip
// them; absence merely forfeits reasoning continuity. Messages without a
// recorded/known model keep replaying: such rows predate model tracking and
// came from this session's own provider.
func (a *anthropicClient) shouldReplayReasoning(msg message.Message) bool {
	if msg.Model == "" {
		return true
	}
	m, ok := models.SupportedModels[msg.Model]
	if !ok {
		return true
	}
	return m.Provider == a.providerOptions.model.Provider
}

// reasoningParts extracts the finalized reasoning blocks from a response,
// verbatim and in emission order: thinking blocks carry text + signature,
// redacted_thinking blocks carry the opaque payload. These are persisted
// as-is so convertMessages can replay them byte-exact — Anthropic verifies
// the signature over each replayed block's content.
func (a *anthropicClient) reasoningParts(msg anthropic.Message) []message.ReasoningContent {
	var parts []message.ReasoningContent
	for _, block := range msg.Content {
		switch variant := block.AsAny().(type) {
		case anthropic.ThinkingBlock:
			parts = append(parts, message.ReasoningContent{
				Thinking:  variant.Thinking,
				Signature: variant.Signature,
			})
		case anthropic.RedactedThinkingBlock:
			parts = append(parts, message.ReasoningContent{
				Redacted: true,
				Data:     variant.Data,
			})
		}
	}
	return parts
}

func (a *anthropicClient) toolCalls(msg anthropic.Message) []message.ToolCall {
	var toolCalls []message.ToolCall

	for _, block := range msg.Content {
		switch variant := block.AsAny().(type) {
		case anthropic.ToolUseBlock:
			// Bedrock's eventstream omits "input" from content_block_start,
			// so when a tool_use receives zero input_json_delta events the
			// accumulator leaves Input as nil bytes. Persisting "" is invalid
			// JSON; normalize to "{}" so future replays don't need to
			// recover. Tool-arg validation still happens in the tool layer.
			input := string(variant.Input)
			if strings.TrimSpace(input) == "" {
				input = "{}"
			}
			toolCall := message.ToolCall{
				ID:       variant.ID,
				Name:     variant.Name,
				Input:    input,
				Type:     string(variant.Type),
				Finished: true,
			}
			toolCalls = append(toolCalls, toolCall)
		}
	}

	return toolCalls
}

func (a *anthropicClient) usage(msg anthropic.Message) TokenUsage {
	return TokenUsage{
		InputTokens:         msg.Usage.InputTokens,
		OutputTokens:        msg.Usage.OutputTokens,
		CacheCreationTokens: msg.Usage.CacheCreationInputTokens,
		CacheReadTokens:     msg.Usage.CacheReadInputTokens,
	}
}

func WithAnthropicBedrock(useBedrock bool) AnthropicOption {
	return func(options *anthropicOptions) {
		if useBedrock {
			options.useVertex = false
		}
		options.useBedrock = useBedrock
	}
}

func WithAnthropicDisableCache() AnthropicOption {
	return func(options *anthropicOptions) {
		options.disableCache = true
	}
}

func DefaultShouldThinkFn(s string) bool {
	return strings.Contains(strings.ToLower(s), "think")
}

func WithAnthropicShouldThinkFn(fn func(string) bool) AnthropicOption {
	return func(options *anthropicOptions) {
		options.shouldThink = fn
	}
}

func WithAnthropicReasoningEffort(effort string) AnthropicOption {
	return func(options *anthropicOptions) {
		options.reasoningEffort = effort
	}
}

func WithAnthropicTaskBudget(budget int64) AnthropicOption {
	return func(options *anthropicOptions) {
		options.taskBudget = budget
	}
}

type taskBudgetRemainingKeyType struct{}

var taskBudgetRemainingKey = taskBudgetRemainingKeyType{}

// TaskBudgetRemainingContext returns a context with the task budget remaining value set.
// Used after compaction to carry the budget across context resets.
func TaskBudgetRemainingContext(ctx context.Context, remaining int64) context.Context {
	return context.WithValue(ctx, taskBudgetRemainingKey, remaining)
}

func WithVertexAI(projectID, localtion string, localForCounting string) AnthropicOption {
	return func(options *anthropicOptions) {
		options.useVertex = true
		options.useBedrock = false
		options.vertexOptions = vertexOptions{projectID: projectID, location: localtion, locationForCounting: localForCounting}
	}
}

// parses image tool response and creates an Anthropic image content block
func (a *anthropicClient) newToolResultImageBlock(toolResult message.ToolResult) (*anthropic.ContentBlockParamUnion, error) {
	// HACK: replace with proper fields passing
	var imageData struct {
		Type     string `json:"type"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	}

	if err := json.Unmarshal([]byte(toolResult.Content), &imageData); err != nil {
		return nil, err
	}
	imageBlock := anthropic.NewImageBlockBase64(imageData.MimeType, imageData.Data)

	toolBlock := anthropic.ToolResultBlockParam{
		ToolUseID: toolResult.ToolCallID,
		Content: []anthropic.ToolResultBlockParamContentUnion{
			{OfImage: imageBlock.OfImage},
		},
		IsError: param.NewOpt(toolResult.IsError),
	}
	return &anthropic.ContentBlockParamUnion{OfToolResult: &toolBlock}, nil
}

// countTokensImagePlaceholder is what swapped-out image/document blocks
// become for the count_tokens call. Per swapped block we add an estimate to
// compensate.
const (
	countTokensImagePlaceholder    = "[image elided for tokenization]"
	countTokensImageTokenEstimate  = 1500 // Anthropic's rough per-image budget at standard res
	countTokensDocumentPlaceholder = "[document elided for tokenization]"
	// countTokensDocumentBytesPerToken converts a PDF's decoded byte size
	// into a rough token estimate. Anthropic budgets 1,500-3,000 tokens per
	// PDF page (each page is processed as an image plus extracted text) and
	// mixed-content PDFs typically weigh tens-to-hundreds of KB per page;
	// ~100 bytes/token lands the estimate in the right order of magnitude
	// for compaction-threshold purposes. Floored at one image-equivalent so
	// tiny PDFs don't count as free.
	countTokensDocumentBytesPerToken = 100
)

// messagesContainMedia reports whether any message holds an image or
// document block, either at the top level or nested inside a tool_result.
// Used as a fast-path guard for stripMediaForCountTokens.
func messagesContainMedia(messages []anthropic.MessageParam) bool {
	for _, msg := range messages {
		for _, block := range msg.Content {
			if block.OfImage != nil || block.OfDocument != nil {
				return true
			}
			if block.OfToolResult != nil {
				for _, inner := range block.OfToolResult.Content {
					if inner.OfImage != nil {
						return true
					}
				}
			}
		}
	}
	return false
}

// documentTokenEstimate approximates the token cost of a stripped document
// block. Plain-text sources return 0 because the caller re-inlines their
// text verbatim (the endpoint counts it exactly); base64 PDFs are estimated
// from decoded payload size.
func documentTokenEstimate(doc *anthropic.DocumentBlockParam) int64 {
	if doc.Source.OfBase64 != nil {
		decodedBytes := len(doc.Source.OfBase64.Data) * 3 / 4
		if est := int64(decodedBytes / countTokensDocumentBytesPerToken); est > countTokensImageTokenEstimate {
			return est
		}
	}
	return countTokensImageTokenEstimate
}

// stripMediaForCountTokens returns a copy of messages with every image and
// document block swapped for a short text stand-in, plus the token estimate
// compensating for the removed blocks. LiteLLM's count_tokens proxy only
// understands text/tool block types — it 500s on Anthropic's "image" and
// "document" content types — so we keep the request text-only and account
// for the stripped media locally. Plain-text document sources are re-inlined
// as text blocks (counted exactly by the endpoint, estimate 0); images and
// base64 PDFs get placeholder text plus a local estimate.
//
// Fast path: if no media is present the input slice is returned unchanged
// (estimate=0), avoiding per-message allocations for text-only
// conversations — which is the common case even on the Bedrock path.
func stripMediaForCountTokens(messages []anthropic.MessageParam) ([]anthropic.MessageParam, int64) {
	if !messagesContainMedia(messages) {
		return messages, 0
	}
	var extraTokens int64
	out := make([]anthropic.MessageParam, len(messages))
	for i, msg := range messages {
		newContent := make([]anthropic.ContentBlockParamUnion, 0, len(msg.Content))
		for _, block := range msg.Content {
			if block.OfImage != nil {
				extraTokens += countTokensImageTokenEstimate
				newContent = append(newContent, anthropic.NewTextBlock(countTokensImagePlaceholder))
				continue
			}
			if block.OfDocument != nil {
				if txt := block.OfDocument.Source.OfText; txt != nil {
					// Text-source document: count its content exactly.
					newContent = append(newContent, anthropic.NewTextBlock(txt.Data))
					continue
				}
				extraTokens += documentTokenEstimate(block.OfDocument)
				newContent = append(newContent, anthropic.NewTextBlock(countTokensDocumentPlaceholder))
				continue
			}
			if block.OfToolResult != nil {
				tr := *block.OfToolResult
				newInner := make([]anthropic.ToolResultBlockParamContentUnion, 0, len(tr.Content))
				for _, inner := range tr.Content {
					if inner.OfImage != nil {
						extraTokens += countTokensImageTokenEstimate
						newInner = append(newInner, anthropic.ToolResultBlockParamContentUnion{
							OfText: &anthropic.TextBlockParam{Text: countTokensImagePlaceholder},
						})
						continue
					}
					newInner = append(newInner, inner)
				}
				tr.Content = newInner
				newContent = append(newContent, anthropic.ContentBlockParamUnion{OfToolResult: &tr})
				continue
			}
			newContent = append(newContent, block)
		}
		out[i] = anthropic.MessageParam{Role: msg.Role, Content: newContent}
	}
	return out, extraTokens
}

func (a *anthropicClient) countTokens(ctx context.Context, messages []message.Message, tools []toolsPkg.BaseTool) (int64, error) {
	if a.countTokensUnsupported.Load() {
		return 0, fmt.Errorf("count_tokens previously answered 404/405 on this endpoint: %w", errors.ErrUnsupported)
	}
	anthropicMessages := a.convertMessages(messages)
	// Only strip media for Bedrock, where count_tokens is routed through the
	// LiteLLM proxy that rejects Anthropic's "image" and "document" content
	// types. The native Anthropic and Vertex count_tokens endpoints handle
	// both accurately.
	var mediaTokenEstimate int64
	if a.options.useBedrock {
		anthropicMessages, mediaTokenEstimate = stripMediaForCountTokens(anthropicMessages)
	}
	anthropicTools := a.convertTools(tools)
	countTools := make([]anthropic.MessageCountTokensToolUnionParam, len(anthropicTools))
	for i, t := range anthropicTools {
		countTools[i] = anthropic.MessageCountTokensToolUnionParam{OfTool: t.OfTool}
	}

	params := anthropic.MessageCountTokensParams{
		Model:    anthropic.Model(a.providerOptions.model.APIModel),
		Messages: anthropicMessages,
		Tools:    countTools,
	}

	// Add system message if present
	if a.providerOptions.systemMessage != "" {
		params.System = anthropic.MessageCountTokensParamsSystemUnion{
			OfTextBlockArray: []anthropic.TextBlockParam{
				{
					Text: a.providerOptions.systemMessage,
				},
			},
		}
	}

	response, err := a.client.Messages.CountTokens(ctx, params)
	if err != nil {
		var apierr *anthropic.Error
		if errors.As(err, &apierr) && (apierr.StatusCode == http.StatusNotFound || apierr.StatusCode == http.StatusMethodNotAllowed) {
			a.countTokensUnsupported.Store(true)
			logging.Info("count_tokens endpoint not implemented by provider; using local estimation for the rest of the session",
				"model", a.providerOptions.model.Name,
				"status", apierr.StatusCode,
			)
			return 0, fmt.Errorf("count_tokens endpoint not implemented (HTTP %d): %w", apierr.StatusCode, errors.ErrUnsupported)
		}
		return 0, fmt.Errorf("failed to count tokens: %w", err)
	}

	return response.InputTokens + mediaTokenEstimate, nil
}

func (a *anthropicClient) setMaxTokens(maxTokens int64) {
	a.providerOptions.maxTokens = maxTokens
}

func (a *anthropicClient) maxTokens() int64 {
	return a.providerOptions.maxTokens
}

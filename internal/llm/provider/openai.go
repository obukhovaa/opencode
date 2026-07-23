package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
)

type openaiOptions struct {
	disableCache    bool
	reasoningEffort string
	legacyMaxTokens bool
}

type OpenAIOption func(*openaiOptions)

type openaiClient struct {
	providerOptions providerClientOptions
	options         openaiOptions
	client          openai.Client
}

type OpenAIClient ProviderClient

func newOpenAIClient(opts providerClientOptions) OpenAIClient {
	openaiOpts := openaiOptions{
		reasoningEffort: "medium",
	}
	for _, o := range opts.openaiOptions {
		o(&openaiOpts)
	}

	openaiClientOptions := []option.RequestOption{}
	if opts.apiKey != "" {
		openaiClientOptions = append(openaiClientOptions, option.WithAPIKey(opts.apiKey))
	}
	if opts.baseURL != "" {
		openaiClientOptions = append(openaiClientOptions, option.WithBaseURL(opts.baseURL))
	}
	if opts.headers != nil {
		for key, value := range opts.headers {
			openaiClientOptions = append(openaiClientOptions, option.WithHeader(key, value))
		}
	}

	client := openai.NewClient(openaiClientOptions...)
	return &openaiClient{
		providerOptions: opts,
		options:         openaiOpts,
		client:          client,
	}
}

func (o *openaiClient) convertMessages(messages []message.Message) (openaiMessages []openai.ChatCompletionMessageParamUnion) {
	// Add system message first
	openaiMessages = append(openaiMessages, openai.SystemMessage(o.providerOptions.systemMessage))

	for _, msg := range messages {
		switch msg.Role {
		case message.User:
			var content []openai.ChatCompletionContentPartUnionParam
			// Mirror the anthropic converter: a caption-less bridge
			// attachment arrives with empty text — don't emit an empty
			// text part alongside the attachment.
			if text := msg.Content().String(); strings.TrimSpace(text) != "" {
				textBlock := openai.ChatCompletionContentPartTextParam{Text: text}
				content = append(content, openai.ChatCompletionContentPartUnionParam{OfText: &textBlock})
			}
			for _, binaryContent := range msg.BinaryContent() {
				content = append(content, convertBinaryContentOpenAI(binaryContent))
			}
			if len(content) == 0 {
				logging.Warn("Skipping user message with no renderable content",
					"message_id", msg.ID,
				)
				continue
			}

			openaiMessages = append(openaiMessages, openai.UserMessage(content))

		case message.Assistant:
			assistantMsg := openai.ChatCompletionAssistantMessageParam{
				Role: "assistant",
			}

			if strings.TrimSpace(msg.Content().String()) != "" {
				assistantMsg.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openai.String(msg.Content().String()),
				}
			}

			if len(msg.ToolCalls()) > 0 {
				assistantMsg.ToolCalls = make([]openai.ChatCompletionMessageToolCallParam, len(msg.ToolCalls()))
				for i, call := range msg.ToolCalls() {
					// Empty arguments are valid on rows persisted before the
					// toolCalls() normalization. Substitute "{}" so the OpenAI
					// API receives a parsable JSON object instead of an empty
					// string.
					arguments := call.Input
					if strings.TrimSpace(arguments) == "" {
						arguments = "{}"
					}
					assistantMsg.ToolCalls[i] = openai.ChatCompletionMessageToolCallParam{
						ID:   call.ID,
						Type: "function",
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      call.Name,
							Arguments: arguments,
						},
					}
				}
			}

			openaiMessages = append(openaiMessages, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &assistantMsg,
			})

		case message.Tool:
			for _, result := range msg.ToolResults() {
				openaiMessages = append(openaiMessages,
					openai.ToolMessage(result.Content, result.ToolCallID),
				)
			}
		}
	}

	return
}

// convertBinaryContentOpenAI maps a binary attachment to the content part
// type the OpenAI Chat Completions API accepts for its MIME type — the
// same defect class as the anthropic converter: wrapping every attachment
// in an image_url part makes any request containing a PDF (or voice note,
// archive, ...) invalid, and since attachments persist in session history,
// one bad attachment poisons every subsequent turn of the session.
func convertBinaryContentOpenAI(bc message.BinaryContent) openai.ChatCompletionContentPartUnionParam {
	mimeType := strings.ToLower(strings.TrimSpace(bc.MIMEType))
	if i := strings.Index(mimeType, ";"); i >= 0 { // strip parameters, e.g. "; charset=utf-8"
		mimeType = strings.TrimSpace(mimeType[:i])
	}
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		imageURL := openai.ChatCompletionContentPartImageImageURLParam{URL: bc.String(models.ProviderOpenAI)}
		return openai.ChatCompletionContentPartUnionParam{
			OfImageURL: &openai.ChatCompletionContentPartImageParam{ImageURL: imageURL},
		}
	case "application/pdf":
		filename := "document.pdf"
		if bc.Path != "" {
			filename = filepath.Base(bc.Path)
		}
		return openai.ChatCompletionContentPartUnionParam{
			OfFile: &openai.ChatCompletionContentPartFileParam{
				File: openai.ChatCompletionContentPartFileFileParam{
					// file_data expects a data URL, which the OpenAI
					// variant of String() already produces.
					FileData: openai.String(bc.String(models.ProviderOpenAI)),
					Filename: openai.String(filename),
				},
			},
		}
	}
	// Zero-byte payloads must not become empty text parts — the persisted
	// attachment would replay an invalid part on every subsequent turn.
	if len(bc.Data) > 0 && strings.HasPrefix(mimeType, "text/") && utf8.Valid(bc.Data) {
		return openai.ChatCompletionContentPartUnionParam{
			OfText: &openai.ChatCompletionContentPartTextParam{Text: string(bc.Data)},
		}
	}
	// Unsupported by the API (audio outside the audio-preview models,
	// video, archives, ...): substitute a text note instead of an invalid
	// part. The bridge saves inbound media to disk before dispatch, so the
	// model can still reach the payload through file tools.
	return openai.ChatCompletionContentPartUnionParam{
		OfText: &openai.ChatCompletionContentPartTextParam{Text: unsupportedAttachmentNote(bc)},
	}
}

func (o *openaiClient) convertTools(tools []tools.BaseTool) []openai.ChatCompletionToolParam {
	openaiTools := make([]openai.ChatCompletionToolParam, len(tools))

	for i, tool := range tools {
		info := tool.Info()
		openaiTools[i] = openai.ChatCompletionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name:        info.Name,
				Description: openai.String(info.Description),
				Parameters: openai.FunctionParameters{
					"type":       "object",
					"properties": info.Parameters,
					"required":   info.Required,
				},
			},
		}
	}

	return openaiTools
}

func (o *openaiClient) finishReason(reason string) message.FinishReason {
	switch reason {
	case "stop":
		return message.FinishReasonEndTurn
	case "length":
		return message.FinishReasonMaxTokens
	case "tool_calls":
		return message.FinishReasonToolUse
	default:
		return message.FinishReasonUnknown
	}
}

func (o *openaiClient) preparedParams(messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolParam) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(o.providerOptions.model.APIModel),
		Messages: messages,
		Tools:    tools,
	}

	if o.providerOptions.model.CanReason == true {
		params.MaxCompletionTokens = openai.Int(o.providerOptions.maxTokens)
		switch o.options.reasoningEffort {
		case "low":
			params.ReasoningEffort = shared.ReasoningEffortLow
		case "medium":
			params.ReasoningEffort = shared.ReasoningEffortMedium
		case "high":
			params.ReasoningEffort = shared.ReasoningEffortHigh
		default:
			params.ReasoningEffort = shared.ReasoningEffortMedium
		}
	} else {
		params.MaxCompletionTokens = openai.Int(o.providerOptions.maxTokens)
		if o.options.legacyMaxTokens {
			params.MaxTokens = openai.Int(o.providerOptions.maxTokens)
		}
	}

	return params
}

func (o *openaiClient) send(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (response *ProviderResponse, err error) {
	params := o.preparedParams(o.convertMessages(messages), o.convertTools(tools))
	o.applyMetadata(ctx, &params)
	cfg := config.Get()
	if cfg.Debug {
		jsonData, _ := json.Marshal(params)
		logging.Debug("Prepared messages", "messages", string(jsonData))
	}
	attempts := 0
	for {
		attempts++
		openaiResponse, err := o.client.Chat.Completions.New(
			ctx,
			params,
		)
		// If there is an error we are going to see if we can retry the call
		if err != nil {
			retry, after, retryErr := o.shouldRetry(attempts, err)
			if retryErr != nil {
				return nil, retryErr
			}
			if retry {
				logging.WarnPersist(fmt.Sprintf("Retrying due to rate limit... attempt %d of %d", attempts, maxRetries), logging.PersistTimeArg, time.Millisecond*time.Duration(after+100))
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(after) * time.Millisecond):
					continue
				}
			}
			return nil, retryErr
		}

		content := ""
		if openaiResponse.Choices[0].Message.Content != "" {
			content = openaiResponse.Choices[0].Message.Content
		}

		toolCalls := o.toolCalls(*openaiResponse)
		finishReason := o.finishReason(string(openaiResponse.Choices[0].FinishReason))

		if len(toolCalls) > 0 {
			finishReason = message.FinishReasonToolUse
		}

		return &ProviderResponse{
			Content:      content,
			ToolCalls:    toolCalls,
			Usage:        o.usage(*openaiResponse),
			FinishReason: finishReason,
		}, nil
	}
}

func (o *openaiClient) stream(ctx context.Context, messages []message.Message, tools []tools.BaseTool) <-chan ProviderEvent {
	params := o.preparedParams(o.convertMessages(messages), o.convertTools(tools))
	o.applyMetadata(ctx, &params)
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}

	cfg := config.Get()
	if cfg.Debug {
		jsonData, _ := json.Marshal(params)
		logging.Debug("Prepared messages", "messages", string(jsonData))
	}

	attempts := 0
	eventChan := make(chan ProviderEvent)

	go func() {
		// emittedOutput latches once any streamed content has reached the
		// consumer — a retry after that point would replay the request and
		// duplicate the assistant message (processEvent appends every delta).
		// rstStreamRetries is a dedicated budget for peer-initiated HTTP/2
		// RST_STREAM resets, separate from attempts.
		emittedOutput := false
		rstStreamRetries := 0
		for {
			attempts++
			openaiStream := o.client.Chat.Completions.NewStreaming(
				ctx,
				params,
			)

			acc := openai.ChatCompletionAccumulator{}
			currentContent := ""
			toolCalls := make([]message.ToolCall, 0)

			reader := newStreamReader(ctx, func() (openai.ChatCompletionChunk, bool) {
				if !openaiStream.Next() {
					return openai.ChatCompletionChunk{}, false
				}
				return openaiStream.Current(), true
			}, func() {
				openaiStream.Close()
			})

			var streamErr error
			for {
				chunk, ok, recvErr := reader.Recv()
				if recvErr != nil {
					streamErr = recvErr
					break
				}
				if !ok {
					break
				}

				acc.AddChunk(chunk)

				for _, choice := range chunk.Choices {
					if choice.Delta.Content != "" {
						emittedOutput = true
						eventChan <- ProviderEvent{
							Type:    EventContentDelta,
							Content: choice.Delta.Content,
						}
						currentContent += choice.Delta.Content
					}
				}
			}
			reader.Close()

			if errors.Is(streamErr, ErrStreamStalled) {
				logging.Warn("OpenAI stream stalled, will retry", "attempt", attempts)
				if attempts < maxRetries {
					continue
				}
				eventChan <- ProviderEvent{Type: EventError, Error: streamErr}
				close(eventChan)
				return
			}

			err := openaiStream.Err()
			if streamErr != nil && err == nil {
				err = streamErr
			}
			if err == nil || errors.Is(err, io.EOF) {
				// Guard against truncated streams where Choices may be empty
				finishReason := message.FinishReasonEndTurn
				if len(acc.ChatCompletion.Choices) > 0 {
					finishReason = o.finishReason(string(acc.ChatCompletion.Choices[0].FinishReason))
					if len(acc.ChatCompletion.Choices[0].Message.ToolCalls) > 0 {
						toolCalls = append(toolCalls, o.toolCalls(acc.ChatCompletion)...)
					}
				} else {
					logging.Warn("OpenAI stream closed with empty Choices (truncated response)")
				}
				if len(toolCalls) > 0 {
					finishReason = message.FinishReasonToolUse
				}

				eventChan <- ProviderEvent{
					Type: EventComplete,
					Response: &ProviderResponse{
						Content:      currentContent,
						ToolCalls:    toolCalls,
						Usage:        o.usage(acc.ChatCompletion),
						FinishReason: finishReason,
					},
				}
				close(eventChan)
				return
			}

			// Retry transient transport errors (e.g. unexpected EOF, connection reset)
			if isTransientStreamError(err) {
				logging.Warn("OpenAI stream transport error, will retry", "attempt", attempts, "error", err)
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

			// Peer-initiated HTTP/2 RST_STREAM (e.g. INTERNAL_ERROR /
			// REFUSED_STREAM): a stream/connection-level reset from the proxy
			// (litellm) or its load balancer — typically a stale pooled
			// connection or a transient upstream blip. Retry on a fresh
			// connection with a small dedicated budget and short backoff, but
			// ONLY while nothing has reached the consumer yet: a pre-first-token
			// reset is safe to replay, whereas retrying after deltas were
			// emitted would duplicate the assistant message. A permanent error a
			// proxy wrapped as RST_STREAM also lands here, but the small budget
			// caps the wasted work at a few quick attempts.
			if isRetryableRSTStreamError(err) {
				if !emittedOutput && rstStreamRetries < maxRSTStreamRetries {
					backoffMs := 500 * (1 << rstStreamRetries)
					rstStreamRetries++
					logging.Warn("OpenAI stream reset by peer (HTTP/2 RST_STREAM), will retry on a fresh connection",
						"attempt", rstStreamRetries, "max", maxRSTStreamRetries, "error", err)
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
				if emittedOutput {
					logging.Warn("OpenAI stream reset by peer (HTTP/2 RST_STREAM) after partial output; not retrying to avoid duplicate content", "error", err)
				} else {
					logging.Warn("OpenAI stream reset by peer (HTTP/2 RST_STREAM); retry budget exhausted", "attempts", rstStreamRetries, "error", err)
				}
				eventChan <- ProviderEvent{Type: EventError, Error: err}
				close(eventChan)
				return
			}

			// If there is an error we are going to see if we can retry the call
			retry, after, retryErr := o.shouldRetry(attempts, err)
			if retryErr != nil {
				eventChan <- ProviderEvent{Type: EventError, Error: retryErr}
				close(eventChan)
				return
			}
			if retry {
				logging.WarnPersist(fmt.Sprintf("Retrying due to rate limit... attempt %d of %d", attempts, maxRetries), logging.PersistTimeArg, time.Millisecond*time.Duration(after+100))
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
			eventChan <- ProviderEvent{Type: EventError, Error: err}
			close(eventChan)
			return
		}
	}()

	return eventChan
}

func (o *openaiClient) applyMetadata(ctx context.Context, params *openai.ChatCompletionNewParams) {
	resolved := resolveMetadata(ctx, o.providerOptions.metadata)
	if resolved == nil {
		return
	}
	meta := make(shared.Metadata)
	for k, v := range resolved {
		switch val := v.(type) {
		case string:
			meta[k] = val
		case []string:
			meta[k] = strings.Join(val, ",")
		}
	}
	if len(meta) > 0 {
		params.Metadata = meta
	}
}

func (o *openaiClient) shouldRetry(attempts int, err error) (bool, int64, error) {
	var apierr *openai.Error
	if !errors.As(err, &apierr) {
		return false, 0, err
	}

	if apierr.StatusCode != 429 && apierr.StatusCode != 500 {
		return false, 0, err
	}

	if attempts > maxRetries {
		return false, 0, fmt.Errorf("maximum retry attempts reached for rate limit: %d retries", maxRetries)
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

func (o *openaiClient) toolCalls(completion openai.ChatCompletion) []message.ToolCall {
	var toolCalls []message.ToolCall

	if len(completion.Choices) > 0 && len(completion.Choices[0].Message.ToolCalls) > 0 {
		for _, call := range completion.Choices[0].Message.ToolCalls {
			input := call.Function.Arguments
			if strings.TrimSpace(input) == "" {
				input = "{}"
			}
			toolCall := message.ToolCall{
				ID:       call.ID,
				Name:     call.Function.Name,
				Input:    input,
				Type:     "function",
				Finished: true,
			}
			toolCalls = append(toolCalls, toolCall)
		}
	}

	return toolCalls
}

func (o *openaiClient) usage(completion openai.ChatCompletion) TokenUsage {
	cachedTokens := completion.Usage.PromptTokensDetails.CachedTokens
	inputTokens := completion.Usage.PromptTokens - cachedTokens

	return TokenUsage{
		InputTokens:         inputTokens,
		OutputTokens:        completion.Usage.CompletionTokens,
		CacheCreationTokens: 0, // OpenAI doesn't provide this directly
		CacheReadTokens:     cachedTokens,
	}
}

func (a *openaiClient) countTokens(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (int64, error) {
	return 0, fmt.Errorf("countTokens is unsupported by openai client: %w", errors.ErrUnsupported)
}

func (a *openaiClient) setMaxTokens(maxTokens int64) {
	a.providerOptions.maxTokens = maxTokens
}

func (a *openaiClient) maxTokens() int64 {
	return a.providerOptions.maxTokens
}

func WithOpenAIDisableCache() OpenAIOption {
	return func(options *openaiOptions) {
		options.disableCache = true
	}
}

func WithLegacyMaxTokens() OpenAIOption {
	return func(options *openaiOptions) {
		options.legacyMaxTokens = true
	}
}

func WithReasoningEffort(effort string) OpenAIOption {
	return func(options *openaiOptions) {
		defaultReasoningEffort := "medium"
		switch effort {
		case "low", "medium", "high":
			defaultReasoningEffort = effort
		default:
			logging.Warn("Invalid reasoning effort, using default: medium")
		}
		options.reasoningEffort = defaultReasoningEffort
	}
}

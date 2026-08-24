package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
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
	"github.com/tidwall/gjson"
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
	// countTokensServerErrors counts CONSECUTIVE 5xx answers from
	// count_tokens (reset on every success). Proxies in front of the real
	// endpoint reject content-block types their token counter does not
	// model — we rewrite the shapes we know about (see
	// stripServerToolBlocksForCountTokens and stripMediaForCountTokens), but
	// a block type added upstream would otherwise 500 once per agent-loop
	// iteration for the rest of the session. After
	// countTokensServerErrorLatchThreshold in a row we latch
	// countTokensUnsupported and stop probing.
	countTokensServerErrors atomic.Int64
}

// countTokensServerErrorLatchThreshold is how many consecutive 5xx answers
// from count_tokens it takes to give up on the endpoint for the session.
// Above 1 so a single blip (proxy restart, transient overload) does not cost
// the accurate count; low enough that a structurally-rejected request shape
// stops burning a round-trip per agent-loop iteration.
const countTokensServerErrorLatchThreshold = 3

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
				reasoning := msg.ReasoningParts()
				var searches []message.ToolSearchContent
				// Only the native path declares the server tool-search tool, so
				// server-search blocks are replayed only there. Replaying them
				// into a fallback-path request (e.g. after a mid-session switch
				// to a non-tool-search model of the same provider family) would
				// reference an undeclared server tool and 400.
				if a.providerOptions.model.SupportsToolSearch {
					searches = msg.ToolSearchParts()
				}

				emitReasoning := func(rc message.ReasoningContent) {
					// Replay reasoning blocks exactly as produced — the API
					// verifies each block's signature and rejects modified
					// blocks. Unsigned parts (legacy rows, streamed previews,
					// non-Anthropic sources) are skipped; redacted blocks carry
					// an opaque payload that round-trips verbatim.
					if rc.Redacted {
						if rc.Data != "" {
							blocks = append(blocks, anthropic.NewRedactedThinkingBlock(rc.Data))
						}
						return
					}
					if rc.Signature != "" {
						blocks = append(blocks, anthropic.NewThinkingBlock(rc.Signature, rc.Thinking))
					}
				}
				emitSearch := func(ts message.ToolSearchContent) {
					// Nothing to replay for a search that returned neither
					// references nor an error; an empty tool_references array is
					// also a rejectable shape on some backends. The discovered
					// tools stay activated+declared via SerializableFor, so
					// skipping such a search is safe.
					if !toolSearchEmittable(ts) {
						return
					}
					var inputMap map[string]any
					if err := json.Unmarshal([]byte(ts.Input), &inputMap); err != nil {
						inputMap = map[string]any{}
					}
					blocks = append(blocks, anthropic.NewServerToolUseBlock(ts.ToolUseID, inputMap, anthropic.ServerToolUseBlockParamName(ts.Name)))
					if ts.ErrorCode != "" {
						blocks = append(blocks, anthropic.NewToolSearchToolResultBlock(
							anthropic.ToolSearchToolResultErrorParam{ErrorCode: anthropic.ToolSearchToolResultErrorCode(ts.ErrorCode)},
							ts.ToolUseID,
						))
						return
					}
					refs := make([]anthropic.ToolReferenceBlockParam, 0, len(ts.References))
					for _, name := range ts.References {
						refs = append(refs, anthropic.ToolReferenceBlockParam{ToolName: name})
					}
					blocks = append(blocks, anthropic.NewToolSearchToolResultBlock(
						anthropic.ToolSearchToolSearchResultBlockParam{ToolReferences: refs},
						ts.ToolUseID,
					))
				}

				if len(searches) > 0 && toolSearchesReplayableInOrder(searches) {
					// Order-preserving replay: re-insert each search at its
					// captured index within the reasoning sequence (the model
					// interleaves thinking → search → thinking → tool_use), so
					// the thinking blocks are replayed in their original
					// positions and the API doesn't reject them as "modified".
					// This keeps the reasoning AND the discovered tools loaded.
					ordered := append([]message.ToolSearchContent(nil), searches...)
					sort.SliceStable(ordered, func(i, j int) bool {
						return *ordered[i].ReasoningOffset < *ordered[j].ReasoningOffset
					})
					si := 0
					for i := 0; i <= len(reasoning); i++ {
						for si < len(ordered) && *ordered[si].ReasoningOffset == i {
							emitSearch(ordered[si])
							si++
						}
						if i < len(reasoning) {
							emitReasoning(reasoning[i])
						}
					}
					for ; si < len(ordered); si++ {
						emitSearch(ordered[si])
					}
				} else {
					// Fallback: rows persisted before offsets were captured, so
					// we can't reproduce the exact thinking/search interleave. On
					// the native path a reconstructed search block would strand
					// this turn's thinking as "modified", so drop the reasoning
					// (it only forfeits reasoning continuity; the discovered
					// tools still replay via their references). Off the native
					// path, or with no search at all, replay reasoning normally.
					if !(a.providerOptions.model.SupportsToolSearch && len(searches) > 0) {
						for _, rc := range reasoning {
							emitReasoning(rc)
						}
					}
					for _, ts := range searches {
						emitSearch(ts)
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

// toolSearchRefsFromStartEvent parses a raw content_block_start payload for a
// tool_search_tool_result block and returns its tool_use_id plus the discovered
// tool names. The SDK's stream accumulator mistypes this block and drops its
// tool_references, so we recover them straight from the raw event.
func toolSearchRefsFromStartEvent(rawJSON string) (toolUseID string, names []string) {
	toolUseID = gjson.Get(rawJSON, "tool_use_id").String()
	for _, ref := range gjson.Get(rawJSON, "content.tool_references").Array() {
		if n := ref.Get("tool_name").String(); n != "" {
			names = append(names, n)
		}
	}
	return toolUseID, names
}

// applyStreamedToolSearchRefs backfills the tool-search references captured
// from the raw stream (keyed by tool_use_id) onto the parts whose references
// the SDK accumulator dropped. Parts that already carry references, or that
// carry an ErrorCode, are left untouched. Restoring the references lets the
// server-side search blocks replay faithfully, so the accompanying thinking
// blocks stay valid on the next request instead of having to be dropped.
func applyStreamedToolSearchRefs(parts []message.ToolSearchContent, streamed map[string][]string) []message.ToolSearchContent {
	if len(streamed) == 0 {
		return parts
	}
	for i := range parts {
		if len(parts[i].References) > 0 || parts[i].ErrorCode != "" {
			continue
		}
		if refs, ok := streamed[parts[i].ToolUseID]; ok {
			parts[i].References = refs
		}
	}
	return parts
}

// toolSearchEmittable reports whether a server-side search has something to
// replay: captured references or an error code. emitSearch drops a search with
// neither (an empty tool_references array is itself a rejectable shape on some
// backends), so this is the single source of truth for "will this search
// produce blocks". toolSearchesReplayableInOrder relies on it staying in lock
// step with emitSearch: a search the guard counts as emittable but emitSearch
// skips (or vice versa) would reopen the hole-in-the-thinking bug.
func toolSearchEmittable(ts message.ToolSearchContent) bool {
	return len(ts.References) > 0 || ts.ErrorCode != ""
}

// toolSearchesReplayableInOrder reports whether every server-side search on a
// turn can be replayed at its original position so the turn's thinking blocks
// can be kept in place. Two conditions must hold for ALL searches:
//
//   - a captured ReasoningOffset — the index (in the turn's reasoning sequence)
//     at which the model emitted the search — so we can re-insert it exactly
//     where it was; and
//   - something to emit there (toolSearchEmittable): captured references or an
//     error code. emitSearch drops a search that has neither, which would leave
//     the surrounding thinking blocks with a hole where the search used to be —
//     the very modification the API rejects.
//
// When both hold for every search we replay the thinking in place, unchanged,
// and the API accepts it. Otherwise (rows persisted before offsets/refs were
// captured, or any partial set) the exact interleave can't be reproduced: a
// reconstructed or missing server_tool_use / tool_search_tool_result block
// makes the latest assistant turn's thinking "modified", and the Bedrock/LiteLLM
// proxy surfaces that 400 as an HTTP/2 `RST_STREAM INTERNAL_ERROR`. The caller
// then falls back to dropping the reasoning; searches that do carry references
// still replay and keep their tools loaded for the session.
func toolSearchesReplayableInOrder(searches []message.ToolSearchContent) bool {
	for _, ts := range searches {
		if ts.ReasoningOffset == nil {
			return false
		}
		if !toolSearchEmittable(ts) {
			return false
		}
	}
	return true
}

func (a *anthropicClient) convertTools(ctx context.Context, tools []toolsPkg.BaseTool) []anthropic.ToolUnionParam {
	// Deferred-tool handling is path-dependent per model, decided fresh on
	// every request (the toolset slice itself is frozen per agent):
	//   - native (SupportsToolSearch): deferred tools ship full schemas with
	//     defer_loading:true (permanently — the API strips them from the
	//     cache key, so a stable flag means a stable prefix) and the GA
	//     server tool-search tool replaces the client-side toolsearch.
	//   - fallback (e.g. Kimi on this same client): non-activated deferred
	//     tools are omitted entirely; session-activated ones are appended
	//     AFTER the stable ordering, in activation order, so previously
	//     serialized tool positions never shift.
	native := a.providerOptions.model.SupportsToolSearch
	sessionID, _ := toolsPkg.GetContextValues(ctx)

	type activatedEntry struct {
		param anthropic.ToolUnionParam
		seq   int64
	}
	out := make([]anthropic.ToolUnionParam, 0, len(tools)+1)
	var activatedTail []activatedEntry
	hasDeferred := false

	for _, tool := range tools {
		info := tool.Info()
		if native && info.Name == toolsPkg.ToolSearchToolName {
			// The server tool owns discovery on the native path.
			continue
		}
		toolParam := anthropic.ToolParam{
			Name:        info.Name,
			Description: anthropic.String(info.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: info.Parameters,
				Required:   info.Required,
			},
		}
		w, isDeferred := tool.(*toolsPkg.DeferredWrapper)
		switch {
		case isDeferred && native:
			hasDeferred = true
			toolParam.DeferLoading = anthropic.Bool(true)
			out = append(out, anthropic.ToolUnionParam{OfTool: &toolParam})
		case isDeferred:
			hasDeferred = true
			if seq, ok := w.ActivatedAt(sessionID); ok {
				activatedTail = append(activatedTail, activatedEntry{anthropic.ToolUnionParam{OfTool: &toolParam}, seq})
			}
		default:
			out = append(out, anthropic.ToolUnionParam{OfTool: &toolParam})
		}
	}

	sort.Slice(activatedTail, func(i, j int) bool { return activatedTail[i].seq < activatedTail[j].seq })
	for _, e := range activatedTail {
		out = append(out, e.param)
	}

	if native && hasDeferred {
		out = append(out, anthropic.ToolUnionParam{
			OfToolSearchToolRegex20251119: &anthropic.ToolSearchToolRegex20251119Param{
				Type: anthropic.ToolSearchToolRegex20251119TypeToolSearchToolRegex20251119,
			},
		})
	}

	// Single cache breakpoint on the last entry that participates in the
	// rendered prefix — i.e. the last one WITHOUT defer_loading (deferred
	// entries are stripped from the prefix by the API, so a breakpoint on
	// one would silently vanish). On the native path that is the appended
	// server tool-search tool; without deferrals it is simply the last tool
	// (the pre-feature behavior).
	if !a.options.disableCache {
		for i := len(out) - 1; i >= 0; i-- {
			if st := out[i].OfToolSearchToolRegex20251119; st != nil {
				st.CacheControl = anthropic.NewCacheControlEphemeralParam()
				break
			}
			if t := out[i].OfTool; t != nil {
				if t.DeferLoading.Valid() && t.DeferLoading.Value {
					continue
				}
				t.CacheControl = anthropic.NewCacheControlEphemeralParam()
				break
			}
			break
		}
	}

	return out
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
	// forced != "" ⇒ the flow runner's forcing wrap-up turn wants this tool
	// forced via tool_choice. The Anthropic Messages API rejects a forced
	// tool_choice while extended thinking is enabled, so when forced we skip
	// the thinking-selection block below AND omit temperature: the skipped
	// block is also where adaptive-but-not-XHigh models (Claude 4.6, Kimi)
	// would set temperature=1, and a leftover Float(0) is a non-default value
	// Opus 4.7+ rejects. Omitting lets the API use its own default.
	forced := forcedTool(ctx)

	// TODO: parameterise temperature via agent config
	// Opus 4.7+ rejects non-default temperature values; omit to let the API use its default (1.0).
	temperature := anthropic.Float(0)
	if a.providerOptions.model.SupportsXHighThinking || forced != "" {
		temperature = param.Opt[float64]{}
	}
	if isUser && forced == "" {
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

	params := anthropic.MessageNewParams{
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
	// Forcing wrap-up turn: compel the model to call the named tool (e.g.
	// struct_output) on this single request. thinking/OutputConfig were left
	// unset above so the API accepts the forced choice.
	if forced != "" {
		params.ToolChoice = anthropic.ToolChoiceParamOfTool(forced)
	}
	return params
}

func (a *anthropicClient) send(ctx context.Context, messages []message.Message, tools []toolsPkg.BaseTool) (resposne *ProviderResponse, err error) {
	preparedMessages := a.preparedMessages(ctx, a.convertMessages(messages), a.convertTools(ctx, tools))
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
			Content:      sb.String(),
			ToolCalls:    a.toolCalls(*anthropicResponse),
			Reasoning:    a.reasoningParts(*anthropicResponse),
			ToolSearches: a.toolSearchParts(*anthropicResponse),
			Usage:        a.usage(*anthropicResponse),
		}, nil
	}
}

func (a *anthropicClient) stream(ctx context.Context, messages []message.Message, tools []toolsPkg.BaseTool) <-chan ProviderEvent {
	preparedMessages := a.preparedMessages(ctx, a.convertMessages(messages), a.convertTools(ctx, tools))
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
		// emittedOutput latches once any streamed content/thinking/tool event
		// has reached the consumer — a retry after that point would replay the
		// request and duplicate the assistant message (processEvent appends
		// every delta). rstStreamRetries is a dedicated budget for
		// peer-initiated HTTP/2 RST_STREAM resets, separate from attempts.
		emittedOutput := false
		rstStreamRetries := 0
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

			// streamedToolSearchRefs recovers the server-side tool-search
			// references the SDK's stream accumulator drops: it mistypes the
			// tool_search_tool_result content (as a web-search union) and
			// re-marshals tool_references away, so toolSearchParts reads them
			// back empty. We capture them verbatim from the raw
			// content_block_start event (keyed by tool_use_id) and merge them in
			// at stream completion, so discovered tools replay WITH their
			// references — which keeps the turn's thinking blocks faithfully
			// replayable instead of being dropped (see convertMessages /
			// messageHasUnreplayableToolSearch).
			streamedToolSearchRefs := map[string][]string{}

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
					emittedOutput = true
					if event.ContentBlock.Type == "tool_search_tool_result" {
						// Capture references before the SDK accumulator drops
						// them (see streamedToolSearchRefs). The raw start event
						// carries the full tool_search_tool_search_result content.
						if id, names := toolSearchRefsFromStartEvent(event.ContentBlock.RawJSON()); id != "" && len(names) > 0 {
							streamedToolSearchRefs[id] = names
						}
					}
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
					emittedOutput = true
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
							ToolSearches: applyStreamedToolSearchRefs(a.toolSearchParts(accumulatedMessage), streamedToolSearchRefs),
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
							ToolSearches: applyStreamedToolSearchRefs(a.toolSearchParts(accumulatedMessage), streamedToolSearchRefs),
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
					logging.Warn("Anthropic stream reset by peer (HTTP/2 RST_STREAM), will retry on a fresh connection",
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
					logging.Warn("Anthropic stream reset by peer (HTTP/2 RST_STREAM) after partial output; not retrying to avoid duplicate content", "error", err)
				} else {
					logging.Warn("Anthropic stream reset by peer (HTTP/2 RST_STREAM); retry budget exhausted", "attempts", rstStreamRetries, "error", err)
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

// toolSearchParts extracts server-side tool-search invocations from a
// response: each server_tool_use block for a tool-search variant paired
// with its tool_search_tool_result by tool_use_id. Persisted for replay
// (see message.ToolSearchContent) and used to activate deferred wrappers
// at discovery time.
func (a *anthropicClient) toolSearchParts(msg anthropic.Message) []message.ToolSearchContent {
	var parts []message.ToolSearchContent
	byUseID := map[string]int{}
	// reasoningSeen counts thinking/redacted_thinking blocks encountered so far,
	// in emission order — captured onto each search as ReasoningOffset so the
	// replay can restore the exact thinking/search interleave (reasoningParts
	// extracts the same blocks, in the same order, without filtering).
	reasoningSeen := 0
	for _, block := range msg.Content {
		switch variant := block.AsAny().(type) {
		case anthropic.ThinkingBlock, anthropic.RedactedThinkingBlock:
			reasoningSeen++
		case anthropic.ServerToolUseBlock:
			if !strings.HasPrefix(string(variant.Name), "tool_search_tool") {
				continue
			}
			input, err := json.Marshal(variant.Input)
			if err != nil {
				input = []byte("{}")
			}
			offset := reasoningSeen
			parts = append(parts, message.ToolSearchContent{
				ToolUseID:       variant.ID,
				Name:            string(variant.Name),
				Input:           string(input),
				ReasoningOffset: &offset,
			})
			byUseID[variant.ID] = len(parts) - 1
		case anthropic.ToolSearchToolResultBlock:
			idx, ok := byUseID[variant.ToolUseID]
			if !ok {
				continue
			}
			if variant.Content.ErrorCode != "" {
				parts[idx].ErrorCode = string(variant.Content.ErrorCode)
				continue
			}
			for _, ref := range variant.Content.ToolReferences {
				parts[idx].References = append(parts[idx].References, ref.ToolName)
			}
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

type forceStructOutputToolKeyType struct{}

// ForceStructOutputToolKey carries a non-empty tool name that the request
// builder must force via tool_choice for a single request. When present, the
// Anthropic client family (native Anthropic, AWS Bedrock, GCP Vertex, and
// Moonshot/Kimi — all share preparedMessages) forces that tool AND disables
// extended thinking + omits temperature for the request (the Anthropic API
// rejects a forced tool_choice while thinking is enabled). Best-effort:
// providers outside that family do not read the key and simply ignore it.
// Set by the flow runner's forcing wrap-up turn (via agent RunOptions).
var ForceStructOutputToolKey = forceStructOutputToolKeyType{}

// WithForcedTool returns a context that forces the named tool on the next
// provider request built from it. An empty name is a no-op.
func WithForcedTool(ctx context.Context, toolName string) context.Context {
	if toolName == "" {
		return ctx
	}
	return context.WithValue(ctx, ForceStructOutputToolKey, toolName)
}

// forcedTool reads the forced-tool name from ctx, or "" when unset.
func forcedTool(ctx context.Context) string {
	name, _ := ctx.Value(ForceStructOutputToolKey).(string)
	return name
}

// ForcedTool reports the forced-tool name carried on ctx (set by
// WithForcedTool), or "" when none is set. Exported so callers and tests
// outside this package can observe the forced-tool signal.
func ForcedTool(ctx context.Context) string {
	return forcedTool(ctx)
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
	// countTokensServerToolBlockOverhead approximates the JSON scaffolding
	// (type / id / tool_use_id / name framing) of a server-side tool block
	// that stripServerToolBlocksForCountTokens re-inlined as text. The
	// block's semantic payload — the search query, the discovered tool
	// names — is re-inlined verbatim and counted exactly by the endpoint, so
	// this covers only the structure the text stand-in drops.
	countTokensServerToolBlockOverhead = 10
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

// messagesContainServerToolBlocks reports whether any message holds a
// server-side tool block — server_tool_use or its paired
// tool_search_tool_result. Those only appear on the native tool-search path
// (see convertMessages), so this is false for the vast majority of
// conversations. Fast-path guard for stripServerToolBlocksForCountTokens.
func messagesContainServerToolBlocks(messages []anthropic.MessageParam) bool {
	for _, msg := range messages {
		for _, block := range msg.Content {
			if block.OfServerToolUse != nil || block.OfToolSearchToolResult != nil {
				return true
			}
		}
	}
	return false
}

// serverToolUseStandIn renders a server_tool_use block as text for the
// count_tokens call: the tool name plus its JSON input (the search query),
// which is the block's entire semantic payload. Re-inlining rather than
// eliding keeps the endpoint's count close to the real one — only the JSON
// scaffolding is lost, and the caller compensates with
// countTokensServerToolBlockOverhead.
func serverToolUseStandIn(b *anthropic.ServerToolUseBlockParam) string {
	input := "{}"
	if b.Input != nil {
		if raw, err := json.Marshal(b.Input); err == nil {
			input = string(raw)
		}
	}
	return fmt.Sprintf("[server tool %s %s]", b.Name, input)
}

// toolSearchResultStandIn renders a tool_search_tool_result block as text for
// the count_tokens call: either the error code, or the discovered tool names.
// The referenced tools' schemas are NOT part of this block — they are sent in
// the request's tools array (see SerializableFor) and counted there — so the
// names alone are the block's payload.
func toolSearchResultStandIn(b *anthropic.ToolSearchToolResultBlockParam) string {
	if errBlock := b.Content.OfRequestToolSearchToolResultError; errBlock != nil {
		return fmt.Sprintf("[tool search error %s]", errBlock.ErrorCode)
	}
	if res := b.Content.OfRequestToolSearchToolSearchResultBlock; res != nil {
		names := make([]string, 0, len(res.ToolReferences))
		for _, ref := range res.ToolReferences {
			names = append(names, ref.ToolName)
		}
		return fmt.Sprintf("[tool search results %s]", strings.Join(names, ","))
	}
	return "[tool search results]"
}

// stripServerToolBlocksForCountTokens returns a copy of messages with every
// server-side tool block — server_tool_use and its paired
// tool_search_tool_result — re-inlined as text, plus the token estimate
// compensating for the JSON scaffolding the swap drops.
//
// Once a session runs a native server-side tool search, convertMessages
// replays those blocks on EVERY subsequent request (mandatory — dropping them
// would strand the turn's signed thinking blocks as "modified"). Anthropic's
// own count_tokens models them, but proxies in front of it generally do not:
// LiteLLM's token counter accepts only text / image_url / tool_use /
// tool_result / thinking / tool_reference content items and answers HTTP 500
// with "Invalid content item type: server_tool_use" for the rest, which would
// cost a failed round-trip per agent-loop iteration for the rest of the
// session.
//
// Unlike stripMediaForCountTokens this runs on every path, not just Bedrock.
// The blocks' entire payload — the search query, the discovered tool names —
// is re-inlined verbatim and still counted exactly by the endpoint, so the
// accuracy given up is a few tokens of framing per block; that is far cheaper
// than guessing which deployments sit behind a proxy and being wrong.
//
// Fast path: conversations without a server-side search (the vast majority)
// get their input slice back unchanged, with no allocations.
func stripServerToolBlocksForCountTokens(messages []anthropic.MessageParam) ([]anthropic.MessageParam, int64) {
	if !messagesContainServerToolBlocks(messages) {
		return messages, 0
	}
	var extraTokens int64
	out := make([]anthropic.MessageParam, len(messages))
	for i, msg := range messages {
		newContent := make([]anthropic.ContentBlockParamUnion, 0, len(msg.Content))
		for _, block := range msg.Content {
			switch {
			case block.OfServerToolUse != nil:
				extraTokens += countTokensServerToolBlockOverhead
				newContent = append(newContent, anthropic.NewTextBlock(serverToolUseStandIn(block.OfServerToolUse)))
			case block.OfToolSearchToolResult != nil:
				extraTokens += countTokensServerToolBlockOverhead
				newContent = append(newContent, anthropic.NewTextBlock(toolSearchResultStandIn(block.OfToolSearchToolResult)))
			default:
				newContent = append(newContent, block)
			}
		}
		out[i] = anthropic.MessageParam{Role: msg.Role, Content: newContent}
	}
	return out, extraTokens
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
// Bedrock-only: the per-image estimate is a coarse stand-in for what a real
// endpoint counts exactly, so we pay it only where the proxy forces us to.
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
		return 0, fmt.Errorf("count_tokens previously latched as unusable on this endpoint: %w", errors.ErrUnsupported)
	}
	anthropicMessages := a.convertMessages(messages)
	// Server-side tool blocks go on every path: any Anthropic-dialect proxy
	// (LiteLLM in front of Bedrock OR Vertex, and third-party endpoints) 500s
	// on them, and re-inlining costs only a few tokens of framing.
	anthropicMessages, strippedTokenEstimate := stripServerToolBlocksForCountTokens(anthropicMessages)
	// Media stripping stays Bedrock-only: there the swap trades an exact
	// count for a coarse per-image estimate, so we pay it only where the
	// proxy leaves no choice. Native Anthropic and Vertex count images and
	// documents accurately.
	if a.options.useBedrock {
		stripped, mediaTokenEstimate := stripMediaForCountTokens(anthropicMessages)
		anthropicMessages = stripped
		strippedTokenEstimate += mediaTokenEstimate
	}
	anthropicTools := a.convertTools(ctx, tools)
	countTools := make([]anthropic.MessageCountTokensToolUnionParam, 0, len(anthropicTools))
	for _, t := range anthropicTools {
		// Map every union member we emit; an unmapped entry would serialize
		// as an empty object and 400 the count_tokens call.
		switch {
		case t.OfTool != nil:
			countTools = append(countTools, anthropic.MessageCountTokensToolUnionParam{OfTool: t.OfTool})
		case t.OfToolSearchToolRegex20251119 != nil:
			countTools = append(countTools, anthropic.MessageCountTokensToolUnionParam{OfToolSearchToolRegex20251119: t.OfToolSearchToolRegex20251119})
		}
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
		// A 5xx usually means the proxy's token counter choked on a content
		// block it does not model. Retrying the identical shape every
		// iteration cannot succeed, so latch after a few in a row.
		if errors.As(err, &apierr) && apierr.StatusCode >= http.StatusInternalServerError {
			if a.countTokensServerErrors.Add(1) >= countTokensServerErrorLatchThreshold {
				a.countTokensUnsupported.Store(true)
				logging.Info("count_tokens endpoint failed repeatedly; using local estimation for the rest of the session",
					"model", a.providerOptions.model.Name,
					"status", apierr.StatusCode,
					"consecutive_failures", countTokensServerErrorLatchThreshold,
					"cause", err.Error(),
				)
				return 0, fmt.Errorf("count_tokens endpoint failed %d times in a row (HTTP %d): %w", countTokensServerErrorLatchThreshold, apierr.StatusCode, errors.ErrUnsupported)
			}
		}
		return 0, fmt.Errorf("failed to count tokens: %w", err)
	}
	a.countTokensServerErrors.Store(0)

	return response.InputTokens + strippedTokenEstimate, nil
}

func (a *anthropicClient) setMaxTokens(maxTokens int64) {
	a.providerOptions.maxTokens = maxTokens
}

func (a *anthropicClient) maxTokens() int64 {
	return a.providerOptions.maxTokens
}

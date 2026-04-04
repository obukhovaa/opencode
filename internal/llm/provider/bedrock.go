package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/bedrock"
	sdkoption "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type bedrockOptions struct {
	// Bedrock specific options can be added here
}

type BedrockOption func(*bedrockOptions)

type bedrockClient struct {
	providerOptions providerClientOptions
	options         bedrockOptions
	childProvider   ProviderClient
}

type BedrockClient ProviderClient

func newBedrockClient(opts providerClientOptions) BedrockClient {
	bedrockOpts := bedrockOptions{}

	for k := range models.BedrockAnthropicModels {
		if k == opts.model.ID {
			// Create Anthropic client with Bedrock configuration
			anthropicOpts := opts
			anthropicOpts.anthropicOptions = append(anthropicOpts.anthropicOptions,
				WithAnthropicBedrock(true),
			)
			return &bedrockClient{
				providerOptions: opts,
				options:         bedrockOpts,
				childProvider:   newAnthropicClient(anthropicOpts),
			}
		}
	}

	// Return client with nil childProvider if model is not supported
	// This will cause an error when used
	return &bedrockClient{
		providerOptions: opts,
		options:         bedrockOpts,
		childProvider:   nil,
	}
}

func (b *bedrockClient) send(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (*ProviderResponse, error) {
	if b.childProvider == nil {
		return nil, errors.New("unsupported model for bedrock provider")
	}
	return b.childProvider.send(ctx, messages, tools)
}

func (b *bedrockClient) stream(ctx context.Context, messages []message.Message, tools []tools.BaseTool) <-chan ProviderEvent {
	eventChan := make(chan ProviderEvent)

	if b.childProvider == nil {
		go func() {
			eventChan <- ProviderEvent{
				Type:  EventError,
				Error: errors.New("unsupported model for bedrock provider"),
			}
			close(eventChan)
		}()
		return eventChan
	}

	return b.childProvider.stream(ctx, messages, tools)
}

func (b *bedrockClient) countTokens(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (int64, error) {
	if b.childProvider == nil {
		return 0, errors.New("unsupported model for bedrock provider")
	}
	return b.childProvider.countTokens(ctx, messages, tools)
}

func (a *bedrockClient) setMaxTokens(maxTokens int64) {
	a.providerOptions.maxTokens = maxTokens
}

func (a *bedrockClient) maxTokens() int64 {
	return a.providerOptions.maxTokens
}

// bedrockMiddleware transforms Anthropic API requests into the Bedrock URL
// format (/model/{modelId}/{action}). Unlike the SDK's built-in
// bedrock.WithConfig which uses exact path matching, this uses suffix
// matching so it works with proxy base URLs that have a path prefix
// (e.g. https://proxy.example.com/bedrock/v1/messages).
//
// The /v1/messages endpoint is transformed to Bedrock invoke format.
// The /v1/messages/count_tokens endpoint is rewritten to strip any proxy
// path prefix (e.g. /bedrock/v1/messages/count_tokens → /v1/messages/count_tokens)
// so it hits the proxy's Anthropic-compatible token counting endpoint.
func bedrockMiddleware() sdkoption.Middleware {
	return func(r *http.Request, next sdkoption.MiddlewareNext) (*http.Response, error) {
		if r.Body == nil || r.Method != http.MethodPost {
			return next(r)
		}

		// Handle count_tokens: strip any proxy path prefix so the request
		// reaches the proxy's /v1/messages/count_tokens endpoint instead
		// of the bedrock passthrough which doesn't support it.
		if strings.HasSuffix(r.URL.Path, "/v1/messages/count_tokens") {
			r.URL.Path = "/v1/messages/count_tokens"
			r.URL.RawPath = "/v1/messages/count_tokens"
			logging.Debug("bedrock middleware count_tokens request",
				"path", r.URL.Path, "method", r.Method,
			)
			return next(r)
		}

		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			return next(r)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body.Close()

		if !gjson.GetBytes(body, "anthropic_version").Exists() {
			body, _ = sjson.SetBytes(body, "anthropic_version", bedrock.DefaultVersion)
		}

		betas := r.Header.Values("anthropic-beta")
		if len(betas) > 0 {
			r.Header.Del("anthropic-beta")
			body, _ = sjson.SetBytes(body, "anthropic_beta", betas)
		}

		model := gjson.GetBytes(body, "model").String()
		stream := gjson.GetBytes(body, "stream").Bool()

		body, _ = sjson.DeleteBytes(body, "model")
		body, _ = sjson.DeleteBytes(body, "stream")

		method := "invoke"
		if stream {
			method = "invoke-with-response-stream"
		}

		newPath := fmt.Sprintf("/model/%s/%s", model, method)
		r.URL.Path = strings.ReplaceAll(r.URL.Path, "/v1/messages", newPath)
		r.URL.RawPath = strings.ReplaceAll(r.URL.Path, model, url.QueryEscape(model))

		logging.Debug("bedrock middleware request",
			"model", model, "stream", stream, "path", r.URL.Path, "method", r.Method,
		)

		reader := bytes.NewReader(body)
		r.Body = io.NopCloser(reader)
		r.GetBody = func() (io.ReadCloser, error) {
			_, err := reader.Seek(0, 0)
			return io.NopCloser(reader), err
		}
		r.ContentLength = int64(len(body))

		res, err := next(r)
		if err != nil || res == nil {
			return res, err
		}

		// The SDK's ssestream.NewDecoder selects the decoder via exact
		// string match on Content-Type. Bedrock returns
		// "application/vnd.amazon.eventstream" but proxies may add
		// parameters or change casing, breaking the lookup. Normalize
		// so the registered EventStream decoder is found.
		if stream {
			ct := res.Header.Get("Content-Type")
			logging.Debug("bedrock middleware response",
				"content_type", ct, "status", res.StatusCode,
			)
			ctLower := strings.ToLower(ct)
			if ct == "" || strings.HasPrefix(ctLower, "application/vnd.amazon.eventstream") {
				res.Header.Set("Content-Type", "application/vnd.amazon.eventstream")
			}
		}

		return res, nil
	}
}

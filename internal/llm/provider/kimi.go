package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/opencode-ai/opencode/internal/message"
)

const (
	defaultKimiBaseURL         = "https://api.moonshot.ai/anthropic"
	maxKimiTokenResponseBytes  = 1 << 20
	maxKimiTokenErrorBodyBytes = 4 << 10
)

type kimiTokenMessage struct {
	Role    message.MessageRole `json:"role"`
	Content any                 `json:"content"`
}

type kimiTokenContent struct {
	Type     string             `json:"type"`
	Text     string             `json:"text,omitempty"`
	ImageURL *kimiTokenImageURL `json:"image_url,omitempty"`
}

type kimiTokenImageURL struct {
	URL string `json:"url"`
}

type kimiTokenCountRequest struct {
	Model    string             `json:"model"`
	Messages []kimiTokenMessage `json:"messages"`
}

type kimiTokenCountResponse struct {
	Data struct {
		TotalTokens int64 `json:"total_tokens"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// kimiTokenCountEndpoint resolves Moonshot's chat-completions token estimator
// beside the configured Anthropic-compatible chat endpoint. Keeping the same
// origin and base path makes baseURL overrides work for compatible proxies;
// endpoints that do not expose the estimator degrade to local estimation.
func kimiTokenCountEndpoint(baseURL string) (string, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultKimiBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse Kimi base URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("parse Kimi base URL: absolute URL required, got %q", baseURL)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.Path = strings.TrimSuffix(u.Path, "/anthropic")
	u.Path += "/v1/tokenizers/estimate-token-count"
	u.RawPath = ""
	return u.String(), nil
}

func kimiTextContent(text string) kimiTokenContent {
	return kimiTokenContent{Type: "text", Text: text}
}

func kimiImageContent(mimeType string, data []byte) kimiTokenContent {
	return kimiTokenContent{
		Type: "image_url",
		ImageURL: &kimiTokenImageURL{
			URL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data),
		},
	}
}

// kimiBinaryContent converts an attachment to the estimator's OpenAI-style
// content parts. Reusing the Anthropic converter's no-documents mode keeps the
// PDF, text and unsupported-media placeholders identical to the chat request.
func kimiBinaryContent(bc message.BinaryContent) kimiTokenContent {
	block := convertBinaryContent(bc, false)
	if block.OfText != nil {
		return kimiTextContent(block.OfText.Text)
	}
	actual, err := checkImage(bc.Data)
	if err != nil {
		// convertBinaryContent has already handled this case; keep a defensive
		// fallback here so a future new block type cannot produce empty content.
		return kimiTextContent(invalidImageNote(bc, err))
	}
	return kimiImageContent(actual, bc.Data)
}

func kimiToolResultImage(result message.ToolResult) (kimiTokenContent, bool) {
	var imageData struct {
		Data string `json:"data"`
	}
	if json.Unmarshal([]byte(result.Content), &imageData) != nil {
		return kimiTokenContent{}, false
	}
	raw, err := base64.StdEncoding.DecodeString(imageData.Data)
	if err != nil {
		return kimiTokenContent{}, false
	}
	mimeType, err := checkImage(raw)
	if err != nil {
		return kimiTokenContent{}, false
	}
	return kimiImageContent(mimeType, raw), true
}

// kimiTokenMessages renders history in the estimate endpoint's documented
// shape. It has no tools or tool_calls fields: tool schemas remain covered by
// baseProvider's local-estimate floor, while tool calls and results are
// included as message content so large agentic histories are not omitted.
func (a *anthropicClient) kimiTokenMessages(messages []message.Message) []kimiTokenMessage {
	out := make([]kimiTokenMessage, 0, len(messages)+1)
	if a.providerOptions.systemMessage != "" {
		out = append(out, kimiTokenMessage{Role: message.System, Content: a.providerOptions.systemMessage})
	}
	for i := range messages {
		msg := &messages[i]
		switch msg.Role {
		case message.System:
			if msg.Content().String() != "" {
				out = append(out, kimiTokenMessage{Role: message.System, Content: msg.Content().String()})
			}
		case message.User:
			parts := make([]kimiTokenContent, 0, 1+len(msg.BinaryContent()))
			if text := msg.Content().String(); strings.TrimSpace(text) != "" {
				parts = append(parts, kimiTextContent(text))
			}
			for _, image := range msg.ImageURLContent() {
				parts = append(parts, kimiTokenContent{Type: "image_url", ImageURL: &kimiTokenImageURL{URL: image.URL}})
			}
			for _, binary := range msg.BinaryContent() {
				parts = append(parts, kimiBinaryContent(binary))
			}
			if len(parts) == 0 {
				continue
			}
			content := any(parts)
			if len(parts) == 1 && parts[0].Type == "text" {
				content = parts[0].Text
			}
			out = append(out, kimiTokenMessage{Role: message.User, Content: content})
		case message.Assistant:
			var text strings.Builder
			if a.shouldReplayReasoning(*msg) {
				for _, reasoning := range msg.ReasoningParts() {
					text.WriteString(reasoning.Thinking)
					text.WriteString(reasoning.Data)
				}
			}
			text.WriteString(msg.Content().String())
			for _, call := range msg.ToolCalls() {
				encoded, _ := json.Marshal(call)
				text.Write(encoded)
			}
			if text.Len() == 0 {
				continue
			}
			out = append(out, kimiTokenMessage{Role: message.Assistant, Content: text.String()})
		case message.Tool:
			for _, result := range msg.ToolResults() {
				if result.IsImageToolResponse() {
					if image, ok := kimiToolResultImage(result); ok {
						out = append(out, kimiTokenMessage{Role: message.Tool, Content: []kimiTokenContent{image}})
						continue
					}
				}
				if result.Content != "" {
					out = append(out, kimiTokenMessage{Role: message.Tool, Content: result.Content})
				}
			}
		}
	}
	return out
}

func (a *anthropicClient) countKimiTokens(ctx context.Context, messages []message.Message) (int64, error) {
	if a.countTokensUnsupported.Load() {
		return 0, fmt.Errorf("Kimi token estimator previously answered 404/405 on this endpoint: %w", errors.ErrUnsupported)
	}
	endpoint, err := kimiTokenCountEndpoint(a.providerOptions.baseURL)
	if err != nil {
		return 0, fmt.Errorf("failed to count tokens: %w", err)
	}
	payload, err := json.Marshal(kimiTokenCountRequest{
		Model:    a.providerOptions.model.APIModel,
		Messages: a.kimiTokenMessages(messages),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to marshal Kimi token estimate request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("failed to create Kimi token estimate request: %w", err)
	}
	for key, value := range a.providerOptions.headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", "application/json")
	if a.providerOptions.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.providerOptions.apiKey)
	}
	httpClient := http.DefaultClient
	if a.conns != nil {
		httpClient = a.conns.Client()
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to count tokens: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxKimiTokenErrorBodyBytes))
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
			a.countTokensUnsupported.Store(true)
			return 0, fmt.Errorf("Kimi token estimator not implemented (HTTP %d): %w", resp.StatusCode, errors.ErrUnsupported)
		}
		return 0, fmt.Errorf("failed to count tokens: POST %q: %s %s", endpoint, resp.Status, strings.TrimSpace(string(body)))
	}
	var decoded kimiTokenCountResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxKimiTokenResponseBytes)).Decode(&decoded); err != nil {
		return 0, fmt.Errorf("failed to decode Kimi token estimate: %w", err)
	}
	if decoded.Error != nil {
		return 0, fmt.Errorf("failed to count tokens: %s (%s)", decoded.Error.Message, decoded.Error.Type)
	}
	return decoded.Data.TotalTokens, nil
}

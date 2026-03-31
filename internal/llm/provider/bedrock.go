package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
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

func (a *bedrockClient) countTokens(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (int64, error) {
	return 0, fmt.Errorf("countTokens is unsupported by bedrock client: %w", errors.ErrUnsupported)
}

func (a *bedrockClient) setMaxTokens(maxTokens int64) {
	a.providerOptions.maxTokens = maxTokens
}

func (a *bedrockClient) maxTokens() int64 {
	return a.providerOptions.maxTokens
}

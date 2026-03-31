package models

const (
	ProviderBedrock   ModelProvider = "bedrock"
	BedrockEUOpus46   ModelID       = "bedrock.eu-claude-opus-4-6"
	BedrockEUSonnet46 ModelID       = "bedrock.eu-claude-sonnet-4-6"
)

var BedrockAnthropicModels = map[ModelID]Model{
	BedrockEUOpus46: {
		ID:                       BedrockEUOpus46,
		Name:                     "Bedrock EU: Claude 4.6 Opus",
		Provider:                 ProviderBedrock,
		APIModel:                 "eu.anthropic.claude-opus-4-6-v1",
		CostPer1MIn:              AnthropicModels[Claude46Opus].CostPer1MIn,
		CostPer1MInCached:        AnthropicModels[Claude46Opus].CostPer1MInCached,
		CostPer1MOut:             AnthropicModels[Claude46Opus].CostPer1MOut,
		CostPer1MOutCached:       AnthropicModels[Claude46Opus].CostPer1MOutCached,
		ContextWindow:            AnthropicModels[Claude46Opus].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude46Opus].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude46Opus].SupportsAttachments,
		CanReason:                AnthropicModels[Claude46Opus].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude46Opus].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[Claude46Opus].SupportsMaximumThinking,
	},
	BedrockEUSonnet46: {
		ID:                  BedrockEUSonnet46,
		Name:                "Bedrock EU: Claude 4.6 Sonnet",
		Provider:            ProviderBedrock,
		APIModel:            "eu.anthropic.claude-sonnet-4-6",
		CostPer1MIn:         AnthropicModels[Claude46Sonnet].CostPer1MIn,
		CostPer1MInCached:   AnthropicModels[Claude46Sonnet].CostPer1MInCached,
		CostPer1MOut:        AnthropicModels[Claude46Sonnet].CostPer1MOut,
		CostPer1MOutCached:  AnthropicModels[Claude46Sonnet].CostPer1MOutCached,
		ContextWindow:       AnthropicModels[Claude46Sonnet].ContextWindow,
		DefaultMaxTokens:    AnthropicModels[Claude46Sonnet].DefaultMaxTokens,
		SupportsAttachments: AnthropicModels[Claude46Sonnet].SupportsAttachments,
		CanReason:           AnthropicModels[Claude46Sonnet].CanReason,
	},
}

package models

const (
	ProviderBedrock   ModelProvider = "bedrock"
	BedrockEUOpus46   ModelID       = "bedrock.eu-claude-opus-4-6"
	BedrockEUOpus47   ModelID       = "bedrock.eu-claude-opus-4-7"
	BedrockEUOpus48   ModelID       = "bedrock.eu-claude-opus-4-8"
	BedrockEUOpus5    ModelID       = "bedrock.eu-claude-opus-5"
	BedrockEUOpus55   ModelID       = "bedrock.eu-claude-opus-5-5"
	BedrockEUSonnet46 ModelID       = "bedrock.eu-claude-sonnet-4-6"
	BedrockEUSonnet5  ModelID       = "bedrock.eu-claude-sonnet-5"
	BedrockEUSonnet55 ModelID       = "bedrock.eu-claude-sonnet-5-5"
	BedrockEUFable5   ModelID       = "bedrock.eu-claude-fable-5"
	BedrockEUFable51  ModelID       = "bedrock.eu-claude-fable-5-1"
	BedrockOpus46     ModelID       = "bedrock.claude-opus-4-6"
	BedrockOpus47     ModelID       = "bedrock.claude-opus-4-7"
	BedrockOpus48     ModelID       = "bedrock.claude-opus-4-8"
	BedrockOpus5      ModelID       = "bedrock.claude-opus-5"
	BedrockOpus55     ModelID       = "bedrock.claude-opus-5-5"
	BedrockSonnet46   ModelID       = "bedrock.claude-sonnet-4-6"
	BedrockSonnet5    ModelID       = "bedrock.claude-sonnet-5"
	BedrockSonnet55   ModelID       = "bedrock.claude-sonnet-5-5"
	BedrockFable5     ModelID       = "bedrock.claude-fable-5"
	BedrockFable51    ModelID       = "bedrock.claude-fable-5-1"
	BedrockEUHaiku45  ModelID       = "bedrock.eu-claude-haiku-4-5"
	BedrockHaiku45    ModelID       = "bedrock.claude-haiku-4-5"
	BedrockEUHaiku55  ModelID       = "bedrock.eu-claude-haiku-5-5"
	BedrockHaiku55    ModelID       = "bedrock.claude-haiku-5-5"
	BedrockKimiK3     ModelID       = "bedrock.kimi-k3"
	BedrockUSKimiK3   ModelID       = "bedrock.us-kimi-k3"
)

const (
	BedrockKimiK31      ModelID = "bedrock.kimi-k3.1"
	BedrockUSKimiK31    ModelID = "bedrock.us-kimi-k3.1"
	BedrockGPT61Sol     ModelID = "bedrock.gpt-6.1-sol"
	BedrockUSGPT61Sol   ModelID = "bedrock.us-gpt-6.1-sol"
	BedrockGPT61Terra   ModelID = "bedrock.gpt-6.1-terra"
	BedrockUSGPT61Terra ModelID = "bedrock.us-gpt-6.1-terra"
)

// bedrockRegionalPremium is the 10% surcharge Bedrock applies to regional
// (geo-pinned, e.g. EU) endpoints over the global endpoint, for Claude 4.5
// and later models. Global entries carry the base Anthropic rates, and so
// does BedrockEUOpus46, whose APIModel is the global alias.
const bedrockRegionalPremium = 1.1

var BedrockAnthropicModels = map[ModelID]Model{
	BedrockEUOpus46: {
		ID:                       BedrockEUOpus46,
		Name:                     "Bedrock EU: Claude 4.6 Opus",
		Provider:                 ProviderBedrock,
		APIModel:                 "claude-opus-4-6",
		CostPer1MIn:              AnthropicModels[Claude46Opus].CostPer1MIn,
		CostPer1MInCached:        AnthropicModels[Claude46Opus].CostPer1MInCached,
		CostPer1MOut:             AnthropicModels[Claude46Opus].CostPer1MOut,
		CostPer1MOutCached:       AnthropicModels[Claude46Opus].CostPer1MOutCached,
		ContextWindow:            AnthropicModels[Claude46Opus].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude46Opus].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude46Opus].SupportsAttachments,
		SupportsToolSearch:       AnthropicModels[Claude46Opus].SupportsToolSearch,
		CanReason:                AnthropicModels[Claude46Opus].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude46Opus].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[Claude46Opus].SupportsMaximumThinking,
	},
	BedrockEUOpus47: {
		ID:                       BedrockEUOpus47,
		Name:                     "Bedrock EU: Claude 4.7 Opus",
		Provider:                 ProviderBedrock,
		APIModel:                 "eu-claude-opus-4-7",
		CostPer1MIn:              AnthropicModels[Claude47Opus].CostPer1MIn * bedrockRegionalPremium,
		CostPer1MInCached:        AnthropicModels[Claude47Opus].CostPer1MInCached * bedrockRegionalPremium,
		CostPer1MOut:             AnthropicModels[Claude47Opus].CostPer1MOut * bedrockRegionalPremium,
		CostPer1MOutCached:       AnthropicModels[Claude47Opus].CostPer1MOutCached * bedrockRegionalPremium,
		ContextWindow:            AnthropicModels[Claude47Opus].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude47Opus].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude47Opus].SupportsAttachments,
		CanReason:                AnthropicModels[Claude47Opus].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude47Opus].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[Claude47Opus].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[Claude47Opus].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[Claude47Opus].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[Claude47Opus].SupportsToolSearch,
	},
	BedrockEUOpus48: {
		ID:                       BedrockEUOpus48,
		Name:                     "Bedrock EU: Claude 4.8 Opus",
		Provider:                 ProviderBedrock,
		APIModel:                 "eu-claude-opus-4-8",
		CostPer1MIn:              AnthropicModels[Claude48Opus].CostPer1MIn * bedrockRegionalPremium,
		CostPer1MInCached:        AnthropicModels[Claude48Opus].CostPer1MInCached * bedrockRegionalPremium,
		CostPer1MOut:             AnthropicModels[Claude48Opus].CostPer1MOut * bedrockRegionalPremium,
		CostPer1MOutCached:       AnthropicModels[Claude48Opus].CostPer1MOutCached * bedrockRegionalPremium,
		ContextWindow:            AnthropicModels[Claude48Opus].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude48Opus].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude48Opus].SupportsAttachments,
		CanReason:                AnthropicModels[Claude48Opus].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude48Opus].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[Claude48Opus].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[Claude48Opus].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[Claude48Opus].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[Claude48Opus].SupportsToolSearch,
	},
	BedrockEUOpus5: {
		ID:                       BedrockEUOpus5,
		Name:                     "Bedrock EU: Claude 5 Opus",
		Provider:                 ProviderBedrock,
		APIModel:                 "eu-claude-opus-5",
		CostPer1MIn:              AnthropicModels[Claude5Opus].CostPer1MIn * bedrockRegionalPremium,
		CostPer1MInCached:        AnthropicModels[Claude5Opus].CostPer1MInCached * bedrockRegionalPremium,
		CostPer1MOut:             AnthropicModels[Claude5Opus].CostPer1MOut * bedrockRegionalPremium,
		CostPer1MOutCached:       AnthropicModels[Claude5Opus].CostPer1MOutCached * bedrockRegionalPremium,
		ContextWindow:            AnthropicModels[Claude5Opus].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude5Opus].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude5Opus].SupportsAttachments,
		CanReason:                AnthropicModels[Claude5Opus].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude5Opus].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[Claude5Opus].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[Claude5Opus].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[Claude5Opus].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[Claude5Opus].SupportsToolSearch,
	},
	BedrockEUOpus55: {
		ID:                       BedrockEUOpus55,
		Name:                     "Bedrock EU: Claude 5.5 Opus",
		Provider:                 ProviderBedrock,
		APIModel:                 "eu-claude-opus-5-5",
		CostPer1MIn:              AnthropicModels[Claude55Opus].CostPer1MIn * bedrockRegionalPremium,
		CostPer1MInCached:        AnthropicModels[Claude55Opus].CostPer1MInCached * bedrockRegionalPremium,
		CostPer1MOut:             AnthropicModels[Claude55Opus].CostPer1MOut * bedrockRegionalPremium,
		CostPer1MOutCached:       AnthropicModels[Claude55Opus].CostPer1MOutCached * bedrockRegionalPremium,
		ContextWindow:            AnthropicModels[Claude55Opus].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude55Opus].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude55Opus].SupportsAttachments,
		CanReason:                AnthropicModels[Claude55Opus].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude55Opus].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[Claude55Opus].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[Claude55Opus].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[Claude55Opus].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[Claude55Opus].SupportsToolSearch,
		RejectsForcedToolChoice:  AnthropicModels[Claude55Opus].RejectsForcedToolChoice,
	},
	BedrockEUFable5: {
		ID:                       BedrockEUFable5,
		Name:                     "Bedrock EU: Claude Fable 5",
		Provider:                 ProviderBedrock,
		APIModel:                 "eu-claude-fable-5",
		CostPer1MIn:              AnthropicModels[ClaudeFable5].CostPer1MIn * bedrockRegionalPremium,
		CostPer1MInCached:        AnthropicModels[ClaudeFable5].CostPer1MInCached * bedrockRegionalPremium,
		CostPer1MOut:             AnthropicModels[ClaudeFable5].CostPer1MOut * bedrockRegionalPremium,
		CostPer1MOutCached:       AnthropicModels[ClaudeFable5].CostPer1MOutCached * bedrockRegionalPremium,
		ContextWindow:            AnthropicModels[ClaudeFable5].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[ClaudeFable5].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[ClaudeFable5].SupportsAttachments,
		CanReason:                AnthropicModels[ClaudeFable5].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[ClaudeFable5].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[ClaudeFable5].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[ClaudeFable5].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[ClaudeFable5].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[ClaudeFable5].SupportsToolSearch,
	},
	BedrockEUFable51: {
		ID:                       BedrockEUFable51,
		Name:                     "Bedrock EU: Claude Fable 5.1",
		Provider:                 ProviderBedrock,
		APIModel:                 "eu-claude-fable-5-1",
		CostPer1MIn:              AnthropicModels[ClaudeFable51].CostPer1MIn * bedrockRegionalPremium,
		CostPer1MInCached:        AnthropicModels[ClaudeFable51].CostPer1MInCached * bedrockRegionalPremium,
		CostPer1MOut:             AnthropicModels[ClaudeFable51].CostPer1MOut * bedrockRegionalPremium,
		CostPer1MOutCached:       AnthropicModels[ClaudeFable51].CostPer1MOutCached * bedrockRegionalPremium,
		ContextWindow:            AnthropicModels[ClaudeFable51].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[ClaudeFable51].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[ClaudeFable51].SupportsAttachments,
		CanReason:                AnthropicModels[ClaudeFable51].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[ClaudeFable51].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[ClaudeFable51].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[ClaudeFable51].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[ClaudeFable51].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[ClaudeFable51].SupportsToolSearch,
		RejectsForcedToolChoice:  AnthropicModels[ClaudeFable51].RejectsForcedToolChoice,
	},
	BedrockEUSonnet46: {
		ID:                       BedrockEUSonnet46,
		Name:                     "Bedrock EU: Claude 4.6 Sonnet",
		Provider:                 ProviderBedrock,
		APIModel:                 "eu-claude-sonnet-4-6",
		CostPer1MIn:              AnthropicModels[Claude46Sonnet].CostPer1MIn * bedrockRegionalPremium,
		CostPer1MInCached:        AnthropicModels[Claude46Sonnet].CostPer1MInCached * bedrockRegionalPremium,
		CostPer1MOut:             AnthropicModels[Claude46Sonnet].CostPer1MOut * bedrockRegionalPremium,
		CostPer1MOutCached:       AnthropicModels[Claude46Sonnet].CostPer1MOutCached * bedrockRegionalPremium,
		ContextWindow:            AnthropicModels[Claude46Sonnet].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude46Sonnet].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude46Sonnet].SupportsAttachments,
		SupportsToolSearch:       AnthropicModels[Claude46Sonnet].SupportsToolSearch,
		CanReason:                AnthropicModels[Claude46Sonnet].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude46Sonnet].SupportsAdaptiveThinking,
	},
	BedrockEUSonnet5: {
		ID:                       BedrockEUSonnet5,
		Name:                     "Bedrock EU: Claude 5 Sonnet",
		Provider:                 ProviderBedrock,
		APIModel:                 "eu-claude-sonnet-5",
		CostPer1MIn:              AnthropicModels[Claude5Sonnet].CostPer1MIn * bedrockRegionalPremium,
		CostPer1MInCached:        AnthropicModels[Claude5Sonnet].CostPer1MInCached * bedrockRegionalPremium,
		CostPer1MOut:             AnthropicModels[Claude5Sonnet].CostPer1MOut * bedrockRegionalPremium,
		CostPer1MOutCached:       AnthropicModels[Claude5Sonnet].CostPer1MOutCached * bedrockRegionalPremium,
		ContextWindow:            AnthropicModels[Claude5Sonnet].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude5Sonnet].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude5Sonnet].SupportsAttachments,
		CanReason:                AnthropicModels[Claude5Sonnet].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude5Sonnet].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[Claude5Sonnet].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[Claude5Sonnet].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[Claude5Sonnet].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[Claude5Sonnet].SupportsToolSearch,
	},
	BedrockEUSonnet55: {
		ID:                       BedrockEUSonnet55,
		Name:                     "Bedrock EU: Claude 5.5 Sonnet",
		Provider:                 ProviderBedrock,
		APIModel:                 "eu-claude-sonnet-5-5",
		CostPer1MIn:              AnthropicModels[Claude55Sonnet].CostPer1MIn * bedrockRegionalPremium,
		CostPer1MInCached:        AnthropicModels[Claude55Sonnet].CostPer1MInCached * bedrockRegionalPremium,
		CostPer1MOut:             AnthropicModels[Claude55Sonnet].CostPer1MOut * bedrockRegionalPremium,
		CostPer1MOutCached:       AnthropicModels[Claude55Sonnet].CostPer1MOutCached * bedrockRegionalPremium,
		ContextWindow:            AnthropicModels[Claude55Sonnet].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude55Sonnet].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude55Sonnet].SupportsAttachments,
		CanReason:                AnthropicModels[Claude55Sonnet].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude55Sonnet].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[Claude55Sonnet].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[Claude55Sonnet].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[Claude55Sonnet].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[Claude55Sonnet].SupportsToolSearch,
		RejectsForcedToolChoice:  AnthropicModels[Claude55Sonnet].RejectsForcedToolChoice,
	},
	BedrockOpus46: {
		ID:                       BedrockOpus46,
		Name:                     "Bedrock: Claude 4.6 Opus",
		Provider:                 ProviderBedrock,
		APIModel:                 "claude-opus-4-6",
		CostPer1MIn:              AnthropicModels[Claude46Opus].CostPer1MIn,
		CostPer1MInCached:        AnthropicModels[Claude46Opus].CostPer1MInCached,
		CostPer1MOut:             AnthropicModels[Claude46Opus].CostPer1MOut,
		CostPer1MOutCached:       AnthropicModels[Claude46Opus].CostPer1MOutCached,
		ContextWindow:            AnthropicModels[Claude46Opus].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude46Opus].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude46Opus].SupportsAttachments,
		SupportsToolSearch:       AnthropicModels[Claude46Opus].SupportsToolSearch,
		CanReason:                AnthropicModels[Claude46Opus].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude46Opus].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[Claude46Opus].SupportsMaximumThinking,
	},
	BedrockOpus47: {
		ID:                       BedrockOpus47,
		Name:                     "Bedrock: Claude 4.7 Opus",
		Provider:                 ProviderBedrock,
		APIModel:                 "claude-opus-4-7",
		CostPer1MIn:              AnthropicModels[Claude47Opus].CostPer1MIn,
		CostPer1MInCached:        AnthropicModels[Claude47Opus].CostPer1MInCached,
		CostPer1MOut:             AnthropicModels[Claude47Opus].CostPer1MOut,
		CostPer1MOutCached:       AnthropicModels[Claude47Opus].CostPer1MOutCached,
		ContextWindow:            AnthropicModels[Claude47Opus].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude47Opus].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude47Opus].SupportsAttachments,
		CanReason:                AnthropicModels[Claude47Opus].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude47Opus].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[Claude47Opus].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[Claude47Opus].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[Claude47Opus].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[Claude47Opus].SupportsToolSearch,
	},
	BedrockOpus48: {
		ID:                       BedrockOpus48,
		Name:                     "Bedrock: Claude 4.8 Opus",
		Provider:                 ProviderBedrock,
		APIModel:                 "claude-opus-4-8",
		CostPer1MIn:              AnthropicModels[Claude48Opus].CostPer1MIn,
		CostPer1MInCached:        AnthropicModels[Claude48Opus].CostPer1MInCached,
		CostPer1MOut:             AnthropicModels[Claude48Opus].CostPer1MOut,
		CostPer1MOutCached:       AnthropicModels[Claude48Opus].CostPer1MOutCached,
		ContextWindow:            AnthropicModels[Claude48Opus].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude48Opus].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude48Opus].SupportsAttachments,
		CanReason:                AnthropicModels[Claude48Opus].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude48Opus].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[Claude48Opus].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[Claude48Opus].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[Claude48Opus].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[Claude48Opus].SupportsToolSearch,
	},
	BedrockOpus5: {
		ID:                       BedrockOpus5,
		Name:                     "Bedrock: Claude 5 Opus",
		Provider:                 ProviderBedrock,
		APIModel:                 "claude-opus-5",
		CostPer1MIn:              AnthropicModels[Claude5Opus].CostPer1MIn,
		CostPer1MInCached:        AnthropicModels[Claude5Opus].CostPer1MInCached,
		CostPer1MOut:             AnthropicModels[Claude5Opus].CostPer1MOut,
		CostPer1MOutCached:       AnthropicModels[Claude5Opus].CostPer1MOutCached,
		ContextWindow:            AnthropicModels[Claude5Opus].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude5Opus].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude5Opus].SupportsAttachments,
		CanReason:                AnthropicModels[Claude5Opus].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude5Opus].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[Claude5Opus].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[Claude5Opus].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[Claude5Opus].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[Claude5Opus].SupportsToolSearch,
	},
	BedrockOpus55: {
		ID:                       BedrockOpus55,
		Name:                     "Bedrock: Claude 5.5 Opus",
		Provider:                 ProviderBedrock,
		APIModel:                 "claude-opus-5-5",
		CostPer1MIn:              AnthropicModels[Claude55Opus].CostPer1MIn,
		CostPer1MInCached:        AnthropicModels[Claude55Opus].CostPer1MInCached,
		CostPer1MOut:             AnthropicModels[Claude55Opus].CostPer1MOut,
		CostPer1MOutCached:       AnthropicModels[Claude55Opus].CostPer1MOutCached,
		ContextWindow:            AnthropicModels[Claude55Opus].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude55Opus].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude55Opus].SupportsAttachments,
		CanReason:                AnthropicModels[Claude55Opus].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude55Opus].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[Claude55Opus].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[Claude55Opus].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[Claude55Opus].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[Claude55Opus].SupportsToolSearch,
		RejectsForcedToolChoice:  AnthropicModels[Claude55Opus].RejectsForcedToolChoice,
	},
	BedrockFable5: {
		ID:                       BedrockFable5,
		Name:                     "Bedrock: Claude Fable 5",
		Provider:                 ProviderBedrock,
		APIModel:                 "claude-fable-5",
		CostPer1MIn:              AnthropicModels[ClaudeFable5].CostPer1MIn,
		CostPer1MInCached:        AnthropicModels[ClaudeFable5].CostPer1MInCached,
		CostPer1MOut:             AnthropicModels[ClaudeFable5].CostPer1MOut,
		CostPer1MOutCached:       AnthropicModels[ClaudeFable5].CostPer1MOutCached,
		ContextWindow:            AnthropicModels[ClaudeFable5].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[ClaudeFable5].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[ClaudeFable5].SupportsAttachments,
		CanReason:                AnthropicModels[ClaudeFable5].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[ClaudeFable5].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[ClaudeFable5].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[ClaudeFable5].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[ClaudeFable5].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[ClaudeFable5].SupportsToolSearch,
	},
	BedrockFable51: {
		ID:                       BedrockFable51,
		Name:                     "Bedrock: Claude Fable 5.1",
		Provider:                 ProviderBedrock,
		APIModel:                 "claude-fable-5-1",
		CostPer1MIn:              AnthropicModels[ClaudeFable51].CostPer1MIn,
		CostPer1MInCached:        AnthropicModels[ClaudeFable51].CostPer1MInCached,
		CostPer1MOut:             AnthropicModels[ClaudeFable51].CostPer1MOut,
		CostPer1MOutCached:       AnthropicModels[ClaudeFable51].CostPer1MOutCached,
		ContextWindow:            AnthropicModels[ClaudeFable51].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[ClaudeFable51].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[ClaudeFable51].SupportsAttachments,
		CanReason:                AnthropicModels[ClaudeFable51].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[ClaudeFable51].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[ClaudeFable51].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[ClaudeFable51].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[ClaudeFable51].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[ClaudeFable51].SupportsToolSearch,
		RejectsForcedToolChoice:  AnthropicModels[ClaudeFable51].RejectsForcedToolChoice,
	},
	BedrockSonnet46: {
		ID:                       BedrockSonnet46,
		Name:                     "Bedrock: Claude 4.6 Sonnet",
		Provider:                 ProviderBedrock,
		APIModel:                 "claude-sonnet-4-6",
		CostPer1MIn:              AnthropicModels[Claude46Sonnet].CostPer1MIn,
		CostPer1MInCached:        AnthropicModels[Claude46Sonnet].CostPer1MInCached,
		CostPer1MOut:             AnthropicModels[Claude46Sonnet].CostPer1MOut,
		CostPer1MOutCached:       AnthropicModels[Claude46Sonnet].CostPer1MOutCached,
		ContextWindow:            AnthropicModels[Claude46Sonnet].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude46Sonnet].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude46Sonnet].SupportsAttachments,
		SupportsToolSearch:       AnthropicModels[Claude46Sonnet].SupportsToolSearch,
		CanReason:                AnthropicModels[Claude46Sonnet].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude46Sonnet].SupportsAdaptiveThinking,
	},
	BedrockSonnet5: {
		ID:                       BedrockSonnet5,
		Name:                     "Bedrock: Claude 5 Sonnet",
		Provider:                 ProviderBedrock,
		APIModel:                 "claude-sonnet-5",
		CostPer1MIn:              AnthropicModels[Claude5Sonnet].CostPer1MIn,
		CostPer1MInCached:        AnthropicModels[Claude5Sonnet].CostPer1MInCached,
		CostPer1MOut:             AnthropicModels[Claude5Sonnet].CostPer1MOut,
		CostPer1MOutCached:       AnthropicModels[Claude5Sonnet].CostPer1MOutCached,
		ContextWindow:            AnthropicModels[Claude5Sonnet].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude5Sonnet].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude5Sonnet].SupportsAttachments,
		CanReason:                AnthropicModels[Claude5Sonnet].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude5Sonnet].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[Claude5Sonnet].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[Claude5Sonnet].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[Claude5Sonnet].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[Claude5Sonnet].SupportsToolSearch,
	},
	BedrockSonnet55: {
		ID:                       BedrockSonnet55,
		Name:                     "Bedrock: Claude 5.5 Sonnet",
		Provider:                 ProviderBedrock,
		APIModel:                 "claude-sonnet-5-5",
		CostPer1MIn:              AnthropicModels[Claude55Sonnet].CostPer1MIn,
		CostPer1MInCached:        AnthropicModels[Claude55Sonnet].CostPer1MInCached,
		CostPer1MOut:             AnthropicModels[Claude55Sonnet].CostPer1MOut,
		CostPer1MOutCached:       AnthropicModels[Claude55Sonnet].CostPer1MOutCached,
		ContextWindow:            AnthropicModels[Claude55Sonnet].ContextWindow,
		DefaultMaxTokens:         AnthropicModels[Claude55Sonnet].DefaultMaxTokens,
		SupportsAttachments:      AnthropicModels[Claude55Sonnet].SupportsAttachments,
		CanReason:                AnthropicModels[Claude55Sonnet].CanReason,
		SupportsAdaptiveThinking: AnthropicModels[Claude55Sonnet].SupportsAdaptiveThinking,
		SupportsMaximumThinking:  AnthropicModels[Claude55Sonnet].SupportsMaximumThinking,
		SupportsXHighThinking:    AnthropicModels[Claude55Sonnet].SupportsXHighThinking,
		SupportsTaskBudget:       AnthropicModels[Claude55Sonnet].SupportsTaskBudget,
		SupportsToolSearch:       AnthropicModels[Claude55Sonnet].SupportsToolSearch,
		RejectsForcedToolChoice:  AnthropicModels[Claude55Sonnet].RejectsForcedToolChoice,
	},
	BedrockEUHaiku45: {
		ID:                  BedrockEUHaiku45,
		Name:                "Bedrock EU: Claude 4.5 Haiku",
		Provider:            ProviderBedrock,
		APIModel:            "eu-claude-haiku-4-5",
		CostPer1MIn:         AnthropicModels[Claude45Haiku].CostPer1MIn * bedrockRegionalPremium,
		CostPer1MInCached:   AnthropicModels[Claude45Haiku].CostPer1MInCached * bedrockRegionalPremium,
		CostPer1MOut:        AnthropicModels[Claude45Haiku].CostPer1MOut * bedrockRegionalPremium,
		CostPer1MOutCached:  AnthropicModels[Claude45Haiku].CostPer1MOutCached * bedrockRegionalPremium,
		ContextWindow:       AnthropicModels[Claude45Haiku].ContextWindow,
		DefaultMaxTokens:    AnthropicModels[Claude45Haiku].DefaultMaxTokens,
		SupportsAttachments: AnthropicModels[Claude45Haiku].SupportsAttachments,
		SupportsToolSearch:  AnthropicModels[Claude45Haiku].SupportsToolSearch,
	},
	BedrockHaiku45: {
		ID:                  BedrockHaiku45,
		Name:                "Bedrock: Claude 4.5 Haiku",
		Provider:            ProviderBedrock,
		APIModel:            "claude-haiku-4-5",
		CostPer1MIn:         AnthropicModels[Claude45Haiku].CostPer1MIn,
		CostPer1MInCached:   AnthropicModels[Claude45Haiku].CostPer1MInCached,
		CostPer1MOut:        AnthropicModels[Claude45Haiku].CostPer1MOut,
		CostPer1MOutCached:  AnthropicModels[Claude45Haiku].CostPer1MOutCached,
		ContextWindow:       AnthropicModels[Claude45Haiku].ContextWindow,
		DefaultMaxTokens:    AnthropicModels[Claude45Haiku].DefaultMaxTokens,
		SupportsAttachments: AnthropicModels[Claude45Haiku].SupportsAttachments,
		SupportsToolSearch:  AnthropicModels[Claude45Haiku].SupportsToolSearch,
	},
	BedrockEUHaiku55: {
		ID:                        BedrockEUHaiku55,
		Name:                      "Bedrock EU: Claude 5.5 Haiku",
		Provider:                  ProviderBedrock,
		APIModel:                  "eu-claude-haiku-5-5",
		CostPer1MIn:               AnthropicModels[Claude55Haiku].CostPer1MIn * bedrockRegionalPremium,
		CostPer1MInCached:         AnthropicModels[Claude55Haiku].CostPer1MInCached * bedrockRegionalPremium,
		CostPer1MOut:              AnthropicModels[Claude55Haiku].CostPer1MOut * bedrockRegionalPremium,
		CostPer1MOutCached:        AnthropicModels[Claude55Haiku].CostPer1MOutCached * bedrockRegionalPremium,
		LongContextThreshold:      AnthropicModels[Claude55Haiku].LongContextThreshold,
		LongContextCostMultiplier: AnthropicModels[Claude55Haiku].LongContextCostMultiplier,
		ContextWindow:             AnthropicModels[Claude55Haiku].ContextWindow,
		DefaultMaxTokens:          AnthropicModels[Claude55Haiku].DefaultMaxTokens,
		SupportsAttachments:       AnthropicModels[Claude55Haiku].SupportsAttachments,
		CanReason:                 AnthropicModels[Claude55Haiku].CanReason,
		SupportsAdaptiveThinking:  AnthropicModels[Claude55Haiku].SupportsAdaptiveThinking,
		SupportsMaximumThinking:   AnthropicModels[Claude55Haiku].SupportsMaximumThinking,
		SupportsXHighThinking:     AnthropicModels[Claude55Haiku].SupportsXHighThinking,
		SupportsTaskBudget:        AnthropicModels[Claude55Haiku].SupportsTaskBudget,
		SupportsToolSearch:        AnthropicModels[Claude55Haiku].SupportsToolSearch,
		RejectsForcedToolChoice:   AnthropicModels[Claude55Haiku].RejectsForcedToolChoice,
		DefaultReasoningEffort:    AnthropicModels[Claude55Haiku].DefaultReasoningEffort,
	},
	BedrockHaiku55: {
		ID:                        BedrockHaiku55,
		Name:                      "Bedrock: Claude 5.5 Haiku",
		Provider:                  ProviderBedrock,
		APIModel:                  "claude-haiku-5-5",
		CostPer1MIn:               AnthropicModels[Claude55Haiku].CostPer1MIn,
		CostPer1MInCached:         AnthropicModels[Claude55Haiku].CostPer1MInCached,
		CostPer1MOut:              AnthropicModels[Claude55Haiku].CostPer1MOut,
		CostPer1MOutCached:        AnthropicModels[Claude55Haiku].CostPer1MOutCached,
		LongContextThreshold:      AnthropicModels[Claude55Haiku].LongContextThreshold,
		LongContextCostMultiplier: AnthropicModels[Claude55Haiku].LongContextCostMultiplier,
		ContextWindow:             AnthropicModels[Claude55Haiku].ContextWindow,
		DefaultMaxTokens:          AnthropicModels[Claude55Haiku].DefaultMaxTokens,
		SupportsAttachments:       AnthropicModels[Claude55Haiku].SupportsAttachments,
		CanReason:                 AnthropicModels[Claude55Haiku].CanReason,
		SupportsAdaptiveThinking:  AnthropicModels[Claude55Haiku].SupportsAdaptiveThinking,
		SupportsMaximumThinking:   AnthropicModels[Claude55Haiku].SupportsMaximumThinking,
		SupportsXHighThinking:     AnthropicModels[Claude55Haiku].SupportsXHighThinking,
		SupportsTaskBudget:        AnthropicModels[Claude55Haiku].SupportsTaskBudget,
		SupportsToolSearch:        AnthropicModels[Claude55Haiku].SupportsToolSearch,
		RejectsForcedToolChoice:   AnthropicModels[Claude55Haiku].RejectsForcedToolChoice,
		DefaultReasoningEffort:    AnthropicModels[Claude55Haiku].DefaultReasoningEffort,
	},
}

// BedrockKimiModels are Moonshot's Kimi models on Bedrock. Unlike the Claude
// entries above they do not speak Anthropic Messages: InvokeModel takes an
// OpenAI chat-completions body and returns chat.completion (streaming as
// EventStream frames wrapping chat.completion.chunk), so they carry
// WireOpenAIChat and the bedrock provider serves them through the OpenAI
// client — see provider/bedrock.go. Reasoning
// is requested through the OpenAI-style reasoning_effort field rather than
// adaptive thinking, but with K3's levels: low|high|max, max being the
// documented default on Moonshot and AWS alike (Bedrock also accepts medium;
// xhigh is not a K3 level). SupportsMaximumThinking therefore holds, so the
// config layer and flow-step validation admit max — the OpenAI rules would
// default it to medium and fold a configured max down to medium. There is
// no EU geo profile yet: Bedrock offers global, US and India, and only the
// first two are registered here.
//
// Bedrock caches the prompt prefix automatically and reports cache writes as
// prompt_tokens_details.cache_write_tokens, billed at CostPer1MInCached.
//
// K3.1 (Bedrock's "Kimi K3.1 Pro - Highspeed", launched 2026-11-04) takes
// none|low|medium|high|xhigh|max with max the default, so it carries both
// top-level flags and DefaultReasoningEffort; it has the same global/US/India
// profiles and no EU one. Its global rate equals Moonshot's Highspeed rate.
var BedrockKimiModels = map[ModelID]Model{
	BedrockKimiK31: {
		ID:                      BedrockKimiK31,
		Name:                    "Bedrock: Kimi K3.1",
		Provider:                ProviderBedrock,
		APIModel:                "global.moonshotai.kimi-k3.1",
		Wire:                    WireOpenAIChat,
		CostPer1MIn:             4.0,
		CostPer1MInCached:       5.0,
		CostPer1MOut:            20.0,
		CostPer1MOutCached:      0.20,
		ContextWindow:           1_048_576,
		DefaultMaxTokens:        131_072,
		CanReason:               true,
		SupportsMaximumThinking: true,
		SupportsXHighThinking:   true,
		SupportsAttachments:     true,
		DefaultReasoningEffort:  ReasoningEffortMax,
	},
	BedrockUSKimiK31: {
		ID:                      BedrockUSKimiK31,
		Name:                    "Bedrock US: Kimi K3.1",
		Provider:                ProviderBedrock,
		APIModel:                "us.moonshotai.kimi-k3.1",
		Wire:                    WireOpenAIChat,
		CostPer1MIn:             4.0 * bedrockRegionalPremium,
		CostPer1MInCached:       5.0 * bedrockRegionalPremium,
		CostPer1MOut:            20.0 * bedrockRegionalPremium,
		CostPer1MOutCached:      0.20 * bedrockRegionalPremium,
		ContextWindow:           1_048_576,
		DefaultMaxTokens:        131_072,
		CanReason:               true,
		SupportsMaximumThinking: true,
		SupportsXHighThinking:   true,
		SupportsAttachments:     true,
		DefaultReasoningEffort:  ReasoningEffortMax,
	},
	BedrockKimiK3: {
		ID:                      BedrockKimiK3,
		Name:                    "Bedrock: Kimi K3",
		Provider:                ProviderBedrock,
		APIModel:                "global.moonshotai.kimi-k3",
		Wire:                    WireOpenAIChat,
		CostPer1MIn:             3.0,
		CostPer1MInCached:       3.75,
		CostPer1MOut:            15.0,
		CostPer1MOutCached:      0.30,
		ContextWindow:           1_000_000,
		DefaultMaxTokens:        131_072,
		CanReason:               true,
		SupportsMaximumThinking: true,
		SupportsAttachments:     true,
	},
	BedrockUSKimiK3: {
		ID:                      BedrockUSKimiK3,
		Name:                    "Bedrock US: Kimi K3",
		Provider:                ProviderBedrock,
		APIModel:                "us.moonshotai.kimi-k3",
		Wire:                    WireOpenAIChat,
		CostPer1MIn:             3.0 * bedrockRegionalPremium,
		CostPer1MInCached:       3.75 * bedrockRegionalPremium,
		CostPer1MOut:            15.0 * bedrockRegionalPremium,
		CostPer1MOutCached:      0.30 * bedrockRegionalPremium,
		ContextWindow:           1_000_000,
		DefaultMaxTokens:        131_072,
		CanReason:               true,
		SupportsMaximumThinking: true,
		SupportsAttachments:     true,
	},
}

// gpt61LongContextThreshold is the prompt size above which GPT-6.1 bills the
// request at gpt61LongContextInputMultiplier on input (cache reads and
// writes included) and gpt61LongContextOutputMultiplier on output.
const (
	gpt61LongContextThreshold        = 272_000
	gpt61LongContextInputMultiplier  = 2.0
	gpt61LongContextOutputMultiplier = 1.5
)

// BedrockOpenAIModels are OpenAI's GPT-6.1 models on Bedrock. Like Kimi they
// take OpenAI chat-completions bodies on InvokeModel (WireOpenAIChat) and
// have global and US profiles but no EU one, so they must not see client
// data. Rates are OpenAI's first-party prices (Bedrock global), the US
// profile adds the regional premium, cache reads are 0.05x and writes 1.25x
// input.
//
// PENDING TOOL-CALL VERIFICATION: OpenAI documents GPT-6.1 tool calling for
// the Responses API, while these entries send Chat Completions through the
// InvokeModel passthrough. Verify a tool-call round trip on that route
// before giving either model weight in a model pool. Attachments stay off until the probe
// covers them. Sol's effort levels (low..max, default medium) are from
// OpenAI's model page; Terra's are unconfirmed, so it stops at high and the
// pool clamp folds xhigh/max down. The 1,050,000-token context is not on the
// Bedrock cards; it is the window the 272,000-token long-context tier
// implies — revisit after the probe.
var BedrockOpenAIModels = map[ModelID]Model{
	BedrockGPT61Sol:     gpt61Model(BedrockGPT61Sol, "Bedrock: GPT-6.1 Sol", "global.openai.gpt-6.1-sol", 2.0, true, 1),
	BedrockUSGPT61Sol:   gpt61Model(BedrockUSGPT61Sol, "Bedrock US: GPT-6.1 Sol", "us.openai.gpt-6.1-sol", 2.0, true, bedrockRegionalPremium),
	BedrockGPT61Terra:   gpt61Model(BedrockGPT61Terra, "Bedrock: GPT-6.1 Terra", "global.openai.gpt-6.1-terra", 1.0, false, 1),
	BedrockUSGPT61Terra: gpt61Model(BedrockUSGPT61Terra, "Bedrock US: GPT-6.1 Terra", "us.openai.gpt-6.1-terra", 1.0, false, bedrockRegionalPremium),
}

// gpt61Model builds a GPT-6.1 entry from its input rate (output is 5x,
// cache writes 1.25x, cache reads 0.05x) and the regional premium.
func gpt61Model(id ModelID, name, apiModel string, input float64, topEfforts bool, premium float64) Model {
	return Model{
		ID:                              id,
		Name:                            name,
		Provider:                        ProviderBedrock,
		APIModel:                        apiModel,
		Wire:                            WireOpenAIChat,
		CostPer1MIn:                     input * premium,
		CostPer1MInCached:               input * 1.25 * premium,
		CostPer1MOut:                    input * 5 * premium,
		CostPer1MOutCached:              input * 0.05 * premium,
		ContextWindow:                   1_050_000,
		DefaultMaxTokens:                128_000,
		CanReason:                       true,
		SupportsXHighThinking:           topEfforts,
		SupportsMaximumThinking:         topEfforts,
		LongContextThreshold:            gpt61LongContextThreshold,
		LongContextCostMultiplier:       gpt61LongContextInputMultiplier,
		LongContextOutputCostMultiplier: gpt61LongContextOutputMultiplier,
		DefaultReasoningEffort:          ReasoningEffortMedium,
	}
}

// IsKimiK3 reports whether id is a Kimi K3-family deployment on any provider. K3
// documents the effort levels low|high|max with max as the default, so the
// config layer resolves an unset reasoningEffort to max on every K3 route
// instead of leaving it to a client default (high for the anthropic client,
// medium for the openai one) that K3 does not document.
func IsKimiK3(id ModelID) bool {
	switch id {
	case KimiK3, KimiK31Pro, KimiK31ProHighspeed, BedrockKimiK3, BedrockUSKimiK3, BedrockKimiK31, BedrockUSKimiK31:
		return true
	}
	return false
}

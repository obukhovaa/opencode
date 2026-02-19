package models

const (
	ProviderVertexAI ModelProvider = "vertexai"

	// Models
	VertexAIGemini30Flash ModelID = "vertexai.gemini-3.0-flash"
	VertexAIGemini30Pro   ModelID = "vertexai.gemini-3.0-pro"
	VertexAISonnet45M     ModelID = "vertexai.claude-sonnet-4-5-m"
	VertexAIOpus45        ModelID = "vertexai.claude-opus-4-5"
	VertexAIOpus46        ModelID = "vertexai.claude-opus-4-6"
	VertexAISonnet46      ModelID = "vertexai.claude-sonnet-4-6"
)

var VertexAIGeminiModels = map[ModelID]Model{
	VertexAIGemini30Pro: {
		ID:                  VertexAIGemini30Pro,
		Name:                "VertexAI: Gemini 3.0 Pro",
		Provider:            ProviderVertexAI,
		APIModel:            "gemini-3-pro-preview",
		CostPer1MIn:         GeminiModels[Gemini30Pro].CostPer1MIn,
		CostPer1MInCached:   GeminiModels[Gemini30Pro].CostPer1MInCached,
		CostPer1MOut:        GeminiModels[Gemini30Pro].CostPer1MOut,
		CostPer1MOutCached:  GeminiModels[Gemini30Pro].CostPer1MOutCached,
		ContextWindow:       GeminiModels[Gemini30Pro].ContextWindow,
		DefaultMaxTokens:    GeminiModels[Gemini30Pro].DefaultMaxTokens,
		SupportsAttachments: true,
		CanReason:           true,
	},
	VertexAIGemini30Flash: {
		ID:                  VertexAIGemini30Flash,
		Name:                "VertexAI: Gemini 3.0 Flash",
		Provider:            ProviderVertexAI,
		APIModel:            "gemini-3-flash-preview",
		CostPer1MIn:         GeminiModels[Gemini30Flash].CostPer1MIn,
		CostPer1MInCached:   GeminiModels[Gemini30Flash].CostPer1MInCached,
		CostPer1MOut:        GeminiModels[Gemini30Flash].CostPer1MOut,
		CostPer1MOutCached:  GeminiModels[Gemini30Flash].CostPer1MOutCached,
		ContextWindow:       GeminiModels[Gemini30Flash].ContextWindow,
		DefaultMaxTokens:    GeminiModels[Gemini30Flash].DefaultMaxTokens,
		SupportsAttachments: true,
		CanReason:           true,
	},
}

var VertexAIAnthropicModels = map[ModelID]Model{
	VertexAISonnet45M: {
		ID:                  VertexAISonnet45M,
		Name:                "VertexAI: Claude Sonnet 4.5 [1m]",
		Provider:            ProviderVertexAI,
		APIModel:            "claude-sonnet-4-5@20250929",
		CostPer1MIn:         AnthropicModels[Claude45Sonnet1M].CostPer1MIn,
		CostPer1MInCached:   AnthropicModels[Claude45Sonnet1M].CostPer1MInCached,
		CostPer1MOut:        AnthropicModels[Claude45Sonnet1M].CostPer1MOut,
		CostPer1MOutCached:  AnthropicModels[Claude45Sonnet1M].CostPer1MOutCached,
		ContextWindow:       AnthropicModels[Claude45Sonnet1M].ContextWindow,
		DefaultMaxTokens:    AnthropicModels[Claude45Sonnet1M].DefaultMaxTokens,
		SupportsAttachments: AnthropicModels[Claude45Sonnet1M].SupportsAttachments,
		CanReason:           AnthropicModels[Claude45Sonnet1M].CanReason,
	},
	VertexAIOpus45: {
		ID:                  VertexAIOpus45,
		Name:                "VertexAI: Claude Opus 4.5",
		Provider:            ProviderVertexAI,
		APIModel:            "claude-opus-4-5@20251101",
		CostPer1MIn:         AnthropicModels[Claude45Opus].CostPer1MIn,
		CostPer1MInCached:   AnthropicModels[Claude45Opus].CostPer1MInCached,
		CostPer1MOut:        AnthropicModels[Claude45Opus].CostPer1MOut,
		CostPer1MOutCached:  AnthropicModels[Claude45Opus].CostPer1MOutCached,
		ContextWindow:       AnthropicModels[Claude45Opus].ContextWindow,
		DefaultMaxTokens:    AnthropicModels[Claude45Opus].DefaultMaxTokens,
		SupportsAttachments: AnthropicModels[Claude45Opus].SupportsAttachments,
		CanReason:           AnthropicModels[Claude45Opus].CanReason,
	},
	VertexAIOpus46: {
		ID:                       VertexAIOpus46,
		Name:                     "VertexAI: Claude Opus 4.6",
		Provider:                 ProviderVertexAI,
		APIModel:                 "claude-opus-4-6",
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
	VertexAISonnet46: {
		ID:                  VertexAISonnet46,
		Name:                "VertexAI: Claude Sonnet 4.6",
		Provider:            ProviderVertexAI,
		APIModel:            "claude-sonnet-4-6",
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

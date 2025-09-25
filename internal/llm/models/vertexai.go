package models

const (
	ProviderVertexAI ModelProvider = "vertexai"

	// Models
	VertexAIGemini25Flash ModelID = "vertexai.gemini-2.5-flash"
	VertexAIGemini25      ModelID = "vertexai.gemini-2.5-pro"
	VertexAISonnet4       ModelID = "vertexai.claude-sonnet-4"
	VertexAISonnet4M      ModelID = "vertexai.claude-sonnet-4-m"
	VertexAIOpus4         ModelID = "vertexai.claude-opus-4"
	VertexAIOpus41        ModelID = "vertexai.claude-opus-4-1"
)

var VertexAIGeminiModels = map[ModelID]Model{
	VertexAIGemini25Flash: {
		ID:                  VertexAIGemini25Flash,
		Name:                "VertexAI: Gemini 2.5 Flash",
		Provider:            ProviderVertexAI,
		APIModel:            "gemini-2.5-flash",
		CostPer1MIn:         GeminiModels[Gemini25Flash].CostPer1MIn,
		CostPer1MInCached:   GeminiModels[Gemini25Flash].CostPer1MInCached,
		CostPer1MOut:        GeminiModels[Gemini25Flash].CostPer1MOut,
		CostPer1MOutCached:  GeminiModels[Gemini25Flash].CostPer1MOutCached,
		ContextWindow:       GeminiModels[Gemini25Flash].ContextWindow,
		DefaultMaxTokens:    GeminiModels[Gemini25Flash].DefaultMaxTokens,
		SupportsAttachments: true,
	},
	VertexAIGemini25: {
		ID:                  VertexAIGemini25,
		Name:                "VertexAI: Gemini 2.5 Pro",
		Provider:            ProviderVertexAI,
		APIModel:            "gemini-2.5-pro",
		CostPer1MIn:         GeminiModels[Gemini25].CostPer1MIn,
		CostPer1MInCached:   GeminiModels[Gemini25].CostPer1MInCached,
		CostPer1MOut:        GeminiModels[Gemini25].CostPer1MOut,
		CostPer1MOutCached:  GeminiModels[Gemini25].CostPer1MOutCached,
		ContextWindow:       GeminiModels[Gemini25].ContextWindow,
		DefaultMaxTokens:    GeminiModels[Gemini25].DefaultMaxTokens,
		CanReason:           GeminiModels[Gemini25].CanReason,
		SupportsAttachments: true,
	},
}

var VertexAIAnthropicModels = map[ModelID]Model{
	VertexAISonnet4: {
		ID:                  VertexAISonnet4,
		Name:                "VertexAI: Claude Sonnet 4",
		Provider:            ProviderVertexAI,
		APIModel:            "claude-sonnet-4@20250514",
		CostPer1MIn:         AnthropicModels[Claude4Sonnet].CostPer1MIn,
		CostPer1MInCached:   AnthropicModels[Claude4Sonnet].CostPer1MInCached,
		CostPer1MOut:        AnthropicModels[Claude4Sonnet].CostPer1MOut,
		CostPer1MOutCached:  AnthropicModels[Claude4Sonnet].CostPer1MOutCached,
		ContextWindow:       AnthropicModels[Claude4Sonnet].ContextWindow,
		DefaultMaxTokens:    AnthropicModels[Claude4Sonnet].DefaultMaxTokens,
		SupportsAttachments: AnthropicModels[Claude4Sonnet].SupportsAttachments,
		CanReason:           AnthropicModels[Claude4Sonnet].CanReason,
	},
	VertexAISonnet4M: {
		ID:                  VertexAISonnet4M,
		Name:                "VertexAI: Claude Sonnet 4 [1m]",
		Provider:            ProviderVertexAI,
		APIModel:            "claude-sonnet-4@20250514",
		CostPer1MIn:         AnthropicModels[Claude4Sonnet1M].CostPer1MIn,
		CostPer1MInCached:   AnthropicModels[Claude4Sonnet1M].CostPer1MInCached,
		CostPer1MOut:        AnthropicModels[Claude4Sonnet1M].CostPer1MOut,
		CostPer1MOutCached:  AnthropicModels[Claude4Sonnet1M].CostPer1MOutCached,
		ContextWindow:       AnthropicModels[Claude4Sonnet1M].ContextWindow,
		DefaultMaxTokens:    AnthropicModels[Claude4Sonnet1M].DefaultMaxTokens,
		SupportsAttachments: AnthropicModels[Claude4Sonnet1M].SupportsAttachments,
		CanReason:           AnthropicModels[Claude4Sonnet1M].CanReason,
	},
	VertexAIOpus4: {
		ID:                  VertexAIOpus4,
		Name:                "VertexAI: Claude Opus 4",
		Provider:            ProviderVertexAI,
		APIModel:            "claude-opus-4@20250514",
		CostPer1MIn:         AnthropicModels[Claude4Opus].CostPer1MIn,
		CostPer1MInCached:   AnthropicModels[Claude4Opus].CostPer1MInCached,
		CostPer1MOut:        AnthropicModels[Claude4Opus].CostPer1MOut,
		CostPer1MOutCached:  AnthropicModels[Claude4Opus].CostPer1MOutCached,
		ContextWindow:       AnthropicModels[Claude4Opus].ContextWindow,
		DefaultMaxTokens:    AnthropicModels[Claude4Opus].DefaultMaxTokens,
		SupportsAttachments: AnthropicModels[Claude4Opus].SupportsAttachments,
		CanReason:           AnthropicModels[Claude4Opus].CanReason,
	},
	VertexAIOpus41: {
		ID:                  VertexAIOpus41,
		Name:                "VertexAI: Claude Opus 4.1",
		Provider:            ProviderVertexAI,
		APIModel:            "claude-opus-4-1@20250805",
		CostPer1MIn:         AnthropicModels[Claude41Opus].CostPer1MIn,
		CostPer1MInCached:   AnthropicModels[Claude41Opus].CostPer1MInCached,
		CostPer1MOut:        AnthropicModels[Claude41Opus].CostPer1MOut,
		CostPer1MOutCached:  AnthropicModels[Claude41Opus].CostPer1MOutCached,
		ContextWindow:       AnthropicModels[Claude41Opus].ContextWindow,
		DefaultMaxTokens:    AnthropicModels[Claude41Opus].DefaultMaxTokens,
		SupportsAttachments: AnthropicModels[Claude41Opus].SupportsAttachments,
		CanReason:           AnthropicModels[Claude41Opus].CanReason,
	},
}

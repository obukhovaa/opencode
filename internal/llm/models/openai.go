package models

const (
	ProviderOpenAI ModelProvider = "openai"

	// TODO: add modern
	O3     ModelID = "o3"
	O4Mini ModelID = "o4-mini"
	GPT5   ModelID = "gpt-5"
)

var OpenAIModels = map[ModelID]Model{
	O3: {
		ID:                  O3,
		Name:                "o3",
		Provider:            ProviderOpenAI,
		APIModel:            "o3",
		CostPer1MIn:         10.00,
		CostPer1MInCached:   2.50,
		CostPer1MOutCached:  0.0,
		CostPer1MOut:        40.00,
		ContextWindow:       200_000,
		CanReason:           true,
		SupportsAttachments: true,
	},
	O4Mini: {
		ID:                  O4Mini,
		Name:                "o4 mini",
		Provider:            ProviderOpenAI,
		APIModel:            "o4-mini",
		CostPer1MIn:         1.10,
		CostPer1MInCached:   0.275,
		CostPer1MOutCached:  0.0,
		CostPer1MOut:        4.40,
		ContextWindow:       128_000,
		DefaultMaxTokens:    50000,
		CanReason:           true,
		SupportsAttachments: true,
	},
	GPT5: {
		ID:                  GPT5,
		Name:                "GPT 5",
		Provider:            ProviderOpenAI,
		APIModel:            "gpt-5",
		CostPer1MIn:         1.25,
		CostPer1MInCached:   0.125,
		CostPer1MOutCached:  0.0,
		CostPer1MOut:        10,
		ContextWindow:       400_000,
		DefaultMaxTokens:    128_000,
		CanReason:           true,
		SupportsAttachments: true,
	},
}

package models

const (
	ProviderAnthropic ModelProvider = "anthropic"

	// Models
	Claude45Sonnet1M ModelID = "claude-4-5-sonnet[1m]"
	Claude45Opus     ModelID = "claude-4.5-opus"
)

// https://docs.anthropic.com/en/docs/about-claude/models/all-models
var AnthropicModels = map[ModelID]Model{
	Claude45Sonnet1M: {
		ID:                  Claude45Sonnet1M,
		Name:                "Claude 4.5 Sonnet [1M]",
		Provider:            ProviderAnthropic,
		APIModel:            "claude-sonnet-4-5",
		CostPer1MIn:         3.0,
		CostPer1MInCached:   3.75,
		CostPer1MOutCached:  0.30,
		CostPer1MOut:        15.0,
		ContextWindow:       1000000,
		DefaultMaxTokens:    64000,
		CanReason:           true,
		SupportsAttachments: true,
	},
	Claude45Opus: {
		ID:                  Claude45Opus,
		Name:                "Claude 4.5 Opus",
		Provider:            ProviderAnthropic,
		APIModel:            "claude-opus-4-5-20251101",
		CostPer1MIn:         5.0,
		CostPer1MInCached:   6.75,
		CostPer1MOutCached:  0.50,
		CostPer1MOut:        25.0,
		ContextWindow:       200000,
		DefaultMaxTokens:    32000,
		CanReason:           true,
		SupportsAttachments: true,
	},
}

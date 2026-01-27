package models

const (
	ProviderGemini ModelProvider = "gemini"

	// Models
	Gemini30Pro       ModelID = "gemini-3.0-pro"
	Gemini30Flash     ModelID = "gemini-3.0-flash"
	Gemini25Flash     ModelID = "gemini-2.5-flash"
	Gemini25          ModelID = "gemini-2.5"
	Gemini20Flash     ModelID = "gemini-2.0-flash"
	Gemini20FlashLite ModelID = "gemini-2.0-flash-lite"
)

var GeminiModels = map[ModelID]Model{
	Gemini30Pro: {
		ID:                  Gemini30Pro,
		Name:                "Gemini 3.0 Pro",
		Provider:            ProviderGemini,
		APIModel:            "gemini-3-pro-preview",
		CostPer1MIn:         2,
		CostPer1MInCached:   0.2,
		CostPer1MOutCached:  0.3833,
		CostPer1MOut:        12,
		ContextWindow:       1048576,
		DefaultMaxTokens:    65535,
		SupportsAttachments: true,
		CanReason:           true,
	},
	Gemini30Flash: {
		ID:                  Gemini30Flash,
		Name:                "Gemini 3.0 Flash",
		Provider:            ProviderGemini,
		APIModel:            "gemini-3-flash-preview",
		CostPer1MIn:         0.5,
		CostPer1MInCached:   0.05,
		CostPer1MOutCached:  0.3833,
		CostPer1MOut:        3,
		ContextWindow:       1048576,
		DefaultMaxTokens:    65535,
		SupportsAttachments: true,
		CanReason:           true,
	},
	Gemini25Flash: {
		ID:                  Gemini25Flash,
		Name:                "Gemini 2.5 Flash",
		Provider:            ProviderGemini,
		APIModel:            "gemini-2.5-flash",
		CostPer1MIn:         0.3,
		CostPer1MInCached:   0.075,
		CostPer1MOutCached:  0.3833,
		CostPer1MOut:        2.5,
		ContextWindow:       1048576,
		DefaultMaxTokens:    65535,
		SupportsAttachments: true,
	},
	Gemini25: {
		ID:                  Gemini25,
		Name:                "Gemini 2.5 Pro",
		Provider:            ProviderGemini,
		APIModel:            "gemini-2.5-pro",
		CostPer1MIn:         1.25,
		CostPer1MInCached:   0.31,
		CostPer1MOutCached:  1.625,
		CostPer1MOut:        10,
		ContextWindow:       1048576,
		DefaultMaxTokens:    65536,
		CanReason:           true,
		SupportsAttachments: true,
	},

	Gemini20Flash: {
		ID:                  Gemini20Flash,
		Name:                "Gemini 2.0 Flash",
		Provider:            ProviderGemini,
		APIModel:            "gemini-2.0-flash",
		CostPer1MIn:         0.10,
		CostPer1MInCached:   0,
		CostPer1MOutCached:  0,
		CostPer1MOut:        0.40,
		ContextWindow:       1000000,
		DefaultMaxTokens:    6000,
		SupportsAttachments: true,
	},
	Gemini20FlashLite: {
		ID:                  Gemini20FlashLite,
		Name:                "Gemini 2.0 Flash Lite",
		Provider:            ProviderGemini,
		APIModel:            "gemini-2.0-flash-lite",
		CostPer1MIn:         0.05,
		CostPer1MInCached:   0,
		CostPer1MOutCached:  0,
		CostPer1MOut:        0.30,
		ContextWindow:       1000000,
		DefaultMaxTokens:    6000,
		SupportsAttachments: true,
	},
}

package models

const (
	ProviderGemini ModelProvider = "gemini"

	// Models
	Gemini30Pro   ModelID = "gemini-3.0-pro"
	Gemini30Flash ModelID = "gemini-3.0-flash"
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
}

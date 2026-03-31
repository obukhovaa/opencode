package models

type (
	ModelID       string
	ModelProvider string
)

type Model struct {
	ID                       ModelID       `json:"id"`
	Name                     string        `json:"name"`
	Provider                 ModelProvider `json:"provider"`
	APIModel                 string        `json:"api_model"`
	CostPer1MIn              float64       `json:"cost_per_1m_in"`
	CostPer1MOut             float64       `json:"cost_per_1m_out"`
	CostPer1MInCached        float64       `json:"cost_per_1m_in_cached"`
	CostPer1MOutCached       float64       `json:"cost_per_1m_out_cached"`
	ContextWindow            int64         `json:"context_window"`
	DefaultMaxTokens         int64         `json:"default_max_tokens"`
	CanReason                bool          `json:"can_reason"`
	SupportsAdaptiveThinking bool          `json:"supports_adaptive_thinking"`
	SupportsMaximumThinking  bool          `json:"supports_maximum_thinking"`
	SupportsAttachments      bool          `json:"supports_attachments"`
	UseLegacyMaxTokens       bool          `json:"use_legacy_max_tokens,omitempty"`
}

const (
	// ForTests
	ProviderMock ModelProvider = "__mock"
)

// Providers in order of popularity
var ProviderPopularity = map[ModelProvider]int{
	ProviderVertexAI:  1,
	ProviderAnthropic: 2,
	ProviderOpenAI:    3,
	ProviderGemini:    4,
	ProviderBedrock:   5,
}

var SupportedModels = map[ModelID]Model{}

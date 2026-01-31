package models

type (
	ModelID       string
	ModelProvider string
)

type Model struct {
	ID                  ModelID       `json:"id"`
	Name                string        `json:"name"`
	Provider            ModelProvider `json:"provider"`
	APIModel            string        `json:"api_model"`
	CostPer1MIn         float64       `json:"cost_per_1m_in"`
	CostPer1MOut        float64       `json:"cost_per_1m_out"`
	CostPer1MInCached   float64       `json:"cost_per_1m_in_cached"`
	CostPer1MOutCached  float64       `json:"cost_per_1m_out_cached"`
	ContextWindow       int64         `json:"context_window"`
	DefaultMaxTokens    int64         `json:"default_max_tokens"`
	CanReason           bool          `json:"can_reason"`
	SupportsAttachments bool          `json:"supports_attachments"`
}

const (
	ProviderBedrock ModelProvider = "bedrock"
	// ForTests
	ProviderMock          ModelProvider = "__mock"
	BedrockClaude45Sonnet ModelID       = "bedrock.claude-4.5-sonnet"
)

// Providers in order of popularity
var ProviderPopularity = map[ModelProvider]int{
	ProviderVertexAI:  1,
	ProviderAnthropic: 2,
	ProviderOpenAI:    3,
	ProviderGemini:    4,
	ProviderBedrock:   5,
}

var SupportedModels = map[ModelID]Model{
	BedrockClaude45Sonnet: {
		ID:                 BedrockClaude45Sonnet,
		Name:               "Bedrock: Claude 4.5 Sonnet",
		Provider:           ProviderBedrock,
		APIModel:           "eu.anthropic.claude-sonnet-4-5-20250929-v1:0",
		CostPer1MIn:        3.0,
		CostPer1MInCached:  3.75,
		CostPer1MOutCached: 0.30,
		CostPer1MOut:       15.0,
	},
}

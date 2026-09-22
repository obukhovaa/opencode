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
	SupportsXHighThinking    bool          `json:"supports_xhigh_thinking"`
	SupportsTaskBudget       bool          `json:"supports_task_budget"`
	SupportsToolSearch       bool          `json:"supports_tool_search"`
	SupportsAttachments      bool          `json:"supports_attachments"`
	UseLegacyMaxTokens       bool          `json:"use_legacy_max_tokens,omitempty"`
	// RejectsForcedToolChoice marks models whose API answers a forced
	// tool_choice ("tool"/"any") with a 400 (Claude Opus 5.5, Claude Fable
	// 5.1). The Anthropic request builder runs a forced-tool request as a
	// normal auto turn instead.
	//
	// The polarity is a deliberate tradeoff, and it is the risky one. Anthropic
	// removed forced tool use because these models think unconditionally and a
	// forced call would skip thinking — so every model that makes thinking
	// mandatory from here on will reject forcing too (Mythos Preview, then
	// Fable 5.1, then Opus 5.5). A zero value therefore means "forcing works",
	// which is right for the existing catalogue but wrong for the direction of
	// travel: a newer model added without this flag 400s at run time on the
	// struct_output rescue turn. The safe-by-default alternative
	// (SupportsForcedToolChoice) would instead silently never force on a model
	// that supports it — a quieter failure, but it would need every existing
	// entry audited and flipped. Kept negative for that reason; when adding a
	// model, read its breaking changes and set this rather than relying on the
	// default (see .agents/skills/add-model/SKILL.md).
	RejectsForcedToolChoice bool `json:"rejects_forced_tool_choice,omitempty"`
}

const (
	// ForTests
	ProviderMock ModelProvider = "__mock"
)

// Providers in order of popularity
var ProviderPopularity = map[ModelProvider]int{
	ProviderVertexAI:    1,
	ProviderAnthropic:   2,
	ProviderOpenAI:      3,
	ProviderGemini:      4,
	ProviderBedrock:     5,
	ProviderYandexCloud: 6,
	ProviderKimi:        7,
}

var SupportedModels = map[ModelID]Model{}

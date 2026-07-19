package models

// Kimi (Moonshot AI) models, served through Moonshot's Anthropic-compatible
// endpoint (https://platform.kimi.ai/docs/guide/claude-code-kimi). The
// provider client is the shared anthropic client — see
// provider.NewProvider's ProviderKimi case.
const (
	ProviderKimi ModelProvider = "kimi"

	KimiK3 ModelID = "kimi.kimi-k3"
)

var KimiModels = map[ModelID]Model{
	KimiK3: {
		ID:       KimiK3,
		Name:     "Kimi K3",
		Provider: ProviderKimi,
		APIModel: "kimi-k3",
		// Flat pricing regardless of context length; cache reads $0.30/1M.
		// Moonshot's caching is automatic with no documented write premium,
		// so cache-creation tokens (if ever reported) bill as normal input.
		CostPer1MIn:        3.0,
		CostPer1MInCached:  3.0,
		CostPer1MOutCached: 0.30,
		CostPer1MOut:       15.0,
		ContextWindow:      1_000_000,
		DefaultMaxTokens:   131_072,
		CanReason:          true,
		// K3 thinks by default; the Anthropic-compatible endpoint takes
		// thinking {type: adaptive} with output_config.effort — only "max"
		// is exposed at launch (config defaults kimi agents to it).
		SupportsAdaptiveThinking: true,
		SupportsMaximumThinking:  true,
		SupportsAttachments:      true,
	},
}

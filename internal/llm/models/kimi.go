package models

// Kimi (Moonshot AI) models, served through Moonshot's Anthropic-compatible
// endpoint (https://platform.kimi.ai/docs/guide/claude-code-kimi). The
// provider client is the shared anthropic client — see
// provider.NewProvider's ProviderKimi case.
const (
	ProviderKimi ModelProvider = "kimi"

	KimiK3               ModelID = "kimi.kimi-k3"
	KimiK27Code          ModelID = "kimi.kimi-k2.7-code"
	KimiK27CodeHighspeed ModelID = "kimi.kimi-k2.7-code-highspeed"
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
	// K2.7 Code and its Highspeed variant (same model, ~180-260 tok/s, at
	// double the price) take adaptive thinking with effort low..max and
	// thinking: disabled, and accept image input. 256K context. Same
	// automatic caching as K3: cache reads discounted, no write premium.
	KimiK27Code: {
		ID:                       KimiK27Code,
		Name:                     "Kimi K2.7 Code",
		Provider:                 ProviderKimi,
		APIModel:                 "kimi-k2.7-code",
		CostPer1MIn:              0.95,
		CostPer1MInCached:        0.95,
		CostPer1MOutCached:       0.19,
		CostPer1MOut:             4.0,
		ContextWindow:            262_144,
		DefaultMaxTokens:         32_768,
		CanReason:                true,
		SupportsAdaptiveThinking: true,
		SupportsMaximumThinking:  true,
		SupportsAttachments:      true,
	},
	KimiK27CodeHighspeed: {
		ID:                       KimiK27CodeHighspeed,
		Name:                     "Kimi K2.7 Code Highspeed",
		Provider:                 ProviderKimi,
		APIModel:                 "kimi-k2.7-code-highspeed",
		CostPer1MIn:              1.90,
		CostPer1MInCached:        1.90,
		CostPer1MOutCached:       0.38,
		CostPer1MOut:             8.0,
		ContextWindow:            262_144,
		DefaultMaxTokens:         32_768,
		CanReason:                true,
		SupportsAdaptiveThinking: true,
		SupportsMaximumThinking:  true,
		SupportsAttachments:      true,
	},
}

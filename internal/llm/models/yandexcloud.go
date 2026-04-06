package models

const (
	ProviderYandexCloud ModelProvider = "yandexcloud"

	YCAliceAILLM     ModelID = "yandexcloud.aliceai-llm"
	YCYandexGPTPro51 ModelID = "yandexcloud.yandexgpt-pro-5.1"
	YCYandexGPTPro5  ModelID = "yandexcloud.yandexgpt-pro-5"
	YCYandexGPTLite5 ModelID = "yandexcloud.yandexgpt-lite-5"
	YCDeepSeekV32    ModelID = "yandexcloud.deepseek-v3.2"
	YCQwen3235B      ModelID = "yandexcloud.qwen3-235b"
	YCQwen35_35B     ModelID = "yandexcloud.qwen3.5-35b"
	YCGPTOss120B     ModelID = "yandexcloud.gpt-oss-120b"
)

var YandexCloudModels = map[ModelID]Model{
	YCAliceAILLM: {
		ID:                 YCAliceAILLM,
		Name:               "Alice AI LLM",
		Provider:           ProviderYandexCloud,
		APIModel:           "aliceai-llm/latest",
		CostPer1MIn:        4.098,
		CostPer1MInCached:  4.098,
		CostPer1MOutCached: 4.098,
		CostPer1MOut:       9.836,
		ContextWindow:      32_768,
		DefaultMaxTokens:   8192,
		UseLegacyMaxTokens: true,
		CanReason:          true,
	},
	YCYandexGPTPro51: {
		ID:                 YCYandexGPTPro51,
		Name:               "YandexGPT Pro 5.1",
		Provider:           ProviderYandexCloud,
		APIModel:           "yandexgpt/rc",
		CostPer1MIn:        6.557,
		CostPer1MInCached:  6.557,
		CostPer1MOutCached: 6.557,
		CostPer1MOut:       6.557,
		ContextWindow:      32_768,
		DefaultMaxTokens:   8192,
		UseLegacyMaxTokens: true,
		CanReason:          true,
	},
	YCYandexGPTPro5: {
		ID:                 YCYandexGPTPro5,
		Name:               "YandexGPT Pro 5",
		Provider:           ProviderYandexCloud,
		APIModel:           "yandexgpt/latest",
		CostPer1MIn:        9.836,
		CostPer1MInCached:  9.836,
		CostPer1MOutCached: 9.836,
		CostPer1MOut:       9.836,
		ContextWindow:      32_768,
		DefaultMaxTokens:   8192,
		UseLegacyMaxTokens: true,
		CanReason:          true,
	},
	YCYandexGPTLite5: {
		ID:                 YCYandexGPTLite5,
		Name:               "YandexGPT Lite 5",
		Provider:           ProviderYandexCloud,
		APIModel:           "yandexgpt-lite/latest",
		CostPer1MIn:        1.639,
		CostPer1MInCached:  1.639,
		CostPer1MOutCached: 1.639,
		CostPer1MOut:       1.639,
		ContextWindow:      32_768,
		DefaultMaxTokens:   8192,
		UseLegacyMaxTokens: true,
		CanReason:          true,
	},
	YCDeepSeekV32: {
		ID:                 YCDeepSeekV32,
		Name:               "YandexCloud: DeepSeek V3.2",
		Provider:           ProviderYandexCloud,
		APIModel:           "deepseek-v32/latest",
		CostPer1MIn:        4.098,
		CostPer1MInCached:  1.066,
		CostPer1MOutCached: 1.066,
		CostPer1MOut:       6.557,
		ContextWindow:      131_072,
		DefaultMaxTokens:   32768, // 8192 with no reasoning
		UseLegacyMaxTokens: true,
		CanReason:          true,
	},
	YCQwen3235B: {
		ID:                 YCQwen3235B,
		Name:               "YandexCloud: Qwen3 235B",
		Provider:           ProviderYandexCloud,
		APIModel:           "qwen3-235b-a22b-fp8/latest",
		CostPer1MIn:        4.098,
		CostPer1MInCached:  4.098,
		CostPer1MOutCached: 4.098,
		CostPer1MOut:       4.098,
		ContextWindow:      262_144,
		DefaultMaxTokens:   32768,
		UseLegacyMaxTokens: true,
		CanReason:          true,
	},
	YCQwen35_35B: {
		ID:                 YCQwen35_35B,
		Name:               "YandexCloud: Qwen3.5 35B",
		Provider:           ProviderYandexCloud,
		APIModel:           "qwen3.5-35b-a3b-fp8/latest",
		CostPer1MIn:        1.639,
		CostPer1MInCached:  0.410,
		CostPer1MOutCached: 0.410,
		CostPer1MOut:       2.459,
		ContextWindow:      262_144,
		DefaultMaxTokens:   32768,
		UseLegacyMaxTokens: true,
		CanReason:          true,
	},
	YCGPTOss120B: {
		ID:                 YCGPTOss120B,
		Name:               "YandexCloud: GPT OSS 120B",
		Provider:           ProviderYandexCloud,
		APIModel:           "gpt-oss-120b/latest",
		CostPer1MIn:        2.459,
		CostPer1MInCached:  2.459,
		CostPer1MOutCached: 2.459,
		CostPer1MOut:       2.459,
		ContextWindow:      131_072,
		DefaultMaxTokens:   32000,
		UseLegacyMaxTokens: true,
		CanReason:          true,
	},
}

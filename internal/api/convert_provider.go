package api

import (
	"sort"
	"strings"

	"github.com/opencode-ai/opencode/internal/llm/models"
)

// providerDisplayNames maps internal provider identifiers to human-readable names.
var providerDisplayNames = map[models.ModelProvider]string{
	models.ProviderAnthropic:   "Anthropic",
	models.ProviderOpenAI:      "OpenAI",
	models.ProviderGemini:      "Google Gemini",
	models.ProviderVertexAI:    "Google Vertex AI",
	models.ProviderBedrock:     "AWS Bedrock",
	models.ProviderYandexCloud: "Yandex Cloud",
	models.ProviderLocal:       "Local",
}

// ConvertProviders groups all supported models by their provider and returns
// a sorted slice of APIProvider values.
func ConvertProviders() []APIProvider {
	// Group models by provider.
	grouped := make(map[models.ModelProvider]map[string]APIModelInfo)
	for _, m := range models.SupportedModels {
		providerModels, ok := grouped[m.Provider]
		if !ok {
			providerModels = make(map[string]APIModelInfo)
			grouped[m.Provider] = providerModels
		}
		providerModels[string(m.ID)] = ConvertModelInfo(m)
	}

	// Build provider list.
	providers := make([]APIProvider, 0, len(grouped))
	for provider, providerModels := range grouped {
		providers = append(providers, APIProvider{
			ID:     string(provider),
			Name:   resolveProviderName(provider),
			Models: providerModels,
		})
	}

	// Sort by popularity (lower number = more popular = first).
	sort.Slice(providers, func(i, j int) bool {
		pi := models.ProviderPopularity[models.ModelProvider(providers[i].ID)]
		pj := models.ProviderPopularity[models.ModelProvider(providers[j].ID)]
		if pi == pj {
			return providers[i].ID < providers[j].ID
		}
		return pi < pj
	})

	return providers
}

// ConvertModelInfo converts a single internal Model to the external API format.
func ConvertModelInfo(m models.Model) APIModelInfo {
	return APIModelInfo{
		ID:         string(m.ID),
		Name:       m.Name,
		ProviderID: string(m.Provider),
		Limit: APIModelLimit{
			Context: int(m.ContextWindow),
			Output:  int(m.DefaultMaxTokens),
		},
		Attachment: m.SupportsAttachments,
		Reasoning:  m.CanReason,
	}
}

// resolveProviderName returns a display name for the provider, falling back
// to a title-cased version of the provider ID if no explicit mapping exists.
func resolveProviderName(provider models.ModelProvider) string {
	if name, ok := providerDisplayNames[provider]; ok {
		return name
	}
	// Fallback: capitalize the first letter.
	s := string(provider)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

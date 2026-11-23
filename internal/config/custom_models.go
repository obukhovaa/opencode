package config

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/logging"
)

// ErrInvalidCustomModel is returned by Load for a customModels entry that
// fails validateCustomModels.
var ErrInvalidCustomModel = errors.New("invalid custom model")

// CustomModel is one `customModels` entry: a model defined in config rather
// than compiled into the catalog, so a deployment can add one without a
// release (docs/providers.md, "Config-defined models"). Field names mirror
// models.Model in camelCase; viper matches them case-insensitively.
type CustomModel struct {
	ID                              string  `json:"id"`
	Name                            string  `json:"name,omitempty"`
	Provider                        string  `json:"provider"`
	Wire                            string  `json:"wire"`
	APIModel                        string  `json:"apiModel"`
	CostPer1MIn                     float64 `json:"costPer1MIn"`
	CostPer1MOut                    float64 `json:"costPer1MOut"`
	CostPer1MInCached               float64 `json:"costPer1MInCached,omitempty"`
	CostPer1MOutCached              float64 `json:"costPer1MOutCached,omitempty"`
	ContextWindow                   int64   `json:"contextWindow"`
	DefaultMaxTokens                int64   `json:"defaultMaxTokens"`
	CanReason                       bool    `json:"canReason,omitempty"`
	SupportsAdaptiveThinking        bool    `json:"supportsAdaptiveThinking,omitempty"`
	SupportsMaximumThinking         bool    `json:"supportsMaximumThinking,omitempty"`
	SupportsXHighThinking           bool    `json:"supportsXHighThinking,omitempty"`
	SupportsTaskBudget              bool    `json:"supportsTaskBudget,omitempty"`
	SupportsToolSearch              bool    `json:"supportsToolSearch,omitempty"`
	SupportsAttachments             bool    `json:"supportsAttachments,omitempty"`
	RejectsForcedToolChoice         bool    `json:"rejectsForcedToolChoice,omitempty"`
	LongContextThreshold            int64   `json:"longContextThreshold,omitempty"`
	LongContextCostMultiplier       float64 `json:"longContextCostMultiplier,omitempty"`
	LongContextOutputCostMultiplier float64 `json:"longContextOutputCostMultiplier,omitempty"`
	DefaultReasoningEffort          string  `json:"defaultReasoningEffort,omitempty"`
}

// CustomModelWires is the set of wire values a customModels entry accepts,
// published as the schema enum (cmd/schema).
var CustomModelWires = []string{string(models.WireAnthropicMessages), string(models.WireOpenAIChat)}

// IsCustomModelWire reports whether v is a wire a customModels entry
// accepts. Exact match, no trimming or case folding.
func IsCustomModelWire(v string) bool {
	return slices.Contains(CustomModelWires, v)
}

// validateCustomModels converts the entries into catalog models, failing on
// the first invalid one. A built-in id is not an error here;
// models.RegisterCustomModels skips it.
func validateCustomModels(list []CustomModel) ([]models.Model, error) {
	out := make([]models.Model, 0, len(list))
	seen := make(map[string]bool, len(list))
	for i, c := range list {
		m, err := c.model()
		if err != nil {
			return nil, fmt.Errorf("%w: customModels[%d] %q: %v", ErrInvalidCustomModel, i, c.ID, err)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("%w: customModels[%d]: duplicate id %q", ErrInvalidCustomModel, i, c.ID)
		}
		seen[c.ID] = true
		out = append(out, m)
	}
	return out, nil
}

func (c CustomModel) model() (models.Model, error) {
	if !models.CustomModelIDRegexp.MatchString(c.ID) {
		return models.Model{}, fmt.Errorf("id must match %s", models.CustomModelIDPattern)
	}
	if models.ModelProvider(c.Provider) != models.ProviderBedrock {
		return models.Model{}, fmt.Errorf("provider %q is not supported (only %q)", c.Provider, models.ProviderBedrock)
	}
	if !IsCustomModelWire(c.Wire) {
		return models.Model{}, fmt.Errorf("wire %q must be one of %s", c.Wire, strings.Join(CustomModelWires, ", "))
	}
	wire := models.Wire(c.Wire)
	if strings.TrimSpace(c.APIModel) == "" {
		return models.Model{}, errors.New("apiModel is required")
	}
	for _, f := range []struct {
		name  string
		value float64
	}{
		{"costPer1MIn", c.CostPer1MIn}, {"costPer1MOut", c.CostPer1MOut},
		{"costPer1MInCached", c.CostPer1MInCached}, {"costPer1MOutCached", c.CostPer1MOutCached},
		{"longContextCostMultiplier", c.LongContextCostMultiplier},
		{"longContextOutputCostMultiplier", c.LongContextOutputCostMultiplier},
	} {
		if f.value < 0 || math.IsNaN(f.value) || math.IsInf(f.value, 0) {
			return models.Model{}, fmt.Errorf("%s must be a non-negative number", f.name)
		}
	}
	if c.CostPer1MIn == 0 || c.CostPer1MOut == 0 {
		return models.Model{}, errors.New("costPer1MIn and costPer1MOut must be positive")
	}
	if c.ContextWindow <= 0 || c.DefaultMaxTokens <= 0 || c.DefaultMaxTokens > c.ContextWindow {
		return models.Model{}, errors.New("contextWindow and defaultMaxTokens must be positive, defaultMaxTokens at most contextWindow")
	}
	if !c.CanReason && (c.SupportsAdaptiveThinking || c.SupportsMaximumThinking || c.SupportsXHighThinking || c.SupportsTaskBudget) {
		return models.Model{}, errors.New("thinking flags need canReason")
	}
	if wire == models.WireOpenAIChat && (c.SupportsAdaptiveThinking || c.SupportsTaskBudget || c.SupportsToolSearch || c.RejectsForcedToolChoice) {
		return models.Model{}, errors.New("supportsAdaptiveThinking, supportsTaskBudget, supportsToolSearch and rejectsForcedToolChoice apply to the anthropic-messages wire only")
	}
	if c.LongContextThreshold < 0 || (c.LongContextThreshold > 0) != (c.LongContextCostMultiplier > 0) ||
		(c.LongContextOutputCostMultiplier > 0 && c.LongContextThreshold == 0) {
		return models.Model{}, errors.New("longContextThreshold and longContextCostMultiplier must be set together (longContextOutputCostMultiplier needs both)")
	}
	name := c.Name
	if name == "" {
		name = c.ID
	}
	m := models.Model{
		ID:                              models.ModelID(c.ID),
		Name:                            name,
		Provider:                        models.ProviderBedrock,
		APIModel:                        c.APIModel,
		Wire:                            wire,
		CostPer1MIn:                     c.CostPer1MIn,
		CostPer1MOut:                    c.CostPer1MOut,
		CostPer1MInCached:               c.CostPer1MInCached,
		CostPer1MOutCached:              c.CostPer1MOutCached,
		ContextWindow:                   c.ContextWindow,
		DefaultMaxTokens:                c.DefaultMaxTokens,
		CanReason:                       c.CanReason,
		SupportsAdaptiveThinking:        c.SupportsAdaptiveThinking,
		SupportsMaximumThinking:         c.SupportsMaximumThinking,
		SupportsXHighThinking:           c.SupportsXHighThinking,
		SupportsTaskBudget:              c.SupportsTaskBudget,
		SupportsToolSearch:              c.SupportsToolSearch,
		SupportsAttachments:             c.SupportsAttachments,
		RejectsForcedToolChoice:         c.RejectsForcedToolChoice,
		LongContextThreshold:            c.LongContextThreshold,
		LongContextCostMultiplier:       c.LongContextCostMultiplier,
		LongContextOutputCostMultiplier: c.LongContextOutputCostMultiplier,
		DefaultReasoningEffort:          strings.ToLower(c.DefaultReasoningEffort),
	}
	if err := models.ValidateReasoningEffort(m, m.DefaultReasoningEffort); err != nil {
		return models.Model{}, fmt.Errorf("defaultReasoningEffort: %v", err)
	}
	return m, nil
}

// registerCustomModels validates cfg.CustomModels and registers them into
// the catalog, warning for each one a built-in id shadows.
func registerCustomModels(c *Config) error {
	if len(c.CustomModels) == 0 {
		models.UnregisterCustomModels()
		return nil
	}
	list, err := validateCustomModels(c.CustomModels)
	if err != nil {
		return err
	}
	for _, id := range models.RegisterCustomModels(list) {
		logging.Warn("customModels entry has a built-in model's id; the built-in definition is kept", "model", id)
	}
	return nil
}

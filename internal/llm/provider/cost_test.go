package provider

import (
	"math"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
)

func TestCalculateCost(t *testing.T) {
	tests := []struct {
		name       string
		model      models.Model
		usage      TokenUsage
		wantInput  float64
		wantOutput float64
	}{
		{
			name:       "flat pricing ignores prompt length",
			model:      models.SupportedModels[models.Claude45Haiku],
			usage:      TokenUsage{InputTokens: 300_000, OutputTokens: 1_000},
			wantInput:  0.3,
			wantOutput: 0.005,
		},
		{
			name:  "haiku 5.5 at the threshold stays on base rates",
			model: models.SupportedModels[models.Claude55Haiku],
			usage: TokenUsage{
				InputTokens:         40_000,
				CacheCreationTokens: 10_000,
				CacheReadTokens:     50_000,
				OutputTokens:        2_000,
			},
			wantInput:  0.00575,
			wantOutput: 0.001,
		},
		{
			name:  "haiku 5.5 over the threshold reprices the whole request",
			model: models.SupportedModels[models.Claude55Haiku],
			usage: TokenUsage{
				InputTokens:         40_001,
				CacheCreationTokens: 10_000,
				CacheReadTokens:     50_000,
				OutputTokens:        2_000,
			},
			wantInput:  0.0287505,
			wantOutput: 0.005,
		},
		{
			name:  "bedrock EU haiku 5.5 stacks the tier on the regional premium",
			model: models.SupportedModels[models.BedrockEUHaiku55],
			usage: TokenUsage{
				InputTokens:         40_001,
				CacheCreationTokens: 10_000,
				CacheReadTokens:     50_000,
				OutputTokens:        2_000,
			},
			wantInput:  0.03162555,
			wantOutput: 0.0055,
		},
		{
			name:       "sonnet 5.5 cache reads cost 0.05x input",
			model:      models.SupportedModels[models.Claude55Sonnet],
			usage:      TokenUsage{CacheReadTokens: 1_000_000},
			wantInput:  0.10,
			wantOutput: 0,
		},
		{
			name:       "a threshold without a multiplier keeps flat pricing",
			model:      models.Model{CostPer1MIn: 1, CostPer1MOut: 5, LongContextThreshold: 100_000},
			usage:      TokenUsage{InputTokens: 200_000, OutputTokens: 1_000},
			wantInput:  0.2,
			wantOutput: 0.005,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotInput, gotOutput := CalculateCost(tt.model, tt.usage)
			if math.Abs(gotInput-tt.wantInput) > 1e-12 {
				t.Errorf("input cost = %v, want %v", gotInput, tt.wantInput)
			}
			if math.Abs(gotOutput-tt.wantOutput) > 1e-12 {
				t.Errorf("output cost = %v, want %v", gotOutput, tt.wantOutput)
			}
		})
	}
}

// A response that ran a server-side tool search sums its passes; the
// long-context tier is a pass's prompt, so a summed report over the
// threshold whose last pass is under it stays on base rates.
func TestCalculateCost_SummedPassesUnderTheThreshold(t *testing.T) {
	model := models.SupportedModels[models.Claude55Haiku]
	summed := TokenUsage{InputTokens: 600, CacheReadTokens: 120_000, OutputTokens: 500}
	lastPass := TokenUsage{InputTokens: 300, CacheReadTokens: 60_000, OutputTokens: 500}
	base, _ := CalculateCost(model, summed)
	summed.LastPass = &lastPass
	got, _ := CalculateCost(model, summed)
	if got >= base {
		t.Errorf("input cost with a last pass under the threshold = %v, want below the long-context price %v", got, base)
	}
	want := model.CostPer1MOutCached/1e6*120_000 + model.CostPer1MIn/1e6*600
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("input cost = %v, want the billed counts at base rates %v", got, want)
	}
}

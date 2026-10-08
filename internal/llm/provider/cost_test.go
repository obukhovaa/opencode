package provider

import (
	"math"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
)

func TestCalculateCost(t *testing.T) {
	tests := []struct {
		name       string
		model      models.ModelID
		usage      TokenUsage
		wantInput  float64
		wantOutput float64
	}{
		{
			name:       "flat pricing ignores prompt length",
			model:      models.Claude45Haiku,
			usage:      TokenUsage{InputTokens: 300_000, OutputTokens: 1_000},
			wantInput:  0.3,
			wantOutput: 0.005,
		},
		{
			name:  "haiku 5.5 at the threshold stays on base rates",
			model: models.Claude55Haiku,
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
			model: models.Claude55Haiku,
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
			model: models.BedrockEUHaiku55,
			usage: TokenUsage{
				InputTokens:         40_001,
				CacheCreationTokens: 10_000,
				CacheReadTokens:     50_000,
				OutputTokens:        2_000,
			},
			wantInput:  0.03162555,
			wantOutput: 0.0055,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotInput, gotOutput := CalculateCost(models.SupportedModels[tt.model], tt.usage)
			if math.Abs(gotInput-tt.wantInput) > 1e-12 {
				t.Errorf("input cost = %v, want %v", gotInput, tt.wantInput)
			}
			if math.Abs(gotOutput-tt.wantOutput) > 1e-12 {
				t.Errorf("output cost = %v, want %v", gotOutput, tt.wantOutput)
			}
		})
	}
}

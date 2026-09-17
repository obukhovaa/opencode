package models

import (
	"errors"
	"testing"
)

func TestIsReasoningEffort(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"low", true},
		{"medium", true},
		{"high", true},
		{"xhigh", true},
		{"max", true},
		{"HIGH", true},
		{"XHigh", true},
		{"", false},
		{"extreme", false},
		{"high ", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := IsReasoningEffort(tt.in); got != tt.want {
				t.Errorf("IsReasoningEffort(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidateReasoningEffort(t *testing.T) {
	noReasoning := Model{ID: "plain", CanReason: false}
	openAIStyle := Model{ID: "o-style", Provider: ProviderOpenAI, CanReason: true}
	adaptive := Model{ID: "adaptive", Provider: ProviderBedrock, CanReason: true, SupportsAdaptiveThinking: true}
	adaptiveMax := Model{ID: "adaptive-max", Provider: ProviderBedrock, CanReason: true, SupportsAdaptiveThinking: true, SupportsMaximumThinking: true}
	adaptiveAll := Model{ID: "adaptive-all", Provider: ProviderBedrock, CanReason: true, SupportsAdaptiveThinking: true, SupportsMaximumThinking: true, SupportsXHighThinking: true}

	tests := []struct {
		name    string
		model   Model
		effort  string
		wantErr bool
	}{
		{"empty is always legal (no reasoning)", noReasoning, "", false},
		{"empty is always legal (adaptive)", adaptiveAll, "", false},
		{"unknown level", adaptiveAll, "extreme", true},
		{"non-reasoning model rejects any effort", noReasoning, "low", true},
		{"openai-style low", openAIStyle, "low", false},
		{"openai-style high", openAIStyle, "high", false},
		{"openai-style rejects xhigh", openAIStyle, "xhigh", true},
		{"openai-style rejects max", openAIStyle, "max", true},
		{"adaptive medium", adaptive, "medium", false},
		{"adaptive without flag rejects xhigh", adaptive, "xhigh", true},
		{"adaptive without flag rejects max", adaptive, "max", true},
		{"adaptive max flag accepts max", adaptiveMax, "max", false},
		{"adaptive max flag still rejects xhigh", adaptiveMax, "xhigh", true},
		{"adaptive all accepts xhigh", adaptiveAll, "xhigh", false},
		{"adaptive all accepts max", adaptiveAll, "max", false},
		{"case-insensitive", adaptiveAll, "XHIGH", false},
		// Real catalog entries: the bedrock 4.6 opus has max but not xhigh,
		// 4.7 opus has both.
		{"catalog bedrock eu opus 4.6 rejects xhigh", SupportedModels[BedrockEUOpus46], "xhigh", true},
		{"catalog bedrock eu opus 4.6 accepts max", SupportedModels[BedrockEUOpus46], "max", false},
		{"catalog bedrock eu opus 4.7 accepts xhigh", SupportedModels[BedrockEUOpus47], "xhigh", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateReasoningEffort(tt.model, tt.effort)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateReasoningEffort(%s, %q) error = %v, wantErr %v", tt.model.ID, tt.effort, err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidReasoningEffort) {
				t.Errorf("error %v is not ErrInvalidReasoningEffort", err)
			}
		})
	}
}

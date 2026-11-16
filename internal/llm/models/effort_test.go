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
	// reasoning_effort-style model whose top level is max (Kimi K3 on Bedrock).
	effortMax := Model{ID: "effort-max", Provider: ProviderBedrock, CanReason: true, SupportsMaximumThinking: true}

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
		// The flags decide on their own: max does not require adaptive thinking.
		{"max flag without adaptive accepts max", effortMax, "max", false},
		{"max flag without adaptive rejects xhigh", effortMax, "xhigh", true},
		{"max flag without adaptive accepts medium", effortMax, "medium", false},
		// Kimi K3 on Bedrock: reasoning_effort takes max (its default) but not xhigh.
		{"catalog bedrock kimi k3 accepts max", SupportedModels[BedrockKimiK3], "max", false},
		{"catalog bedrock kimi k3 accepts medium", SupportedModels[BedrockKimiK3], "medium", false},
		{"catalog bedrock us kimi k3 rejects xhigh", SupportedModels[BedrockUSKimiK3], "xhigh", true},
		// Kimi K3.1 on Bedrock takes every level up to max.
		{"catalog bedrock kimi k3.1 accepts xhigh", SupportedModels[BedrockKimiK31], "xhigh", false},
		{"catalog bedrock us kimi k3.1 accepts max", SupportedModels[BedrockUSKimiK31], "max", false},
		// GPT-6.1: Sol documents xhigh and max, Terra stops at high.
		{"catalog bedrock gpt-6.1 sol accepts xhigh", SupportedModels[BedrockGPT61Sol], "xhigh", false},
		{"catalog bedrock us gpt-6.1 sol accepts max", SupportedModels[BedrockUSGPT61Sol], "max", false},
		{"catalog bedrock gpt-6.1 terra accepts high", SupportedModels[BedrockGPT61Terra], "high", false},
		{"catalog bedrock us gpt-6.1 terra rejects xhigh", SupportedModels[BedrockUSGPT61Terra], "xhigh", true},
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

func TestClampReasoningEffort(t *testing.T) {
	noReasoning := Model{ID: "plain"}
	upToHigh := Model{ID: "high", CanReason: true}
	withMax := Model{ID: "max", CanReason: true, SupportsMaximumThinking: true}
	withXHigh := Model{ID: "xhigh", CanReason: true, SupportsXHighThinking: true}
	all := Model{ID: "all", CanReason: true, SupportsXHighThinking: true, SupportsMaximumThinking: true}

	tests := []struct {
		model  Model
		effort string
		want   string
	}{
		{all, "", ""},
		{all, "extreme", ""},
		{noReasoning, "high", ""},
		{upToHigh, "low", "low"},
		{upToHigh, "Medium", "medium"},
		{upToHigh, "xhigh", "high"},
		{upToHigh, "max", "high"},
		{withMax, "max", "max"},
		{withMax, "xhigh", "high"},
		{withXHigh, "max", "xhigh"},
		{withXHigh, "XHIGH", "xhigh"},
		{all, "max", "max"},
		{all, " xhigh ", "xhigh"},
		{SupportedModels[BedrockGPT61Terra], "xhigh", "high"},
		{SupportedModels[BedrockUSKimiK31], "max", "max"},
		{SupportedModels[BedrockSonnet55], "max", "max"},
	}
	for _, tt := range tests {
		t.Run(string(tt.model.ID)+"/"+tt.effort, func(t *testing.T) {
			got := ClampReasoningEffort(tt.model, tt.effort)
			if got != tt.want {
				t.Fatalf("ClampReasoningEffort = %q, want %q", got, tt.want)
			}
			if err := ValidateReasoningEffort(tt.model, got); err != nil {
				t.Fatalf("clamped effort %q is still invalid: %v", got, err)
			}
		})
	}
}

package config

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
)

// TestValidateAgentBedrockOpenAIWireEffort pins the effort rules for the
// OpenAI-wire Bedrock models added for GENAI-375: an unset effort resolves to
// the model's DefaultReasoningEffort, levels the model accepts pass through,
// and xhigh / max fold to high where the model lacks them.
func TestValidateAgentBedrockOpenAIWireEffort(t *testing.T) {
	tests := []struct {
		model  models.ModelID
		effort string
		want   string
	}{
		{models.BedrockKimiK31, "", "max"},
		{models.BedrockUSKimiK31, "", "max"},
		{models.BedrockKimiK31, "xhigh", "xhigh"},
		{models.BedrockUSKimiK31, "medium", "medium"},
		{models.BedrockGPT61Sol, "", "medium"},
		{models.BedrockUSGPT61Sol, "xhigh", "xhigh"},
		{models.BedrockGPT61Sol, "max", "max"},
		{models.BedrockGPT61Terra, "", "medium"},
		{models.BedrockUSGPT61Terra, "low", "low"},
		{models.BedrockGPT61Terra, "xhigh", "high"},
		{models.BedrockUSGPT61Terra, "max", "high"},
	}
	for _, tt := range tests {
		t.Run(string(tt.model)+"/"+tt.effort, func(t *testing.T) {
			clearProviderEnv(t)
			c := &Config{
				Agents: map[AgentName]Agent{
					AgentCoder: {Model: tt.model, ReasoningEffort: tt.effort},
				},
				Providers: map[models.ModelProvider]Provider{
					models.ProviderBedrock: {APIKey: "test-key"},
				},
			}
			if err := validateAgent(c, AgentCoder, c.Agents[AgentCoder]); err != nil {
				t.Fatalf("validateAgent: %v", err)
			}
			if got := c.Agents[AgentCoder].ReasoningEffort; got != tt.want {
				t.Fatalf("effort = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUnsetReasoningEffortDefault: Kimi K3 everywhere resolves to max,
// OpenAI-wire Bedrock models to their DefaultReasoningEffort, and an
// Anthropic-wire model stays empty even with a DefaultReasoningEffort — the
// Anthropic client applies that per turn, and a value resolved into config
// would follow the agent onto another model under a flow step override.
func TestUnsetReasoningEffortDefault(t *testing.T) {
	tests := []struct {
		model models.ModelID
		want  string
	}{
		{models.KimiK3, "max"},
		{models.BedrockKimiK3, "max"},
		{models.BedrockUSKimiK31, "max"},
		{models.BedrockGPT61Sol, "medium"},
		{models.BedrockHaiku55, ""},
		{models.KimiK27Code, ""},
		{models.BedrockOpus48, ""},
	}
	for _, tt := range tests {
		t.Run(string(tt.model), func(t *testing.T) {
			if got := unsetReasoningEffortDefault(models.SupportedModels[tt.model]); got != tt.want {
				t.Fatalf("unsetReasoningEffortDefault = %q, want %q", got, tt.want)
			}
		})
	}
}

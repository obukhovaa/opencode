---
name: add-model
description: Add or remove LLM model definitions for providers (Anthropic, OpenAI, Gemini, VertexAI, Bedrock). Use when the user provides a model card URL, documentation link, or plain text description of a new model to register. Also use when asked to remove/deprecate an existing model. Handles model struct definition, provider registration, README update, and schema regeneration.
---

# Add/Remove Model Skill

## Overview

Add a new model definition to the opencode project, or remove a deprecated one. Models live in `internal/llm/models/` with one file per provider.

## Model Struct Fields

Every model must populate these fields in `Model{}`:

```go
Model{
    ID:                       ModelID,       // e.g. "claude-4.5-sonnet[1m]"
    Name:                     string,        // Human-readable, e.g. "Claude 4.5 Sonnet [1M]"
    Provider:                 ModelProvider, // e.g. ProviderAnthropic
    APIModel:                 string,        // API model identifier sent to provider
    CostPer1MIn:              float64,       // Cost per 1M input tokens (USD)
    CostPer1MOut:             float64,       // Cost per 1M output tokens (USD)
    CostPer1MInCached:        float64,       // Cost per 1M cached input tokens
    CostPer1MOutCached:       float64,       // Cost per 1M cached output tokens
    ContextWindow:            int64,         // Max context window size in tokens
    DefaultMaxTokens:         int64,         // Default max output tokens (0 if unset)
    CanReason:                bool,          // Whether model supports reasoning/thinking
    SupportsAdaptiveThinking: bool,          // Whether model supports adaptive thinking (reasoningEffort)
    SupportsMaximumThinking:  bool,          // Whether model supports "max" reasoning effort
    SupportsAttachments:      bool,          // Whether model supports file attachments
}
```

## Required Information from Model Card

Extract these from the model card/documentation. If any are missing, ask the user specifically:

1. **API model name** (the string sent in API calls)
2. **Pricing**: input cost, output cost, cached input cost, cached output cost (per 1M tokens)
3. **Context window** size (in tokens)
4. **Max output tokens** (default)
5. **Reasoning support**: does it support thinking/reasoning? Adaptive thinking? Maximum thinking?
6. **Attachments**: does it support file/image attachments?

For VertexAI models: the API model name format differs (e.g. `claude-sonnet-4-5@20250929` instead of `claude-sonnet-4-5`). The model card usually provides this.

## Adding a Model — Step by Step

### 1. Determine the provider file

| Provider | File | Map variable | Provider const |
|----------|------|-------------|----------------|
| Anthropic | `anthropic.go` | `AnthropicModels` | `ProviderAnthropic` |
| OpenAI | `openai.go` | `OpenAIModels` | `ProviderOpenAI` |
| Gemini | `gemini.go` | `GeminiModels` | `ProviderGemini` |
| VertexAI (Gemini) | `vertexai.go` | `VertexAIGeminiModels` | `ProviderVertexAI` |
| VertexAI (Anthropic) | `vertexai.go` | `VertexAIAnthropicModels` | `ProviderVertexAI` |
| Bedrock | `models.go` | `SupportedModels` (inline) | `ProviderBedrock` |

### 2. Add the ModelID constant

Add a new `ModelID` constant in the provider file's `const` block. Follow naming conventions:
- Anthropic: `Claude{Version}{Variant}` e.g. `Claude45Sonnet1M`
- OpenAI: model name e.g. `GPT5`, `O4Mini`
- Gemini: `Gemini{Version}{Variant}` e.g. `Gemini30Pro`
- VertexAI: prefix with `VertexAI` e.g. `VertexAISonnet45M`
- The string value uses the provider prefix: `"vertexai.claude-sonnet-4-5-m"`, `"claude-4-5-sonnet[1m]"`, `"gemini-3.0-pro"`

### 3. Add the model definition to the map

Add entry to the appropriate map variable. For VertexAI models that mirror Anthropic/Gemini models, reference the base model's costs:

```go
VertexAINewModel: {
    ID:               VertexAINewModel,
    Name:             "VertexAI: Model Name",
    Provider:         ProviderVertexAI,
    APIModel:         "model-api-name@version",
    CostPer1MIn:      AnthropicModels[BaseModelID].CostPer1MIn,
    // ... reference base model for shared fields
},
```

### 4. Verify bootstrap registration

Check `bootstrap.go` — the `init()` function must copy the model's map into `SupportedModels`. Existing maps are already registered. Only add a new `maps.Copy()` line if you created a new map variable.

### 5. Update README.md

Update the "Supported Models" table at line ~287 in `README.md`. Add the new model name to the appropriate provider row.

### 6. Regenerate schema

Run: `go run cmd/schema/main.go > opencode-schema.json`

This picks up new model IDs automatically via `models.SupportedModels` iteration.

### 7. Build check

Run: `go build ./...` to verify compilation.

## Removing a Model

Reverse the add steps:
1. Remove the `ModelID` constant from the provider file
2. Remove the model entry from the map
3. Remove any VertexAI/Bedrock mirrors that reference the removed model
4. Update README.md to remove the model name
5. Regenerate schema: `go run cmd/schema/main.go > opencode-schema.json`
6. Build check: `go build ./...`

## Output

After completing, provide a brief summary:
- Model name and ID
- Provider
- Key specs (context window, pricing tier, reasoning support)
- Whether it was added or removed

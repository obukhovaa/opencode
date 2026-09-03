package cmd

import (
	"context"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/langfuse"
	"github.com/opencode-ai/opencode/internal/logging"
)

// initLangfusePrompts brings up Langfuse Prompt Management, if configured,
// and optionally pre-fetches every prompt the flow and agent registries
// reference.
//
// Called from both entry points (cmd/root.go and cmd/serve.go) next to the
// tracing Init, but gated separately: prompt management resolves the same
// credentials yet is its own capability, so a deployment can manage prompts
// without shipping traces, or vice versa.
func initLangfusePrompts(cfg *config.Config) {
	if cfg == nil || cfg.Telemetry == nil || cfg.Telemetry.Langfuse == nil {
		return
	}
	lf := cfg.Telemetry.Langfuse
	if lf.Prompts == nil || !lf.Prompts.Enabled {
		return
	}

	// Durations were validated at config load; a parse error here can only
	// mean the config was mutated afterwards, so fall back to the defaults
	// rather than taking the process down over a telemetry setting.
	cacheTTL, err := lf.Prompts.CacheTTLDuration()
	if err != nil {
		logging.Warn("langfuse: ignoring invalid prompts.cacheTTL", "error", err)
	}
	timeout, err := lf.Prompts.TimeoutDuration()
	if err != nil {
		logging.Warn("langfuse: ignoring invalid prompts.timeout", "error", err)
	}

	ok := langfuse.InitPrompts(lf.PublicKey, lf.SecretKey, lf.BaseURL, langfuse.PromptOptions{
		DefaultLabel: lf.Prompts.Label,
		CacheTTL:     cacheTTL,
		Timeout:      timeout,
	})
	if !ok {
		logging.Warn("Langfuse prompts enabled in config but credentials resolved to empty — prompt references will fail")
		return
	}
	logging.Info("Langfuse prompt management enabled",
		"url", lf.BaseURL, "label", langfuse.GetPrompts().DefaultLabel())

	if !lf.Prompts.WarmupEnabled() {
		return
	}
	// Bounded so a slow or unreachable Langfuse cannot stretch startup:
	// every reference that misses here is simply fetched at use time.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	langfuse.GetPrompts().Warm(ctx, collectPromptRefs())
}

// collectPromptRefs gathers every Langfuse prompt reference declared by a
// discovered flow step or a registered agent type.
func collectPromptRefs() []langfuse.PromptRef {
	var refs []langfuse.PromptRef
	for _, f := range flow.All() {
		for _, step := range f.Spec.Steps {
			if step.LangfusePromptPath != "" {
				refs = append(refs, langfuse.PromptRef{
					Path:  step.LangfusePromptPath,
					Label: step.LangfusePromptLabel,
				})
			}
		}
	}
	for _, a := range agentregistry.GetRegistry().List() {
		if a.LangfusePromptPath != "" {
			refs = append(refs, langfuse.PromptRef{
				Path:  a.LangfusePromptPath,
				Label: a.LangfusePromptLabel,
			})
		}
	}
	return refs
}

package flow

import (
	"encoding/json"

	"github.com/opencode-ai/opencode/internal/config"
)

// A job arg that any MCP server maps through flowArgHeaders is a credential in
// all but name. config.RedactedFlowArgs names them; this file is where the
// flow engine keeps them out of what it stores and what it renders.

// marshalPersistedArgs encodes args for flow_states.args, leaving out every
// redacted arg. A resumed step takes those from the args the orchestrator
// supplies to the run, never from the row.
func marshalPersistedArgs(args map[string]any) ([]byte, error) {
	redacted := config.RedactedFlowArgs()
	if len(redacted) == 0 {
		return json.Marshal(args)
	}
	kept := make(map[string]any, len(args))
	for k, v := range args {
		if _, drop := redacted[k]; !drop {
			kept[k] = v
		}
	}
	return json.Marshal(kept)
}

// withRedactedArgs copies the redacted args of src (the current run's args)
// onto dst, the args of a step re-entered from a persisted row that does not
// hold them.
func withRedactedArgs(dst, src map[string]any) map[string]any {
	redacted := config.RedactedFlowArgs()
	if len(redacted) == 0 {
		return dst
	}
	if dst == nil {
		dst = map[string]any{}
	}
	for name := range redacted {
		if v, ok := src[name]; ok {
			dst[name] = v
		}
	}
	return dst
}

// promptArgs returns args as a prompt may see them: every redacted arg is the
// literal config.RedactedFlowArgPlaceholder, present in args or not, so a
// step resumed from a row renders the same prompt as the original.
func promptArgs(args map[string]any) map[string]any {
	redacted := config.RedactedFlowArgs()
	if len(redacted) == 0 {
		return args
	}
	out := make(map[string]any, len(args)+len(redacted))
	for k, v := range args {
		out[k] = v
	}
	for name := range redacted {
		out[name] = config.RedactedFlowArgPlaceholder
	}
	return out
}

// substitutePrompt is substituteScoped for text that reaches the model —
// inline step prompts and Langfuse-hosted ones alike. Routing and session
// naming keep the real values.
func substitutePrompt(template string, args, stepVars map[string]any) string {
	return substituteScoped(template, promptArgs(args), stepVars)
}

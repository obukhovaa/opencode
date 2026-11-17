package agent

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/opencode-ai/opencode/internal/config"
)

type mcpDiagnosticsKey struct{}

type mcpDiagnostics struct{ restricted atomic.Bool }

// Server stderr has no call identity. Restrict the entire shared client once
// it can carry composition data, including ordinary calls on that client.
func compositionServerDiagnostics(name string) bool {
	cfg := config.Get()
	if cfg == nil || cfg.MCPComposition == nil {
		return false
	}
	for _, endpoint := range cfg.MCPComposition.ReadTools {
		// Ambiguous server-name prefixes are restricted conservatively. This
		// controls diagnostics only; exact resolved tools still govern access.
		if strings.HasPrefix(endpoint, name+"_") {
			return true
		}
	}
	return false
}

func mcpDiagnosticsFromContext(ctx context.Context, name string) *mcpDiagnostics {
	if policy, ok := ctx.Value(mcpDiagnosticsKey{}).(*mcpDiagnostics); ok {
		return policy
	}
	policy := &mcpDiagnostics{}
	policy.restricted.Store(compositionServerDiagnostics(name))
	return policy
}

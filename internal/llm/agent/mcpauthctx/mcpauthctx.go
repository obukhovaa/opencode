// Package mcpauthctx threads per-flow-run MCP Authorization overrides
// through context.Context (openspec change agent-pod-pool-runtime,
// design D1).
//
// A pool-mode pod serves many flow runs over its lifetime, each with its
// own job-scoped MCP bearer token delivered in the POST /flow body. The
// token must apply only to MCP calls made under that run's context —
// mutating the shared config.MCPServers map would race concurrent tool
// calls and leak the token into later runs. Instead the flow runner
// stamps the override onto the run context with WithAuthOverride, and
// mcpRegistry.StartClient layers it on top of the server's static
// headers per call via AuthOverrideFromContext.
//
// The package deliberately has no dependencies beyond the standard
// library so both internal/api (the flow runner) and internal/llm/agent
// (the MCP registry) can import it without cycles.
package mcpauthctx

import "context"

// ctxKey is the private context key carrying the override map.
type ctxKey struct{}

// WithAuthOverride returns a context carrying an Authorization override
// for the named MCP server. headerValue is the full header value (e.g.
// "Bearer <token>"). Overrides accumulate copy-on-write: a derived
// context with an override for server B keeps a parent's override for
// server A, and the parent context is never mutated. An empty
// serverName is a no-op (there is nothing to key the override on).
func WithAuthOverride(ctx context.Context, serverName, headerValue string) context.Context {
	if serverName == "" {
		return ctx
	}
	existing, _ := ctx.Value(ctxKey{}).(map[string]string)
	next := make(map[string]string, len(existing)+1)
	for k, v := range existing {
		next[k] = v
	}
	next[serverName] = headerValue
	return context.WithValue(ctx, ctxKey{}, next)
}

// AuthOverrideFromContext reports the Authorization override for the
// named MCP server, if the context carries one. ok is false when no
// override was set for that server — callers fall back to the server's
// static config headers.
func AuthOverrideFromContext(ctx context.Context, serverName string) (value string, ok bool) {
	m, _ := ctx.Value(ctxKey{}).(map[string]string)
	value, ok = m[serverName]
	return value, ok
}

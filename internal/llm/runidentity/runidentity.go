// Package runidentity carries the LLM and telemetry identity that
// belongs to ONE flow run on a pool pod (openspec change
// agent-pod-pool-runtime, design D10).
//
// Why a process-level holder and not a context value:
//
// The per-run MCP token uses a context override (mcpauthctx) because its
// consumer — an MCP tool CALL — always has the run's context in hand.
// The three values here do not have that luxury:
//
//   - The provider API key is consumed when the provider client is
//     CONSTRUCTED (agent.createAgentProvider), and that function takes no
//     context; agentFactory.NewAgent is handed one and discards it.
//   - provider.GetUserID is a package-level function with no context
//     parameter, reached from both the Langfuse trace builder and the
//     provider-metadata resolver.
//
// This is the same shape as MCP tool DISCOVERY, which is context-free for
// the same structural reason and is therefore served by the
// MCPRegistry.SetDiscoveryAuth seam. This package is that seam for the
// LLM key and telemetry identity, and the flow runner drives it with the
// identical publish/revert discipline (see
// flowRunner.applyRunScopedIdentity).
//
// Lifetime: set before a run's agents are built, cleared when the run
// reaches a terminal state. There is NO boot-time value here — the pod's
// static identity lives in config (telemetry.userId, providers[].apiKey)
// and is never mutated — so clearing is always safe and always correct,
// in every deployment mode. That is what makes a positive clear on a run
// that carries no identity the right default rather than a hazard.
package runidentity

import (
	"strings"
	"sync/atomic"
)

// Identity is one run's LLM and telemetry identity. A zero-valued field
// means "no override" and the config-derived value stands.
type Identity struct {
	// APIKey is the provider API key billed for this run. On the pool
	// path this is the caller's per-team LiteLLM key, replacing the
	// pod's shared boot-time key.
	APIKey string
	// UserID overrides telemetry.userId for this run's traces and
	// provider metadata.
	UserID string
	// Tags are extra trace tags. They SHADOW config tags sharing the
	// same `key:` prefix — see MergeTags.
	Tags []string
}

// current is the identity of the run in flight, or nil when no run has
// published one. An atomic pointer rather than a mutex-guarded struct so
// the read path — which sits under every LLM request — never blocks, and
// so readers always observe a whole Identity rather than a half-updated
// one.
var current atomic.Pointer[Identity]

// Set publishes id as the current run's identity; Set(nil) clears it.
// The Identity and its Tags slice are copied, so a caller may reuse or
// mutate the value it passed without affecting what readers observe.
func Set(id *Identity) {
	if id == nil {
		current.Store(nil)
		return
	}
	cp := *id
	if len(id.Tags) > 0 {
		cp.Tags = append([]string(nil), id.Tags...)
	}
	current.Store(&cp)
}

// Get returns the current run identity, or nil when none is published.
// The result MUST be treated as read-only.
func Get() *Identity { return current.Load() }

// APIKey returns the current run's provider API key override, or "".
func APIKey() string {
	if id := current.Load(); id != nil {
		return id.APIKey
	}
	return ""
}

// UserID returns the current run's telemetry user id override, or "".
func UserID() string {
	if id := current.Load(); id != nil {
		return id.UserID
	}
	return ""
}

// Tags returns a copy of the current run's extra trace tags.
func Tags() []string {
	id := current.Load()
	if id == nil || len(id.Tags) == 0 {
		return nil
	}
	return append([]string(nil), id.Tags...)
}

// MergeTags overlays run tags onto config tags, with run tags SHADOWING
// any config tag that shares their `key:` prefix.
//
// Plain concatenation would be wrong for the case that motivated this:
// a pool pod's config carries a static `team:<pool-owner>` tag from boot,
// and a run for a different team must not emit both. Shadowing by prefix
// generalises that to any namespaced tag (`env:`, `team:`, `tier:`)
// without teaching this package which keys exist.
//
// Tags with no colon are compared whole, so an unnamespaced tag shadows
// only an identical one. Order is preserved: surviving config tags first,
// then run tags.
func MergeTags(base, run []string) []string {
	if len(run) == 0 {
		return base
	}
	shadowed := make(map[string]struct{}, len(run))
	for _, t := range run {
		shadowed[tagKey(t)] = struct{}{}
	}
	out := make([]string, 0, len(base)+len(run))
	for _, t := range base {
		if _, drop := shadowed[tagKey(t)]; drop {
			continue
		}
		out = append(out, t)
	}
	return append(out, run...)
}

// tagKey returns the shadowing key of a tag: the namespace INCLUDING its
// colon for a `key:value` tag, or the whole tag when it carries no
// namespace.
//
// Keeping the colon is what stops a bare `team` from shadowing
// `team:acme`. They are different tags, and only the namespaced form
// participates in namespace shadowing; dropping the colon would make the
// two collide on the key "team".
func tagKey(tag string) string {
	if i := strings.Index(tag, ":"); i >= 0 {
		return tag[:i+1]
	}
	return tag
}

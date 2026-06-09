package service

import (
	"context"
	"fmt"
	"net/http"
	"sort"
)

// healthResponse mirrors the bridge-http-api spec's /router/health body:
// overall status plus a per-identity adapters map. The schema is also
// used as the value of the `bridge` key in opencode's /global/health
// response — see bridge.health (Phase 6.5).
type healthResponse struct {
	Status   string                      `json:"status"`
	Error    string                      `json:"error,omitempty"`
	Adapters map[string]healthAdapterRow `json:"adapters,omitempty"`
}

type healthAdapterRow struct {
	Status        string `json:"status"`
	LastError     string `json:"lastError,omitempty"`
	LastInboundAt int64  `json:"lastInboundAt,omitempty"`
	LastFailureAt int64  `json:"lastFailureAt,omitempty"`
	BoundSessions int    `json:"boundSessions"`
}

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.healthSnapshot(r.Context()))
}

// HealthSnapshot satisfies api.HealthReporter so the main /global/health
// endpoint can embed the bridge state under the `bridge` key, per the
// bridge-http-api spec's "Extended /health" requirement.
func (s *Service) HealthSnapshot(r *http.Request) any {
	return s.healthSnapshot(r.Context())
}

// BridgeBanner returns a short summary suitable for the API server's
// startup banner. Satisfies api.BannerProvider. Lines are pre-formatted
// "<channel>:<identity>  <status>  <count> sessions" rows ready to
// print. Returns "disabled" + empty lines when no channel is enabled
// — the caller (api.Server.Start) chooses how to render that.
func (s *Service) BridgeBanner(ctx context.Context) (string, []string) {
	snap := s.healthSnapshot(ctx)
	if len(snap.Adapters) == 0 {
		return snap.Status, nil
	}
	// Stable, alphabetical ordering so the banner doesn't reshuffle
	// between restarts.
	keys := make([]string, 0, len(snap.Adapters))
	for k := range snap.Adapters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		row := snap.Adapters[k]
		noun := "sessions"
		if row.BoundSessions == 1 {
			noun = "session"
		}
		lines = append(lines, fmt.Sprintf("%-24s %-10s %d %s", k, row.Status, row.BoundSessions, noun))
	}
	return snap.Status, lines
}

// healthSnapshot computes the current bridge health state. Exposed for
// the api server's /global/health handler to embed.
func (s *Service) healthSnapshot(ctx context.Context) healthResponse {
	if s == nil {
		return healthResponse{Status: "disabled"}
	}
	if s.cfg == nil || !s.cfg.AnyChannelEnabled() {
		return healthResponse{Status: "disabled"}
	}
	s.mu.Lock()
	adapters := make(map[string]healthAdapterRow, len(s.adapters))
	keys := make([]string, 0, len(s.adapters))
	for k := range s.adapters {
		keys = append(keys, k)
	}
	s.mu.Unlock()

	overall := "ok"
	for _, k := range keys {
		adapter := s.Adapter(splitAdapterKey(k))
		if adapter == nil {
			continue
		}
		st := adapter.Status()
		row := healthAdapterRow{
			Status:        st.Status,
			LastError:     st.LastError,
			LastInboundAt: st.LastInboundAt,
			LastFailureAt: st.LastFailureAt,
		}
		// Count bindings — best-effort; failures fall back to 0.
		if count, err := s.store.CountBindingsByIdentity(ctx, s.projectID, adapter.Channel(), adapter.Identity()); err == nil {
			row.BoundSessions = count
		}
		adapters[k] = row
		// Aggregate worst status as overall: error > degraded > disabled > running.
		overall = worstStatus(overall, row.Status)
	}
	return healthResponse{Status: overall, Adapters: adapters}
}

// worstStatus returns the more-severe of two adapter statuses.
func worstStatus(a, b string) string {
	rank := func(s string) int {
		switch s {
		case "error":
			return 3
		case "degraded":
			return 2
		case "disabled":
			return 1
		case "running":
			return 0
		}
		return 0
	}
	if rank(a) >= rank(b) {
		return a
	}
	return b
}

// splitAdapterKey inverts adapterKey("channel:identity").
func splitAdapterKey(k string) (string, string) {
	for i := 0; i < len(k); i++ {
		if k[i] == ':' {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

package service

import (
	"context"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/logging"
)

const (
	// requesterLookupTimeout bounds the platform lookup so a slow API
	// never delays the turn by more than this; on timeout the raw author
	// id is used.
	requesterLookupTimeout = 3 * time.Second
	// requesterCacheTTL is how long a resolved (or unresolvable) author
	// stays cached. Long enough that a busy thread costs one lookup, short
	// enough that a scope fixed on the platform app shows up the same day.
	requesterCacheTTL = time.Hour
	// requesterFailureTTL is how long a failed lookup is remembered. A
	// permanent error (missing_scope, user_not_found) would otherwise cost
	// a platform call, a WARN and up to requesterLookupTimeout on every
	// message; short so a transient one (rate limit, timeout) clears soon.
	requesterFailureTTL = 5 * time.Minute
)

type requesterCacheEntry struct {
	email   string
	expires time.Time
}

// requesterCache maps "channel:identity:authorID" to the author's resolved
// email. Unresolvable authors and failed lookups are cached with an empty
// email so a missing scope does not cost a platform call per message.
type requesterCache struct {
	mu      sync.Mutex
	entries map[string]requesterCacheEntry
	now     func() time.Time
}

func newRequesterCache() *requesterCache {
	return &requesterCache{entries: make(map[string]requesterCacheEntry), now: time.Now}
}

func (c *requesterCache) get(key string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || c.now().After(e.expires) {
		return "", false
	}
	return e.email, true
}

func (c *requesterCache) put(key, email string, ttl time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = requesterCacheEntry{email: email, expires: c.now().Add(ttl)}
}

// requesterFor returns the identity to attribute an inbound turn to: the
// author's email when the adapter can resolve it, else the raw AuthorID.
// Returns "" when the inbound carries no author (the trace then falls back
// to telemetry.requester).
func (s *Service) requesterFor(ctx context.Context, in bridge.Inbound) string {
	if in.AuthorID == "" {
		return ""
	}
	resolver, ok := s.Adapter(in.Peer.Channel, in.Peer.Identity).(bridge.UserEmailResolver)
	if !ok {
		return in.AuthorID
	}
	key := in.Peer.Channel + ":" + in.Peer.Identity + ":" + in.AuthorID
	if email, hit := s.requesters.get(key); hit {
		return orAuthorID(email, in.AuthorID)
	}
	lookupCtx, cancel := context.WithTimeout(ctx, requesterLookupTimeout)
	defer cancel()
	email, err := resolver.ResolveUserEmail(lookupCtx, in.AuthorID)
	if err != nil {
		// Cached briefly, so the call and the WARN repeat at most once per
		// requesterFailureTTL per author.
		logging.Warn("bridge: requester lookup failed; using author id",
			"channel", in.Peer.Channel, "identity", in.Peer.Identity, "author", in.AuthorID,
			"retry_after", requesterFailureTTL, "err", err)
		s.requesters.put(key, "", requesterFailureTTL)
		return in.AuthorID
	}
	s.requesters.put(key, email, requesterCacheTTL)
	return orAuthorID(email, in.AuthorID)
}

func orAuthorID(email, authorID string) string {
	if email != "" {
		return email
	}
	return authorID
}

package service

import (
	"context"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// AdapterFactory constructs a bridge.Adapter for one configured
// identity. The bridge service uses this to start adapters at boot AND
// to hot-add new identities when an operator POSTs to
// /router/identities/{channel}.
//
// The bridge/service package does NOT import the platform adapter
// packages directly — that would force every binary that includes the
// API server to compile all three chat-platform SDKs. cmd/serve.go
// supplies a factory closure at construction time that knows how to
// build each adapter; tests substitute a stub factory.
type AdapterFactory func(ctx context.Context, channel, identityID string, cfg *bridge.Config) (bridge.Adapter, error)

// SetAdapterFactory installs the factory the orchestrator uses for
// boot-time + hot-add adapter construction. Called by cmd/serve.go after
// New() but before Start().
func (s *Service) SetAdapterFactory(f AdapterFactory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adapterFactory = f
}

// LaunchAdapter constructs an adapter via the configured factory and
// registers it with the orchestrator. Used by:
//
//   - cmd/serve.go at boot, iterating every enabled identity in
//     cfg.Router and calling LaunchAdapter for each
//   - the /router/identities/{channel} POST handler when an operator
//     adds a new identity at runtime (hot-add)
//
// Returns the same set of errors RegisterAdapter does plus any error
// the factory returns (typically token validation failure). Returns
// nil + nil if no factory is configured (the bridge is running in a
// test or special-purpose mode that skips real adapters).
func (s *Service) LaunchAdapter(ctx context.Context, channel, identityID string) error {
	s.mu.Lock()
	factory := s.adapterFactory
	s.mu.Unlock()
	if factory == nil {
		return nil
	}
	adapter, err := factory(ctx, channel, identityID, s.cfg)
	if err != nil {
		return err
	}
	if adapter == nil {
		return nil
	}
	return s.RegisterAdapter(ctx, adapter)
}

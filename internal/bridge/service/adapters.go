package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/logging"
)

// RegisterAdapter installs adapter into the orchestrator's per-identity
// adapter table, attempts to acquire the identity lock, and (on success)
// starts the adapter's background goroutine pumping inbound onto the
// shared inboundCh.
//
// Returns immediately with an error if the identity is already registered
// or the identity lock could not be acquired (another opencode instance
// owns the identity). Per the chat-bridge spec, per-identity startup
// failure MUST NOT prevent other identities from coming up — callers
// (the bridge orchestrator boot path) iterate every configured identity
// independently and surface failures via /router/health.
func (s *Service) RegisterAdapter(ctx context.Context, adapter bridge.Adapter) error {
	key := adapterKey(adapter.Channel(), adapter.Identity())

	s.mu.Lock()
	if _, exists := s.adapters[key]; exists {
		s.mu.Unlock()
		return fmt.Errorf("bridge: adapter %s already registered", key)
	}
	s.adapters[key] = adapter
	s.mu.Unlock()

	// Identity-lock acquisition is gated on whether the adapter
	// opens a platform listener. In mediated-inbound mode the
	// runner's adapter is constructed with Identity.Inbound ==
	// "disabled" (openspec change bridge-orchestrator-mediated-inbound
	// Phase A): no Socket Mode connection, no "one event = one
	// connection" race to coordinate, so the per-identity lock is
	// moot. Acquiring it here actively breaks the multi-runner-on-
	// same-identity case that mediated-inbound was designed to
	// enable — runner 1 takes the lock, runner 2 fails with
	// ErrIdentityLocked, no adapter registered, every subsequent
	// Bind on runner 2 errors with "no adapter for slack:default".
	//
	// Adapters opt out of the lock by implementing
	// AdapterInboundActiver and returning false. Adapters that don't
	// implement the interface are treated as inbound-active (today's
	// behaviour for non-mediated deployments).
	inboundActive := true
	if a, ok := adapter.(bridge.AdapterInboundActiver); ok {
		inboundActive = a.InboundActive()
	}

	var lock store.LockHandle
	if inboundActive {
		var err error
		lock, err = s.lockMgr.Lock(ctx, s.projectID, adapter.Channel(), adapter.Identity())
		if err != nil {
			s.mu.Lock()
			delete(s.adapters, key)
			s.mu.Unlock()
			if errors.Is(err, store.ErrIdentityLocked) {
				logging.Info("bridge: identity is owned by another opencode instance",
					"channel", adapter.Channel(), "identity", adapter.Identity())
			} else {
				logging.Warn("bridge: identity lock failed",
					"channel", adapter.Channel(), "identity", adapter.Identity(), "err", err)
			}
			return fmt.Errorf("bridge: lock %s: %w", key, err)
		}
	} else {
		logging.Info("bridge: adapter inbound is disabled — skipping identity lock (mediated-inbound mode)",
			"channel", adapter.Channel(), "identity", adapter.Identity())
	}

	// Adapter-side per-(channel, identity, peerKey) serialization happens
	// inside adapter.Start (each adapter is responsible for de-duping
	// platform retries) — the orchestrator does NOT add another layer.
	// All that's left is to start the adapter with a buffered inbound
	// channel that pumps into the shared inboundCh.
	adapterInbound := make(chan bridge.Inbound, 32)
	if err := adapter.Start(s.ctx, adapterInbound); err != nil {
		if lock != nil {
			_ = lock.Release()
		}
		s.mu.Lock()
		delete(s.adapters, key)
		s.mu.Unlock()
		return fmt.Errorf("bridge: adapter Start %s: %w", key, err)
	}

	// Track the lock so Stop / DeregisterAdapter can release it.
	// nil lock (mediated-inbound adapters) is recorded as a sentinel
	// — the release path nil-checks before calling Release.
	if lock != nil {
		s.adapterMu.Lock()
		if s.adapterLocks == nil {
			s.adapterLocks = make(map[string]store.LockHandle)
		}
		s.adapterLocks[key] = lock
		s.adapterMu.Unlock()
	}

	// Pump adapter inbound into the shared orchestrator channel. One
	// goroutine per adapter — these are tiny (no parsing, no IO) but get
	// the recover-and-log treatment for symmetry with other supervised
	// goroutines.
	s.launchSupervised("adapter-pump/"+key, func(ctx context.Context) {
		s.runAdapterPump(ctx, key, adapterInbound)
	})

	logging.Info("bridge: adapter registered",
		"channel", adapter.Channel(), "identity", adapter.Identity())
	return nil
}

// runAdapterPump forwards inbound from one adapter onto the orchestrator's
// shared inboundCh. The orchestrator's runInboundLoop reads from there
// and fans into per-session dispatchers. The pump exists primarily to
// provide a single back-pressure boundary between adapter goroutines
// (one per identity) and the shared dispatch path.
//
// Per the chat-bridge spec: inbound messages MUST NOT be dropped — when
// the downstream dispatcher is slow, back-pressure propagates through
// this pump to the adapter (and from there to the chat platform). The
// inboundCh send is therefore unconditional except for context
// cancellation. A periodic warning is logged when the send blocks for a
// long time to make the bottleneck observable.
func (s *Service) runAdapterPump(ctx context.Context, key string, src <-chan bridge.Inbound) {
	for {
		select {
		case <-ctx.Done():
			return
		case in, ok := <-src:
			if !ok {
				return
			}
			s.sendInboundWithBackpressure(ctx, key, in)
		}
	}
}

// sendInboundWithBackpressure pushes one inbound to the shared channel,
// blocking indefinitely (only honoring ctx cancellation). Emits a warning
// every adapterPumpStallWarnInterval that the send is stalled so a wedged
// dispatcher is visible in logs without dropping messages.
func (s *Service) sendInboundWithBackpressure(ctx context.Context, key string, in bridge.Inbound) {
	timer := time.NewTimer(adapterPumpStallWarnInterval)
	defer timer.Stop()
	for {
		select {
		case s.inboundCh <- in:
			return
		case <-ctx.Done():
			return
		case <-timer.C:
			logging.Warn("bridge: adapter pump stalled (dispatcher slow); back-pressuring adapter",
				"adapter", key, "peer", in.Peer)
			timer.Reset(adapterPumpStallWarnInterval)
		}
	}
}

// adapterPumpStallWarnInterval is the cadence at which we log a stall
// warning while the adapter pump is blocked on the shared inboundCh.
// Tuned to be loud enough to notice in tail-following logs but not so
// chatty that a normally-slow downstream floods the log.
const adapterPumpStallWarnInterval = 5 * time.Second

// DeregisterAdapter removes an adapter and releases its identity lock.
// Used by /router/identities/{kind}/{id} DELETE (Phase 6.3) and by Stop.
func (s *Service) DeregisterAdapter(channel, identityID string) {
	key := adapterKey(channel, identityID)

	s.mu.Lock()
	delete(s.adapters, key)
	s.mu.Unlock()

	s.adapterMu.Lock()
	lock := s.adapterLocks[key]
	delete(s.adapterLocks, key)
	s.adapterMu.Unlock()

	if lock != nil {
		_ = lock.Release()
	}
}

// adapterMu protects adapterLocks. Declared at package scope rather than
// on Service so the zero-value works in tests that construct minimal
// Service instances.
//
// Note: this is per-Service-instance — using a field-level sync.Mutex
// avoids any cross-test contention. See service.go for the field.
var _ = sync.Mutex{}

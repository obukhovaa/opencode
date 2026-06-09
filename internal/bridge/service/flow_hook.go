package service

import (
	"context"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// InteractiveHook satisfies flow.InteractiveHook so the flow engine can
// auto-bind / auto-unbind a session to chat-platform peers at the
// boundaries of an interactive: true step.
//
// Implementation note: this type lives in internal/bridge/service rather
// than internal/flow because it needs Service.Bind / Service.Unbind. The
// flow package declares the interface and only depends on
// internal/bridge for the PeerRef type — no cycle.
type interactiveBridge struct {
	svc *Service
}

// InteractiveHook returns a flow.InteractiveHook-compatible value
// bound to this Service. cmd/serve.go installs it via
// flow.SetInteractiveHook after the bridge has started.
func (s *Service) InteractiveHook() *interactiveBridge {
	return &interactiveBridge{svc: s}
}

// OnInteractiveStepStart binds the step's session to the resolved
// target peers. Per the flow-api spec, this MUST complete BEFORE the
// agent's first turn fires so the agent's output naturally fans out to
// every reviewer.
func (h *interactiveBridge) OnInteractiveStepStart(ctx context.Context, sessionID string, target []bridge.PeerRef) error {
	if h == nil || h.svc == nil {
		return nil
	}
	results, err := h.svc.Bind(ctx, sessionID, target)
	if err != nil {
		return err
	}
	for _, r := range results {
		if r.Err != nil {
			return r.Err
		}
	}
	return nil
}

// OnInteractiveStepComplete unbinds the session after the step's agent
// completes (struct_output fires). The dispatcher and all bindings for
// this session are torn down.
func (h *interactiveBridge) OnInteractiveStepComplete(ctx context.Context, sessionID string) error {
	if h == nil || h.svc == nil {
		return nil
	}
	return h.svc.Unbind(ctx, sessionID)
}

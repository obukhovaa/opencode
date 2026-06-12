package flow

import (
	"context"
	"errors"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// ErrInteractiveBridgeDisabled is returned by the flow engine when an
// interactive: true step starts but no InteractiveHook is registered
// (i.e. cfg.Router == nil at boot time). Per the flow-api spec:
// "If the bridge is not enabled (cfg.Router == nil), the step MUST fail
// fast with a clear error indicating the bridge is required."
var ErrInteractiveBridgeDisabled = errors.New("flow: interactive step requires router configuration; bridge is not enabled")

// InteractiveHook is the contract the chat-bridge satisfies so the flow
// engine can auto-bind / auto-unbind a session to chat-platform peers at
// the boundaries of an interactive step.
//
// Lifecycle per interactive step:
//
//  1. flow.Service.runStep resolves interaction.target against the
//     step's args (single PeerRef or []PeerRef).
//  2. OnInteractiveStepStart is called BEFORE agent.Run. The hook
//     persists the bindings and (when the orchestrator-init flow needs
//     it) emits flow.waiting_for_input via the SSE broker.
//  3. The agent runs; its output fans out to bound peers; reviewer
//     replies route back to the agent through the bridge inbound path.
//  4. On agent struct_output, OnInteractiveStepComplete is called to
//     unbind the session.
//
// The hook is invoked synchronously — failures bubble up and fail-fast
// the step. Implementations MUST be idempotent (a step retry calls
// Start again).
type InteractiveHook interface {
	OnInteractiveStepStart(ctx context.Context, sessionID string, target []bridge.PeerRef) error
	OnInteractiveStepComplete(ctx context.Context, sessionID string) error
}

// nopInteractiveHook is the default when no bridge is registered. Every
// call returns ErrInteractiveBridgeDisabled so the step fails fast per
// the spec.
type nopInteractiveHook struct{}

func (nopInteractiveHook) OnInteractiveStepStart(context.Context, string, []bridge.PeerRef) error {
	return ErrInteractiveBridgeDisabled
}
func (nopInteractiveHook) OnInteractiveStepComplete(context.Context, string) error { return nil }

// resolveInteractionTarget resolves a StepInteraction's target expression
// against the flow's args. The expression must be of the form
// "${args.NAME}" where args[NAME] is either a PeerRef-shaped map or an
// array of such maps. Returns the resolved peers as a []bridge.PeerRef.
//
// This is the only template-style expression the flow YAML schema
// supports for now — keeping the parser deliberately simple (no
// nested-path access, no expressions, no defaults). Operators wanting
// dynamic peer selection drive it via flow-args (set by the
// orchestrator at flow start) rather than complex YAML logic.
func resolveInteractionTarget(interaction *StepInteraction, args map[string]any) ([]bridge.PeerRef, error) {
	if interaction == nil {
		return nil, errors.New("interactive step has no interaction block")
	}
	target := interaction.Target
	if target == "" {
		return nil, errors.New("interaction.target is required for interactive steps")
	}
	// Accept "${args.NAME}" form. Anything else is an error today.
	const prefix = "${args."
	const suffix = "}"
	if !startsWith(target, prefix) || !endsWith(target, suffix) {
		return nil, errInteractionTargetSyntax
	}
	name := target[len(prefix) : len(target)-len(suffix)]
	if name == "" {
		return nil, errInteractionTargetSyntax
	}
	raw, ok := args[name]
	if !ok {
		return nil, &interactionMissingArgError{Name: name}
	}
	// Accept either a single PeerRef-shaped map or an array of them.
	var peers []bridge.PeerRef
	switch v := raw.(type) {
	case map[string]any:
		peer, err := peerRefFromMap(v)
		if err != nil {
			return nil, err
		}
		peers = []bridge.PeerRef{peer}
	case []any:
		peers = make([]bridge.PeerRef, 0, len(v))
		for i, entry := range v {
			m, ok := entry.(map[string]any)
			if !ok {
				return nil, &interactionElementError{Index: i, Kind: "must be an object"}
			}
			peer, err := peerRefFromMap(m)
			if err != nil {
				return nil, &interactionElementError{Index: i, Kind: err.Error()}
			}
			peers = append(peers, peer)
		}
		if len(peers) == 0 {
			return nil, errors.New("interaction.target resolved to empty array")
		}
	default:
		return nil, errInteractionTargetType
	}
	// Identity override: when the step's interaction block declares an
	// identity, force it onto every resolved peer — flow YAML wins over
	// args.reviewer.identity. Empty Identity in the StepInteraction
	// means "use whatever the args carry" (backwards-compat default).
	if interaction.Identity != "" {
		for i := range peers {
			peers[i].Identity = interaction.Identity
		}
	}
	return peers, nil
}

var (
	errInteractionTargetSyntax = errors.New("interaction.target must be a ${args.NAME} expression")
	errInteractionTargetType   = errors.New("interaction.target args entry must be an object or array of objects")
)

type interactionMissingArgError struct{ Name string }

func (e *interactionMissingArgError) Error() string {
	return "interaction.target references missing flow arg: " + e.Name
}

type interactionElementError struct {
	Index int
	Kind  string
}

func (e *interactionElementError) Error() string {
	return "interaction.target[" + itoa(e.Index) + "]: " + e.Kind
}

// peerRefFromMap converts a generic map to bridge.PeerRef. The map
// shape mirrors the JSON `PeerRef`: channel, identity, peerId, optional
// mention.
func peerRefFromMap(m map[string]any) (bridge.PeerRef, error) {
	get := func(k string) (string, error) {
		v, ok := m[k]
		if !ok {
			return "", nil
		}
		s, ok := v.(string)
		if !ok {
			return "", errors.New(k + " must be a string")
		}
		return s, nil
	}
	channel, err := get("channel")
	if err != nil {
		return bridge.PeerRef{}, err
	}
	identity, err := get("identity")
	if err != nil {
		return bridge.PeerRef{}, err
	}
	peerID, err := get("peerId")
	if err != nil {
		return bridge.PeerRef{}, err
	}
	mention, err := get("mention")
	if err != nil {
		return bridge.PeerRef{}, err
	}
	if channel == "" || identity == "" || peerID == "" {
		return bridge.PeerRef{}, errors.New("PeerRef requires channel, identity, peerId")
	}
	return bridge.PeerRef{Channel: channel, Identity: identity, PeerID: peerID, Mention: mention}, nil
}

// tiny utility helpers kept inline so this file has no extra imports
// beyond context+errors+bridge.
func startsWith(s, prefix string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix }
func endsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

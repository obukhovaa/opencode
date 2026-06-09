package flow

import (
	"context"
	"errors"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

func TestResolveInteractionTargetSinglePeer(t *testing.T) {
	t.Parallel()
	args := map[string]any{
		"reviewer": map[string]any{
			"channel":  "slack",
			"identity": "default",
			"peerId":   "D012345",
			"mention":  "<@U01ABC>",
		},
	}
	peers, err := resolveInteractionTarget(&StepInteraction{Target: "${args.reviewer}"}, args)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("peers len = %d", len(peers))
	}
	want := bridge.PeerRef{
		Channel: "slack", Identity: "default", PeerID: "D012345", Mention: "<@U01ABC>",
	}
	if peers[0] != want {
		t.Errorf("got %+v, want %+v", peers[0], want)
	}
}

func TestResolveInteractionTargetArrayPeers(t *testing.T) {
	t.Parallel()
	args := map[string]any{
		"reviewers": []any{
			map[string]any{"channel": "slack", "identity": "default", "peerId": "D1"},
			map[string]any{"channel": "telegram", "identity": "default", "peerId": "12345"},
		},
	}
	peers, err := resolveInteractionTarget(&StepInteraction{Target: "${args.reviewers}"}, args)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("peers len = %d", len(peers))
	}
	if peers[0].PeerID != "D1" || peers[1].PeerID != "12345" {
		t.Errorf("peers = %+v", peers)
	}
}

func TestResolveInteractionTargetMissingArg(t *testing.T) {
	t.Parallel()
	_, err := resolveInteractionTarget(&StepInteraction{Target: "${args.ghost}"}, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing arg")
	}
	if _, ok := err.(*interactionMissingArgError); !ok {
		t.Errorf("err = %v, want interactionMissingArgError", err)
	}
}

func TestResolveInteractionTargetBadSyntax(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"reviewer", "${reviewer}", "${args.}", ""} {
		_, err := resolveInteractionTarget(&StepInteraction{Target: in}, map[string]any{})
		if err == nil {
			t.Errorf("%q: expected error", in)
		}
	}
}

func TestNopHookFailsFast(t *testing.T) {
	t.Parallel()
	h := nopInteractiveHook{}
	err := h.OnInteractiveStepStart(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D1"},
	})
	if !errors.Is(err, ErrInteractiveBridgeDisabled) {
		t.Errorf("err = %v, want ErrInteractiveBridgeDisabled", err)
	}
}

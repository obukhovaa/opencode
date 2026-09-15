package service

import (
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// TestCmdVerbosity_ListsTwoModesAndMarksActive: the no-argument form
// describes both levels, names the card for compact, and marks the live
// one active.
func TestCmdVerbosity_ListsTwoModesAndMarksActive(t *testing.T) {
	t.Parallel()
	s := &Service{}
	reply := s.cmdVerbosity(nil, bridge.Inbound{})
	if reply == nil || reply.Hint == nil {
		t.Fatal("expected a reply with a list hint")
	}
	if len(reply.Hint.Items) != 2 {
		t.Fatalf("items = %d; want 2 (compact, full)", len(reply.Hint.Items))
	}
	if reply.Hint.Items[0].Label != bridge.ToolUpdateVerbosityCompact || reply.Hint.Items[0].Marker != "active" {
		t.Errorf("first item = %+v; want compact marked active", reply.Hint.Items[0])
	}
	if !strings.Contains(reply.Hint.Items[0].Sublabel, "progress card") {
		t.Errorf("compact description = %q; want it to name the progress card", reply.Hint.Items[0].Sublabel)
	}
	if !strings.Contains(reply.Text, "compact") || !strings.Contains(reply.Text, "full") {
		t.Errorf("text fallback = %q; want both modes listed", reply.Text)
	}
}

// TestCmdVerbosity_AcceptsAliasesRejectsUnknown: verbose and debug switch
// to full; an unknown word is rejected and leaves the live value alone.
func TestCmdVerbosity_AcceptsAliasesRejectsUnknown(t *testing.T) {
	t.Parallel()
	s := &Service{}
	for _, alias := range []string{"verbose", "debug"} {
		reply := s.cmdVerbosity(nil, bridge.Inbound{CommandArgs: alias})
		if reply == nil || !strings.Contains(reply.Text, "verbosity: full") {
			t.Errorf("/verbosity %s reply = %+v; want it to report full", alias, reply)
		}
		if got := s.ToolVerbosity(); got != bridge.ToolUpdateVerbosityFull {
			t.Errorf("after /verbosity %s live = %q; want full", alias, got)
		}
		if _, err := s.SetToolVerbosity("compact"); err != nil {
			t.Fatalf("reset: %v", err)
		}
	}
	reply := s.cmdVerbosity(nil, bridge.Inbound{CommandArgs: "chatty"})
	if reply == nil || !strings.Contains(reply.Text, "Failed to set verbosity") {
		t.Errorf("/verbosity chatty reply = %+v; want a rejection", reply)
	}
	if got := s.ToolVerbosity(); got != bridge.ToolUpdateVerbosityCompact {
		t.Errorf("live after rejected switch = %q; want compact (unchanged)", got)
	}
}

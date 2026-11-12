package agent

import (
	"errors"
	"testing"
)

func TestWithIdleSessionDoesNotBlockUnrelatedConversation(t *testing.T) {
	if !acquireSessionSlot("other", t) {
		t.Fatal("other slot busy")
	}
	defer releaseSessionSlot("other")
	ran := false
	if err := WithIdleSession("target", func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("ran=%t err=%v", ran, err)
	}
	if !acquireSessionSlot("target", t) {
		t.Fatal("target slot busy")
	}
	defer releaseSessionSlot("target")
	ran = false
	err := WithIdleSession("target", func() error { ran = true; return nil })
	if !errors.Is(err, ErrSessionBusy) || ran {
		t.Fatalf("ran=%t err=%v", ran, err)
	}
}

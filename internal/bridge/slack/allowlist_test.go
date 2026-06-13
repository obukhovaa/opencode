package slack

import (
	"context"
	"errors"
	"testing"

	"github.com/slack-go/slack/slackevents"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// stubAllowlist returns a checker that accepts identifiers in the given
// set and rejects everything else. Empty set means "deny all".
func stubAllowlist(accept ...string) bridge.AllowlistChecker {
	allowed := make(map[string]bool, len(accept))
	for _, a := range accept {
		allowed[a] = true
	}
	return func(_ context.Context, identifier string) (bool, error) {
		return allowed[identifier], nil
	}
}

func TestPrivateModeAcceptsAllowlistedAuthorViaDM(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	a, err := New(
		Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp", Access: AccessPrivate},
		Options{APIURL: mock.URL() + "/", Allowlisted: stubAllowlist("U5Z84SKMZ")},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetBotUserID("UBOT")
	t.Cleanup(func() { _ = a.Stop() })

	inbound := make(chan bridge.Inbound, 4)
	a.SetInbound(inbound)

	a.HandleMessageEvent(context.Background(), &slackevents.MessageEvent{
		Channel: "D012345",
		User:    "U5Z84SKMZ",
		Text:    "hello",
	})

	select {
	case in := <-inbound:
		if in.AuthorID != "U5Z84SKMZ" {
			t.Errorf("AuthorID = %q", in.AuthorID)
		}
	default:
		t.Fatal("expected inbound for allowlisted user, got none")
	}
}

func TestPrivateModeDropsNonAllowlistedDM(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	a, err := New(
		Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp", Access: AccessPrivate},
		Options{APIURL: mock.URL() + "/", Allowlisted: stubAllowlist("U5Z84SKMZ")},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetBotUserID("UBOT")
	t.Cleanup(func() { _ = a.Stop() })

	inbound := make(chan bridge.Inbound, 4)
	a.SetInbound(inbound)

	a.HandleMessageEvent(context.Background(), &slackevents.MessageEvent{
		Channel: "D012345",
		User:    "U_OTHER",
		Text:    "spam",
	})

	select {
	case in := <-inbound:
		t.Fatalf("expected no inbound for non-allowlisted user, got %+v", in)
	default:
	}
}

func TestPrivateModeAcceptsAppMentionFromAllowlistedChannel(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	a, err := New(
		Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp", Access: AccessPrivate},
		Options{APIURL: mock.URL() + "/", Allowlisted: stubAllowlist("C0AJ6MZLNJE")},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetBotUserID("UBOT")
	t.Cleanup(func() { _ = a.Stop() })

	inbound := make(chan bridge.Inbound, 4)
	a.SetInbound(inbound)

	a.HandleAppMention(context.Background(), &slackevents.AppMentionEvent{
		Channel:   "C0AJ6MZLNJE",
		User:      "U_OTHER",
		Text:      "<@UBOT> ping",
		TimeStamp: "1700000123.000200",
	})

	select {
	case <-inbound:
	default:
		t.Fatal("expected inbound — allowlisted channel id should authorise mention")
	}
}

func TestPublicModeBypassesAllowlist(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	a, err := New(
		Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp"}, // Access empty = public
		Options{APIURL: mock.URL() + "/", Allowlisted: stubAllowlist( /* empty set */ )},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetBotUserID("UBOT")
	t.Cleanup(func() { _ = a.Stop() })

	inbound := make(chan bridge.Inbound, 4)
	a.SetInbound(inbound)

	a.HandleMessageEvent(context.Background(), &slackevents.MessageEvent{
		Channel: "D012345",
		User:    "U_ANY",
		Text:    "hi",
	})

	select {
	case <-inbound:
	default:
		t.Fatal("public-mode adapter must accept inbound regardless of allowlist")
	}
}

func TestPrivateModeWithoutCheckerFailsOpen(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	a, err := New(
		Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp", Access: AccessPrivate},
		Options{APIURL: mock.URL() + "/"}, // no Allowlisted
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetBotUserID("UBOT")
	t.Cleanup(func() { _ = a.Stop() })

	inbound := make(chan bridge.Inbound, 4)
	a.SetInbound(inbound)

	a.HandleMessageEvent(context.Background(), &slackevents.MessageEvent{
		Channel: "D012345",
		User:    "U_ANY",
		Text:    "hi",
	})

	select {
	case <-inbound:
	default:
		t.Fatal("nil checker in private mode must fail-open (accept inbound), got drop")
	}
}

func TestAllowlistCheckerErrorFailsClosed(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	failing := func(_ context.Context, _ string) (bool, error) {
		return false, errors.New("db down")
	}
	a, err := New(
		Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp", Access: AccessPrivate},
		Options{APIURL: mock.URL() + "/", Allowlisted: failing},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetBotUserID("UBOT")
	t.Cleanup(func() { _ = a.Stop() })

	inbound := make(chan bridge.Inbound, 4)
	a.SetInbound(inbound)

	a.HandleMessageEvent(context.Background(), &slackevents.MessageEvent{
		Channel: "D012345",
		User:    "U_ANY",
		Text:    "hi",
	})

	select {
	case in := <-inbound:
		t.Fatalf("checker error should fail-closed, got inbound %+v", in)
	default:
	}
}

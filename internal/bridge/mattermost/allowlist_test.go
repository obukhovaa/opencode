package mattermost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
)

func mmStubAllowlist(accept ...string) bridge.AllowlistChecker {
	allowed := make(map[string]bool, len(accept))
	for _, a := range accept {
		allowed[a] = true
	}
	return func(_ context.Context, identifier string) (bool, error) {
		return allowed[identifier], nil
	}
}

func startAdapterWithOpts(t *testing.T, id Identity, mock *mockServer, opts Options, inboundCap int) (*Adapter, <-chan bridge.Inbound) {
	t.Helper()
	if id.ServerURL == "" {
		id.ServerURL = mock.URL()
	}
	if opts.MediaDir == "" {
		opts.MediaDir = t.TempDir()
	}
	a, err := New(id, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch := make(chan bridge.Inbound, inboundCap)
	ctx, cancel := context.WithCancel(context.Background())
	if err := a.Start(ctx, ch); err != nil {
		cancel()
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = a.Stop()
		cancel()
	})
	return a, ch
}

func TestMattermostPrivateAcceptsAllowlistedAuthor(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})

	_, inbound := startAdapterWithOpts(t,
		Identity{ID: "default", AccessToken: "tok", Access: AccessPrivate},
		mock,
		Options{Allowlisted: mmStubAllowlist("uAuthor")},
		4)

	mock.pushPostedEvent("D", Post{
		ID: "p1", ChannelID: "c1", UserID: "uAuthor", Message: "hi",
	})

	in := receiveOne(t, inbound, 2*time.Second)
	if in.AuthorID != "uAuthor" {
		t.Errorf("AuthorID = %q", in.AuthorID)
	}
}

func TestMattermostPrivateDropsNonAllowlistedAuthor(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})

	_, inbound := startAdapterWithOpts(t,
		Identity{ID: "default", AccessToken: "tok", Access: AccessPrivate},
		mock,
		Options{Allowlisted: mmStubAllowlist("uAuthor")},
		4)

	mock.pushPostedEvent("D", Post{
		ID: "p2", ChannelID: "c2", UserID: "uOther", Message: "spam",
	})

	select {
	case in := <-inbound:
		t.Fatalf("expected drop, got %+v", in)
	case <-time.After(250 * time.Millisecond):
	}
}

func TestMattermostPublicBypassesAllowlist(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})

	_, inbound := startAdapterWithOpts(t,
		Identity{ID: "default", AccessToken: "tok"}, // public default
		mock,
		Options{Allowlisted: mmStubAllowlist( /* empty */ )},
		4)

	mock.pushPostedEvent("D", Post{
		ID: "p3", ChannelID: "c3", UserID: "uAnyone", Message: "hello",
	})

	in := receiveOne(t, inbound, 2*time.Second)
	if in.Text != "hello" {
		t.Errorf("Text = %q", in.Text)
	}
}

func TestMattermostPrivateMissingCheckerFailsOpen(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})

	_, inbound := startAdapterWithOpts(t,
		Identity{ID: "default", AccessToken: "tok", Access: AccessPrivate},
		mock,
		Options{}, // no Allowlisted
		4)

	mock.pushPostedEvent("D", Post{
		ID: "p4", ChannelID: "c4", UserID: "uAnyone", Message: "hello",
	})

	in := receiveOne(t, inbound, 2*time.Second)
	if in.Text != "hello" {
		t.Errorf("Text = %q; want fail-open accept", in.Text)
	}
}

func TestMattermostAllowlistErrorFailsClosed(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, User{ID: "bot123", Username: "testbot"})

	failing := func(_ context.Context, _ string) (bool, error) {
		return false, errors.New("db down")
	}
	_, inbound := startAdapterWithOpts(t,
		Identity{ID: "default", AccessToken: "tok", Access: AccessPrivate},
		mock,
		Options{Allowlisted: failing},
		4)

	mock.pushPostedEvent("D", Post{
		ID: "p5", ChannelID: "c5", UserID: "uAnyone", Message: "hello",
	})

	select {
	case in := <-inbound:
		t.Fatalf("checker error should fail-closed, got %+v", in)
	case <-time.After(250 * time.Millisecond):
	}
}

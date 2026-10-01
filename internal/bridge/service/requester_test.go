package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

// emailResolvingAdapter is a stubAdapter that also implements
// bridge.UserEmailResolver, counting lookups so tests can assert caching.
type emailResolvingAdapter struct {
	*stubAdapter
	emails map[string]string
	err    error
	calls  int
}

func (a *emailResolvingAdapter) ResolveUserEmail(_ context.Context, userID string) (string, error) {
	a.calls++
	if a.err != nil {
		return "", a.err
	}
	return a.emails[userID], nil
}

func newRequesterTestService(adapter bridge.Adapter) *Service {
	s := &Service{adapters: map[string]bridge.Adapter{}, requesters: newRequesterCache()}
	if adapter != nil {
		s.adapters[adapterKey(adapter.Channel(), adapter.Identity())] = adapter
	}
	return s
}

func inboundFrom(author string) bridge.Inbound {
	return bridge.Inbound{
		Peer:     bridge.PeerRef{Channel: "slack", Identity: "bot", PeerID: "C1|1700000000.000100"},
		Text:     "hello",
		AuthorID: author,
	}
}

func TestRequesterFor(t *testing.T) {
	ctx := context.Background()

	t.Run("no author yields empty", func(t *testing.T) {
		s := newRequesterTestService(nil)
		if got := s.requesterFor(ctx, inboundFrom("")); got != "" {
			t.Fatalf("requester = %q, want empty", got)
		}
	})

	t.Run("adapter without resolver yields raw author id", func(t *testing.T) {
		s := newRequesterTestService(newStubAdapter("slack", "bot"))
		if got := s.requesterFor(ctx, inboundFrom("U1")); got != "U1" {
			t.Fatalf("requester = %q, want U1", got)
		}
	})

	t.Run("resolved email is used and cached", func(t *testing.T) {
		a := &emailResolvingAdapter{stubAdapter: newStubAdapter("slack", "bot"), emails: map[string]string{"U1": "one@example.com"}}
		s := newRequesterTestService(a)
		for i := 0; i < 3; i++ {
			if got := s.requesterFor(ctx, inboundFrom("U1")); got != "one@example.com" {
				t.Fatalf("requester = %q, want one@example.com", got)
			}
		}
		if a.calls != 1 {
			t.Fatalf("lookups = %d, want 1 (cached after the first)", a.calls)
		}
	})

	t.Run("each author in a shared thread resolves separately", func(t *testing.T) {
		a := &emailResolvingAdapter{stubAdapter: newStubAdapter("slack", "bot"), emails: map[string]string{
			"U1": "one@example.com", "U2": "two@example.com",
		}}
		s := newRequesterTestService(a)
		if got := s.requesterFor(ctx, inboundFrom("U1")); got != "one@example.com" {
			t.Fatalf("U1 requester = %q", got)
		}
		if got := s.requesterFor(ctx, inboundFrom("U2")); got != "two@example.com" {
			t.Fatalf("U2 requester = %q", got)
		}
	})

	t.Run("profile without email falls back to author id and is cached", func(t *testing.T) {
		a := &emailResolvingAdapter{stubAdapter: newStubAdapter("slack", "bot"), emails: map[string]string{}}
		s := newRequesterTestService(a)
		for i := 0; i < 2; i++ {
			if got := s.requesterFor(ctx, inboundFrom("U9")); got != "U9" {
				t.Fatalf("requester = %q, want U9", got)
			}
		}
		if a.calls != 1 {
			t.Fatalf("lookups = %d, want 1", a.calls)
		}
	})

	t.Run("lookup error falls back to author id and is retried", func(t *testing.T) {
		a := &emailResolvingAdapter{stubAdapter: newStubAdapter("slack", "bot"), err: errors.New("rate limited")}
		s := newRequesterTestService(a)
		for i := 0; i < 2; i++ {
			if got := s.requesterFor(ctx, inboundFrom("U1")); got != "U1" {
				t.Fatalf("requester = %q, want U1", got)
			}
		}
		if a.calls != 2 {
			t.Fatalf("lookups = %d, want 2 (errors are not cached)", a.calls)
		}
	})

	t.Run("zero Service has no cache and still resolves", func(t *testing.T) {
		a := &emailResolvingAdapter{stubAdapter: newStubAdapter("slack", "bot"), emails: map[string]string{"U1": "one@example.com"}}
		s := &Service{adapters: map[string]bridge.Adapter{adapterKey("slack", "bot"): a}}
		if got := s.requesterFor(ctx, inboundFrom("U1")); got != "one@example.com" {
			t.Fatalf("requester = %q, want one@example.com", got)
		}
	})
}

func TestRequesterCacheExpires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := newRequesterCache()
	c.now = func() time.Time { return now }
	c.put("k", "one@example.com")
	if got, ok := c.get("k"); !ok || got != "one@example.com" {
		t.Fatalf("get = %q, %v; want hit", got, ok)
	}
	now = now.Add(requesterCacheTTL + time.Second)
	if _, ok := c.get("k"); ok {
		t.Fatal("entry still served after TTL")
	}
}

// requesterCaptureAgent records the requester carried by each Run ctx.
type requesterCaptureAgent struct {
	agentpkg.Service // nil — only Run is called
	got              []string
}

func (a *requesterCaptureAgent) Run(ctx context.Context, _, _ string, _ int, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	a.got = append(a.got, tools.RequesterFromContext(ctx))
	ch := make(chan agentpkg.AgentEvent, 1)
	ch <- agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeResponse}
	close(ch)
	return ch, nil
}

// Each turn in a shared session runs with its own author's requester, so a
// thread several people post in attributes every turn to the right person.
func TestHandleInbound_RunCtxCarriesAuthorRequester(t *testing.T) {
	ag := &requesterCaptureAgent{}
	svc, stub := newDispatchTestSvc(t, ag)
	svc.adapters[adapterKey("slack", "default")] = &emailResolvingAdapter{
		stubAdapter: stub,
		emails:      map[string]string{"U1": "one@example.com", "U2": "two@example.com"},
	}

	d := newBareDispatch(svc, "S1")
	for _, author := range []string{"U1", "U2", ""} {
		in := testInbound("hi")
		in.AuthorID = author
		d.handleInbound(context.Background(), in)
	}

	want := []string{"one@example.com", "two@example.com", ""}
	if len(ag.got) != len(want) {
		t.Fatalf("Run calls = %d, want %d", len(ag.got), len(want))
	}
	for i := range want {
		if ag.got[i] != want[i] {
			t.Errorf("turn %d requester = %q, want %q", i, ag.got[i], want[i])
		}
	}
}

package langfuse

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestPromptClient builds a client pointed at a test server, with
// credentials supplied inline so the LANGFUSE_* environment of the machine
// running the tests cannot leak in.
func newTestPromptClient(t *testing.T, baseURL string, opts PromptOptions) *PromptClient {
	t.Helper()
	c := NewPromptClient("pk-test", "sk-test", baseURL, opts)
	if !c.Enabled() {
		t.Fatal("test client is disabled — credentials did not resolve")
	}
	return c
}

func TestPromptClient_Resolve(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantText   string
		wantErrIs  error
		wantErrHas string
	}{
		{
			name:     "text prompt",
			status:   http.StatusOK,
			body:     `{"name":"flows/x/main","version":3,"type":"text","prompt":"do the thing"}`,
			wantText: "do the thing",
		},
		{
			name:     "chat prompt is flattened in source order",
			status:   http.StatusOK,
			body:     `{"name":"a","version":1,"type":"chat","prompt":[{"role":"system","content":"be terse"},{"role":"user","content":"go"}]}`,
			wantText: "system: be terse\n\nuser: go",
		},
		{
			name:     "chat block with no role keeps only its content",
			status:   http.StatusOK,
			body:     `{"name":"a","version":1,"type":"chat","prompt":[{"content":"bare"}]}`,
			wantText: "bare",
		},
		{
			name:     "type is a hint only — a text payload labelled chat still resolves",
			status:   http.StatusOK,
			body:     `{"name":"a","version":1,"type":"chat","prompt":"actually text"}`,
			wantText: "actually text",
		},
		{
			name:      "missing prompt",
			status:    http.StatusNotFound,
			body:      `{"error":"not found"}`,
			wantErrIs: ErrPromptNotFound,
		},
		{
			name:      "bad credentials",
			status:    http.StatusUnauthorized,
			body:      `{"error":"unauthorized"}`,
			wantErrIs: ErrPromptUnauthorized,
		},
		{
			name:      "forbidden is also an auth failure",
			status:    http.StatusForbidden,
			body:      `{"error":"forbidden"}`,
			wantErrIs: ErrPromptUnauthorized,
		},
		{
			name:      "whitespace-only prompt is a resolution error",
			status:    http.StatusOK,
			body:      `{"name":"a","version":1,"type":"text","prompt":"   \n  "}`,
			wantErrIs: ErrPromptEmpty,
		},
		{
			name:      "chat prompt of empty blocks is a resolution error",
			status:    http.StatusOK,
			body:      `{"name":"a","version":1,"type":"chat","prompt":[{"role":"system","content":"  "}]}`,
			wantErrIs: ErrPromptEmpty,
		},
		{
			name:       "server error surfaces its status",
			status:     http.StatusBadGateway,
			body:       `bad gateway`,
			wantErrHas: "HTTP 502",
		},
		{
			name:       "unsupported payload shape",
			status:     http.StatusOK,
			body:       `{"name":"a","version":1,"type":"weird","prompt":{"nested":true}}`,
			wantErrHas: "unsupported payload type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c := newTestPromptClient(t, srv.URL, PromptOptions{})
			got, err := c.Resolve(context.Background(), "flows/x/main", "")

			switch {
			case tt.wantErrIs != nil:
				if !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("error = %v, want %v", err, tt.wantErrIs)
				}
			case tt.wantErrHas != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErrHas) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErrHas)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got.Text != tt.wantText {
					t.Errorf("Text = %q, want %q", got.Text, tt.wantText)
				}
			}
		})
	}
}

// TestPromptClient_RequestShape pins what actually goes on the wire: the v2
// endpoint, the label as a query parameter, Basic auth, and — the easy one
// to get wrong — a slash-bearing prompt name escaped into ONE path segment
// rather than a sub-path.
func TestPromptClient_RequestShape(t *testing.T) {
	var gotPath, gotRawQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotRawQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"name":"a","version":1,"type":"text","prompt":"p"}`))
	}))
	defer srv.Close()

	c := newTestPromptClient(t, srv.URL, PromptOptions{})
	if _, err := c.Resolve(context.Background(), "flows/react-on-jira/prepare-plan", "staging"); err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}

	wantPath := "/api/public/v2/prompts/flows%2Freact-on-jira%2Fprepare-plan"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q (slashes must be escaped into one segment)", gotPath, wantPath)
	}
	if gotRawQuery != "label=staging" {
		t.Errorf("query = %q, want label=staging", gotRawQuery)
	}
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Errorf("Authorization = %q, want Basic auth", gotAuth)
	}
}

func TestPromptClient_DefaultLabel(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"name":"a","version":1,"type":"text","prompt":"p"}`))
	}))
	defer srv.Close()

	t.Run("falls back to production", func(t *testing.T) {
		c := newTestPromptClient(t, srv.URL, PromptOptions{})
		if _, err := c.Resolve(context.Background(), "a", ""); err != nil {
			t.Fatalf("Resolve() error: %v", err)
		}
		if gotQuery != "label=production" {
			t.Errorf("query = %q, want label=production", gotQuery)
		}
	})

	t.Run("configured default applies to unlabelled references", func(t *testing.T) {
		c := newTestPromptClient(t, srv.URL, PromptOptions{DefaultLabel: "canary"})
		if _, err := c.Resolve(context.Background(), "a", ""); err != nil {
			t.Fatalf("Resolve() error: %v", err)
		}
		if gotQuery != "label=canary" {
			t.Errorf("query = %q, want label=canary", gotQuery)
		}
	})
}

func TestPromptClient_CachesWithinTTL(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"name":"a","version":1,"type":"text","prompt":"p"}`))
	}))
	defer srv.Close()

	c := newTestPromptClient(t, srv.URL, PromptOptions{CacheTTL: time.Hour})
	for range 3 {
		if _, err := c.Resolve(context.Background(), "a", "production"); err != nil {
			t.Fatalf("Resolve() error: %v", err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("upstream calls = %d, want 1 — a fresh entry must not re-fetch", got)
	}

	// A different label is a different cache key, not a hit on the first.
	if _, err := c.Resolve(context.Background(), "a", "staging"); err != nil {
		t.Fatalf("Resolve() error: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("upstream calls = %d, want 2 — label is part of the cache key", got)
	}
}

// TestPromptClient_ServesStaleOnError is the resilience contract: once a
// prompt has been fetched, a later Langfuse outage must not fail the caller.
func TestPromptClient_ServesStaleOnError(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"name":"a","version":7,"type":"text","prompt":"cached text"}`))
	}))
	defer srv.Close()

	// A zero TTL makes every subsequent Resolve a re-fetch attempt.
	c := newTestPromptClient(t, srv.URL, PromptOptions{CacheTTL: time.Nanosecond})
	first, err := c.Resolve(context.Background(), "a", "production")
	if err != nil {
		t.Fatalf("priming Resolve() error: %v", err)
	}
	if first.Version != 7 {
		t.Fatalf("Version = %d, want 7", first.Version)
	}

	fail.Store(true)
	got, err := c.Resolve(context.Background(), "a", "production")
	if err != nil {
		t.Fatalf("Resolve() after outage error = %v, want the cached copy", err)
	}
	if got.Text != "cached text" {
		t.Errorf("Text = %q, want the cached text", got.Text)
	}
}

// TestPromptClient_ColdMissPropagates is the other half of the contract: with
// nothing cached there is nothing to fall back to, and the caller must learn
// that rather than run an agent on an empty prompt.
func TestPromptClient_ColdMissPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestPromptClient(t, srv.URL, PromptOptions{})
	if _, err := c.Resolve(context.Background(), "a", "production"); err == nil {
		t.Fatal("Resolve() error = nil, want an error on a cold miss")
	}
}

// TestPromptClient_SingleFlight pins that a cold cache under concurrent
// resolution of the same reference issues one request, not one per caller —
// the case a flow fan-out hits on every process start.
func TestPromptClient_SingleFlight(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		<-release
		_, _ = w.Write([]byte(`{"name":"a","version":1,"type":"text","prompt":"p"}`))
	}))
	defer srv.Close()

	c := newTestPromptClient(t, srv.URL, PromptOptions{CacheTTL: time.Hour})

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = c.Resolve(context.Background(), "a", "production")
		}(i)
	}
	// Let the first request reach the handler, then unblock everyone.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("upstream calls = %d, want 1 — concurrent misses must collapse", got)
	}
}

func TestPromptClient_DisabledWithoutCredentials(t *testing.T) {
	t.Setenv("LANGFUSE_PUBLIC_KEY", "")
	t.Setenv("LANGFUSE_SECRET_KEY", "")

	c := NewPromptClient("", "", "https://example.invalid", PromptOptions{})
	if c.Enabled() {
		t.Fatal("client with no credentials must be disabled")
	}
	if _, err := c.Resolve(context.Background(), "a", ""); !errors.Is(err, ErrPromptsDisabled) {
		t.Errorf("error = %v, want ErrPromptsDisabled", err)
	}

	// The nil client is the pre-Init global, and must behave the same way
	// rather than panicking on a receiver nobody initialised.
	var nilClient *PromptClient
	if nilClient.Enabled() {
		t.Error("nil client must report disabled")
	}
	if _, err := nilClient.Resolve(context.Background(), "a", ""); !errors.Is(err, ErrPromptsDisabled) {
		t.Errorf("nil client error = %v, want ErrPromptsDisabled", err)
	}
	if got := nilClient.DefaultLabel(); got != DefaultPromptLabel {
		t.Errorf("nil client DefaultLabel() = %q, want %q", got, DefaultPromptLabel)
	}
}

// TestPromptClient_Warm covers the startup pre-fetch: it dedupes, it fills
// the cache, and — the part that matters at boot — a failing reference is
// swallowed rather than propagated.
func TestPromptClient_Warm(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if strings.Contains(r.URL.EscapedPath(), "missing") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"name":"a","version":1,"type":"text","prompt":%q}`, "warm")))
	}))
	defer srv.Close()

	c := newTestPromptClient(t, srv.URL, PromptOptions{CacheTTL: time.Hour})
	c.Warm(context.Background(), []PromptRef{
		{Path: "a"},
		{Path: "a", Label: "production"}, // same key once the default fills in
		{Path: "a"},                      // exact duplicate
		{Path: "missing"},
		{Path: ""}, // skipped entirely
	})

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream calls = %d, want 2 (one per distinct reference)", got)
	}

	// The warmed entry is now a cache hit.
	if _, err := c.Resolve(context.Background(), "a", "production"); err != nil {
		t.Fatalf("Resolve() after warm error: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("upstream calls = %d, want 2 — warm-up must have populated the cache", got)
	}
}

// TestPromptClient_EscapesNameAsPathSegment pins the escaper choice.
//
// A prompt name is one path SEGMENT: a slash inside it is part of the name
// (Langfuse renders it as a folder) and must arrive as %2F, while a space
// must arrive as %20. url.QueryEscape gets the slash right but encodes a
// space as "+", which is a literal plus in a path — so a prompt named
// "team review" would be requested as the name "team+review" and 404.
func TestPromptClient_EscapesNameAsPathSegment(t *testing.T) {
	tests := []struct {
		name        string
		promptName  string
		wantRawPath string
		wantDecoded string
	}{
		{
			name:        "a space becomes %20, never +",
			promptName:  "team review",
			wantRawPath: "/api/public/v2/prompts/team%20review",
			wantDecoded: "team review",
		},
		{
			name:        "a slash stays inside the segment as %2F",
			promptName:  "flows/triage/main",
			wantRawPath: "/api/public/v2/prompts/flows%2Ftriage%2Fmain",
			wantDecoded: "flows/triage/main",
		},
		{
			name:        "a literal plus is not mistaken for a space",
			promptName:  "a+b",
			wantRawPath: "/api/public/v2/prompts/a+b",
			wantDecoded: "a+b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotRaw, gotDecoded string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotRaw = r.URL.EscapedPath()
				gotDecoded = strings.TrimPrefix(r.URL.Path, "/api/public/v2/prompts/")
				_, _ = w.Write([]byte(`{"name":"x","version":1,"type":"text","prompt":"ok"}`))
			}))
			defer srv.Close()

			c := newTestPromptClient(t, srv.URL, PromptOptions{})
			if _, err := c.Resolve(context.Background(), tt.promptName, "production"); err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if gotRaw != tt.wantRawPath {
				t.Errorf("escaped path = %q, want %q", gotRaw, tt.wantRawPath)
			}
			// What the server actually resolves the name to. This is the
			// assertion that fails under QueryEscape.
			if gotDecoded != tt.wantDecoded {
				t.Errorf("server-decoded name = %q, want %q", gotDecoded, tt.wantDecoded)
			}
		})
	}
}

// TestPromptClient_RetryFloorBoundsOutageCost pins that a sustained outage
// costs one request per entry per floor, not one per caller.
//
// Without the floor a failed re-fetch cannot advance fetchedAt, so the entry
// stays stale for the whole outage and every caller re-attempts — and because
// the fetch runs under the entry lock those attempts serialise, so N callers
// pay N × the HTTP timeout for a prompt whose cached copy was already there.
func TestPromptClient_RetryFloorBoundsOutageCost(t *testing.T) {
	t.Run("a warm entry is replayed without re-fetching", func(t *testing.T) {
		var calls int32
		var fail atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			if fail.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`{"name":"a","version":3,"type":"text","prompt":"cached text"}`))
		}))
		defer srv.Close()

		// A nanosecond TTL makes every call after the first a re-fetch
		// candidate, which is what isolates the floor.
		c := newTestPromptClient(t, srv.URL, PromptOptions{CacheTTL: time.Nanosecond})
		if _, err := c.Resolve(context.Background(), "a", "production"); err != nil {
			t.Fatalf("priming Resolve() error: %v", err)
		}
		fail.Store(true)

		for i := range 20 {
			got, err := c.Resolve(context.Background(), "a", "production")
			if err != nil {
				t.Fatalf("Resolve() #%d error = %v, want the cached copy", i, err)
			}
			if got.Text != "cached text" {
				t.Fatalf("Resolve() #%d Text = %q, want the cached text", i, got.Text)
			}
		}
		// 1 priming + exactly 1 failed attempt for the whole burst.
		if got := atomic.LoadInt32(&calls); got != 2 {
			t.Errorf("upstream calls = %d, want 2 — the floor must suppress a re-fetch per caller", got)
		}
	})

	t.Run("a cold entry replays the error without re-fetching", func(t *testing.T) {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		c := newTestPromptClient(t, srv.URL, PromptOptions{})
		for i := range 20 {
			_, err := c.Resolve(context.Background(), "a", "production")
			if !errors.Is(err, ErrPromptNotFound) {
				t.Fatalf("Resolve() #%d error = %v, want ErrPromptNotFound", i, err)
			}
		}
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Errorf("upstream calls = %d, want 1 — a cold failure must be replayed, not re-attempted", got)
		}
	})

	t.Run("the floor expires and the entry recovers", func(t *testing.T) {
		var fail atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if fail.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`{"name":"a","version":9,"type":"text","prompt":"fresh text"}`))
		}))
		defer srv.Close()

		c := newTestPromptClient(t, srv.URL, PromptOptions{CacheTTL: time.Nanosecond})
		c.retryFloor = time.Millisecond
		if _, err := c.Resolve(context.Background(), "a", "production"); err != nil {
			t.Fatalf("priming Resolve() error: %v", err)
		}
		fail.Store(true)
		if _, err := c.Resolve(context.Background(), "a", "production"); err != nil {
			t.Fatalf("Resolve() during outage error = %v, want the cached copy", err)
		}

		fail.Store(false)
		time.Sleep(2 * time.Millisecond)
		got, err := c.Resolve(context.Background(), "a", "production")
		if err != nil {
			t.Fatalf("Resolve() after recovery error = %v", err)
		}
		if got.Version != 9 || got.Text != "fresh text" {
			t.Errorf("Resolve() = %+v, want the re-fetched version 9 — the floor must not pin a stale copy", got)
		}
	})
}

// TestPromptClient_CancelledContextIsNotASuccess pins that a cancelled
// caller does not walk away with a stale copy and a nil error.
//
// A caller can queue on the entry lock for as long as another caller's HTTP
// timeout, and a mutex wait ignores ctx — so without an explicit check the
// serve-stale path turns "your run was cancelled" into "here is last
// minute's prompt", and the flow goes on to build an agent.
func TestPromptClient_CancelledContextIsNotASuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"a","version":1,"type":"text","prompt":"cached text"}`))
	}))
	defer srv.Close()

	c := newTestPromptClient(t, srv.URL, PromptOptions{CacheTTL: time.Nanosecond})
	if _, err := c.Resolve(context.Background(), "a", "production"); err != nil {
		t.Fatalf("priming Resolve() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := c.Resolve(ctx, "a", "production")
	if err == nil {
		t.Fatalf("Resolve() error = nil (text %q), want the cancellation to propagate", got.Text)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want one wrapping context.Canceled", err)
	}
}

// TestFlattenPrompt_UnsupportedChatShapes pins the difference between "the
// author saved a blank prompt" and "this payload is a shape we cannot use".
//
// Both used to surface as ErrPromptEmpty, because an array of objects with
// neither `role` nor `content` unmarshals into []chatMessage without error,
// every field zero — which is exactly the shape of Langfuse's `placeholder`
// block and of a chat block whose content is an array of content parts.
func TestFlattenPrompt_UnsupportedChatShapes(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantErr  string
		wantIs   error
		wantText string
	}{
		{
			name:    "content parts array is named as a shape problem",
			body:    `{"name":"a","version":1,"type":"chat","prompt":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			wantErr: "unsupported payload type",
		},
		{
			name:    "a placeholder-only chat prompt is a shape problem, not an empty one",
			body:    `{"name":"a","version":1,"type":"chat","prompt":[{"type":"placeholder","name":"chat_history"}]}`,
			wantErr: "none carry a role or text",
		},
		{
			name:     "a placeholder among real blocks keeps the real blocks",
			body:     `{"name":"a","version":1,"type":"chat","prompt":[{"role":"system","content":"be terse"},{"type":"placeholder","name":"chat_history"},{"role":"user","content":"go"}]}`,
			wantText: "system: be terse\n\nuser: go",
		},
		{
			name:   "an absent prompt field is an empty prompt",
			body:   `{"name":"a","version":1,"type":"text"}`,
			wantIs: ErrPromptEmpty,
		},
		{
			name:   "a null prompt is an empty prompt",
			body:   `{"name":"a","version":1,"type":"text","prompt":null}`,
			wantIs: ErrPromptEmpty,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c := newTestPromptClient(t, srv.URL, PromptOptions{})
			got, err := c.Resolve(context.Background(), "a", "production")

			switch {
			case tt.wantText != "":
				if err != nil {
					t.Fatalf("Resolve() error = %v, want nil", err)
				}
				if got.Text != tt.wantText {
					t.Errorf("Text = %q, want %q", got.Text, tt.wantText)
				}
			case tt.wantIs != nil:
				if !errors.Is(err, tt.wantIs) {
					t.Errorf("error = %v, want one wrapping %v", err, tt.wantIs)
				}
			default:
				if err == nil {
					t.Fatalf("Resolve() error = nil, want one containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want one containing %q", err, tt.wantErr)
				}
				if errors.Is(err, ErrPromptEmpty) {
					t.Error("error wraps ErrPromptEmpty — a shape problem must not read as a blank prompt")
				}
			}
		})
	}
}

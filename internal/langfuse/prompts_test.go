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

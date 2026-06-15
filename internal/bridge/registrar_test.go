package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// captureOrchestrator emulates the c2-agent orchestrator's
// /router/bindings/register + DELETE /router/bindings endpoints. Tests
// drive HTTPRegistrar against this and assert the wire shape +
// auth header.
type captureOrchestrator struct {
	server *httptest.Server

	authPassword string

	registerCalls   atomic.Int64
	deregisterCalls atomic.Int64
	registerBodies  atomic.Value // []RemoteBinding
	statusOverride  atomic.Int64 // when non-zero, return this status for next request
}

func newCaptureOrchestrator(t *testing.T) *captureOrchestrator {
	t.Helper()
	c := &captureOrchestrator{}
	c.registerBodies.Store([]RemoteBinding{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /router/bindings/register", func(w http.ResponseWriter, r *http.Request) {
		c.registerCalls.Add(1)
		if c.authPassword != "" {
			_, pass, ok := r.BasicAuth()
			if !ok || pass != c.authPassword {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		if override := c.statusOverride.Swap(0); override != 0 {
			w.WriteHeader(int(override))
			return
		}
		var b RemoteBinding
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		current := c.registerBodies.Load().([]RemoteBinding)
		c.registerBodies.Store(append(current, b))
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("DELETE /router/bindings", func(w http.ResponseWriter, r *http.Request) {
		c.deregisterCalls.Add(1)
		if c.authPassword != "" {
			_, pass, ok := r.BasicAuth()
			if !ok || pass != c.authPassword {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	c.server = httptest.NewServer(mux)
	t.Cleanup(c.server.Close)
	return c
}

func (c *captureOrchestrator) URL() string { return c.server.URL }

func (c *captureOrchestrator) latestRegister(t *testing.T) RemoteBinding {
	t.Helper()
	rows := c.registerBodies.Load().([]RemoteBinding)
	if len(rows) == 0 {
		t.Fatalf("no register calls captured")
	}
	return rows[len(rows)-1]
}

// --- tests -----------------------------------------------------------------

func TestNewHTTPRegistrar_RejectsEmptyBaseURL(t *testing.T) {
	t.Parallel()
	_, err := NewHTTPRegistrar(HTTPRegistrarConfig{})
	if err == nil {
		t.Fatal("expected error for empty BaseURL")
	}
}

func TestNewHTTPRegistrar_RejectsBadURL(t *testing.T) {
	t.Parallel()
	for _, u := range []string{"no-scheme", "://broken", ""} {
		_, err := NewHTTPRegistrar(HTTPRegistrarConfig{BaseURL: u})
		if err == nil {
			t.Errorf("expected error for BaseURL %q", u)
		}
	}
}

func TestHTTPRegistrar_RegisterHappyPath(t *testing.T) {
	t.Parallel()
	orch := newCaptureOrchestrator(t)
	r, err := NewHTTPRegistrar(HTTPRegistrarConfig{
		BaseURL:    orch.URL(),
		HTTPClient: orch.server.Client(),
	})
	if err != nil {
		t.Fatalf("NewHTTPRegistrar: %v", err)
	}
	b := RemoteBinding{
		ProjectID:     "default",
		Channel:       "slack",
		Identity:      "default",
		PeerID:        "D-test",
		JobID:         "job-A",
		ContainerHost: "c2-agent-job-A.svc",
		ContainerPort: 8080,
		SessionID:     "sess-1",
		MentionHandle: "@reviewer.example",
	}
	if err := r.Register(context.Background(), b); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := orch.registerCalls.Load(); got != 1 {
		t.Errorf("register calls = %d, want 1", got)
	}
	got := orch.latestRegister(t)
	if got.PeerID != "D-test" || got.JobID != "job-A" || got.ContainerHost != "c2-agent-job-A.svc" {
		t.Errorf("orchestrator received %+v", got)
	}
}

func TestHTTPRegistrar_RegisterPropagatesAuth(t *testing.T) {
	t.Parallel()
	orch := newCaptureOrchestrator(t)
	orch.authPassword = "shared-secret"
	r, _ := NewHTTPRegistrar(HTTPRegistrarConfig{
		BaseURL:    orch.URL(),
		Password:   "shared-secret",
		HTTPClient: orch.server.Client(),
	})
	if err := r.Register(context.Background(), RemoteBinding{
		ProjectID: "default", Channel: "slack", Identity: "default", PeerID: "D-1",
		JobID: "j", ContainerHost: "h", ContainerPort: 1,
	}); err != nil {
		t.Fatalf("Register with auth: %v", err)
	}
	if got := orch.registerCalls.Load(); got != 1 {
		t.Errorf("register calls = %d, want 1 (auth should have succeeded)", got)
	}
}

func TestHTTPRegistrar_RegisterAuthFailureSurfaces(t *testing.T) {
	t.Parallel()
	orch := newCaptureOrchestrator(t)
	orch.authPassword = "expected"
	r, _ := NewHTTPRegistrar(HTTPRegistrarConfig{
		BaseURL:    orch.URL(),
		Password:   "wrong",
		HTTPClient: orch.server.Client(),
	})
	err := r.Register(context.Background(), RemoteBinding{
		ProjectID: "default", Channel: "slack", Identity: "default", PeerID: "D-1",
		JobID: "j", ContainerHost: "h", ContainerPort: 1,
	})
	if err == nil {
		t.Fatal("expected error for wrong password")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v (want 401 mention)", err)
	}
}

func TestHTTPRegistrar_RegisterSurfacesNon2xx(t *testing.T) {
	t.Parallel()
	orch := newCaptureOrchestrator(t)
	orch.statusOverride.Store(int64(http.StatusServiceUnavailable))
	r, _ := NewHTTPRegistrar(HTTPRegistrarConfig{
		BaseURL:    orch.URL(),
		HTTPClient: orch.server.Client(),
	})
	err := r.Register(context.Background(), RemoteBinding{
		ProjectID: "default", Channel: "slack", Identity: "default", PeerID: "D-1",
		JobID: "j", ContainerHost: "h", ContainerPort: 1,
	})
	if err == nil {
		t.Fatal("expected error on 503")
	}
}

func TestHTTPRegistrar_DeregisterHappyPath(t *testing.T) {
	t.Parallel()
	orch := newCaptureOrchestrator(t)
	r, _ := NewHTTPRegistrar(HTTPRegistrarConfig{
		BaseURL:    orch.URL(),
		HTTPClient: orch.server.Client(),
	})
	if err := r.Deregister(context.Background(), "default", "slack", "default", "D-test"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if got := orch.deregisterCalls.Load(); got != 1 {
		t.Errorf("deregister calls = %d, want 1", got)
	}
}

func TestHTTPRegistrar_DeregisterAuthFailureSurfaces(t *testing.T) {
	t.Parallel()
	orch := newCaptureOrchestrator(t)
	orch.authPassword = "expected"
	r, _ := NewHTTPRegistrar(HTTPRegistrarConfig{
		BaseURL:    orch.URL(),
		Password:   "wrong",
		HTTPClient: orch.server.Client(),
	})
	err := r.Deregister(context.Background(), "default", "slack", "default", "D-test")
	if err == nil {
		t.Fatal("expected error for wrong password")
	}
}

// TestHTTPRegistrar_URLJoiningHandlesTrailingSlash verifies that a
// BaseURL with or without trailing slash hits the same endpoint.
// url.URL.JoinPath normalises this — locking the contract here means
// operators can configure either shape without breakage.
func TestHTTPRegistrar_URLJoiningHandlesTrailingSlash(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{"", "/"} {
		orch := newCaptureOrchestrator(t)
		r, err := NewHTTPRegistrar(HTTPRegistrarConfig{
			BaseURL:    orch.URL() + suffix,
			HTTPClient: orch.server.Client(),
		})
		if err != nil {
			t.Fatalf("NewHTTPRegistrar(suffix=%q): %v", suffix, err)
		}
		if err := r.Register(context.Background(), RemoteBinding{
			ProjectID: "default", Channel: "slack", Identity: "default", PeerID: "D-1",
			JobID: "j", ContainerHost: "h", ContainerPort: 1,
		}); err != nil {
			t.Fatalf("Register(suffix=%q): %v", suffix, err)
		}
		if got := orch.registerCalls.Load(); got != 1 {
			t.Errorf("suffix=%q: register calls = %d, want 1 (URL joining must hit /router/bindings/register regardless of trailing slash)", suffix, got)
		}
		if err := r.Deregister(context.Background(), "default", "slack", "default", "D-1"); err != nil {
			t.Fatalf("Deregister(suffix=%q): %v", suffix, err)
		}
		if got := orch.deregisterCalls.Load(); got != 1 {
			t.Errorf("suffix=%q: deregister calls = %d, want 1", suffix, got)
		}
	}
}

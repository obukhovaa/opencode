package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RemoteBinding is the wire shape the runner sends to the orchestrator's
// `POST /router/bindings/register` endpoint (openspec change
// bridge-orchestrator-mediated-inbound, Phase D). Mirrors the
// orchestrator's `routerBindingsRegisterRequest` struct field-for-field
// so a single round-trip serialisation suffices.
type RemoteBinding struct {
	ProjectID       string `json:"projectId"`
	Channel         string `json:"channel"`
	Identity        string `json:"identity"`
	PeerID          string `json:"peerId"`
	JobID           string `json:"jobId"`
	ContainerHost   string `json:"containerHost"`
	ContainerPort   int    `json:"containerPort"`
	SessionID       string `json:"sessionId,omitempty"`
	MentionHandle   string `json:"mentionHandle,omitempty"`
	ExpiresAtUnixMs int64  `json:"expiresAtUnixMs,omitempty"`
}

// RemoteRegistrar is the contract the bridge service consults at the
// boundaries of an interactive flow step (Phase F). When non-nil, the
// service calls Register at OnInteractiveStepStart AND keeps a local
// `bridge_sessions` row alongside (the orchestrator's binding table
// drives the FORWARDER's per-event lookup; the local table is the
// fast-path cache for in-process dispatch). Deregister fires from
// OnInteractiveStepComplete; failure is logged + swallowed (the
// orchestrator's TTL sweeper is the backstop).
type RemoteRegistrar interface {
	// Register upserts a binding row on the orchestrator's index.
	// MUST be idempotent — the runner may retry the same row when a
	// transient HTTP failure happens.
	Register(ctx context.Context, b RemoteBinding) error

	// Deregister removes the binding row for the given peer key.
	// MUST be idempotent — the orchestrator returns 200 even when no
	// row matches, so a missing row is a successful deregister.
	Deregister(ctx context.Context, projectID, channel, identity, peerID string) error
}

// HTTPRegistrar is the production RemoteRegistrar implementation. It
// POSTs to the orchestrator's /router/bindings/register endpoint with
// HTTP Basic auth (the same OPENCODE_SERVER_PASSWORD-style shared
// secret the orchestrator uses on its outbound /event SSE +
// /flow/status calls — Phase D's `routerBindingsRegisterRequest`
// expects this contract).
type HTTPRegistrar struct {
	baseURL  *url.URL
	password string
	client   *http.Client
}

// HTTPRegistrarConfig bundles construction-time options. BaseURL is
// the orchestrator's root (e.g. `http://c2-agent-orchestrator.c2:8080`).
// Password is the shared Basic-auth secret; empty means no auth
// header (dev posture, matches the orchestrator's empty-password
// bypass).
type HTTPRegistrarConfig struct {
	BaseURL    string
	Password   string
	HTTPClient *http.Client
	// Timeout caps each individual HTTP call. 0 → default 5s
	// (matches the orchestrator's forwarder ForwardTimeout, so
	// neither side waits longer than the other).
	Timeout time.Duration
}

// RegistrarTimeout is the default per-call timeout when
// HTTPRegistrarConfig.Timeout is unset.
const RegistrarTimeout = 5 * time.Second

// NewHTTPRegistrar constructs a registrar. Returns an error when
// BaseURL is empty or unparseable.
func NewHTTPRegistrar(cfg HTTPRegistrarConfig) (*HTTPRegistrar, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("bridge.registrar: BaseURL is required")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("bridge.registrar: parse BaseURL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("bridge.registrar: BaseURL missing scheme or host: %q", cfg.BaseURL)
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = RegistrarTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &HTTPRegistrar{
		baseURL:  u,
		password: cfg.Password,
		client:   client,
	}, nil
}

// Register implements RemoteRegistrar.
func (r *HTTPRegistrar) Register(ctx context.Context, b RemoteBinding) error {
	body, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("encode binding: %w", err)
	}
	target := r.baseURL.JoinPath("/router/bindings/register").String()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	r.applyAuth(req)
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", target, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("register binding: status %d", resp.StatusCode)
	}
	return nil
}

// deregisterRequest is the JSON shape the orchestrator's DELETE
// /router/bindings expects (mirrors `routerBindingsDeleteRequest`).
type deregisterRequest struct {
	ProjectID string `json:"projectId"`
	Channel   string `json:"channel"`
	Identity  string `json:"identity"`
	PeerID    string `json:"peerId"`
}

// Deregister implements RemoteRegistrar.
func (r *HTTPRegistrar) Deregister(ctx context.Context, projectID, channel, identity, peerID string) error {
	body, err := json.Marshal(deregisterRequest{
		ProjectID: projectID,
		Channel:   channel,
		Identity:  identity,
		PeerID:    peerID,
	})
	if err != nil {
		return fmt.Errorf("encode deregister: %w", err)
	}
	target := r.baseURL.JoinPath("/router/bindings").String()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	r.applyAuth(req)
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("DELETE %s: %w", target, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("deregister binding: status %d", resp.StatusCode)
	}
	return nil
}

func (r *HTTPRegistrar) applyAuth(req *http.Request) {
	if r.password == "" {
		return
	}
	// Username is arbitrary — the orchestrator only validates the
	// password. "opencode-runner" gives operators reading logs a
	// breadcrumb about which side is calling.
	req.SetBasicAuth("opencode-runner", r.password)
}

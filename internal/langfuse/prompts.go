package langfuse

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/logging"
)

// Langfuse Prompt Management, the read side.
//
// This is deliberately NOT part of the tracing Client. There is no official
// Langfuse Go SDK, so both halves are hand-written, but they share only
// credentials: tracing pushes spans out over OTLP, prompt management pulls
// text in over the public REST API. Either can be enabled without the other
// — a deployment that manages prompts in the Langfuse UI but ships its
// traces elsewhere is a legitimate configuration.
//
// Freshness model: every resolution is served from an in-process cache with
// a short TTL. A prompt edited in the Langfuse UI therefore lands on the
// next run once the entry expires, with no redeploy and no restart. When the
// backend is unreachable a stale entry is served rather than failing the
// caller — an agent running last minute's prompt is strictly better than an
// agent that cannot run at all.

const (
	// DefaultPromptLabel is the Langfuse label resolved when a reference
	// does not name one. Deliberately "production" and never "latest":
	// "latest" moves on every save in the UI, so a half-finished edit
	// would reach a running flow the moment its author hit save.
	DefaultPromptLabel = "production"

	defaultPromptCacheTTL = 60 * time.Second
	defaultPromptTimeout  = 10 * time.Second
)

var (
	// ErrPromptNotFound is returned when Langfuse has no prompt at the
	// requested path, or none carrying the requested label.
	ErrPromptNotFound = errors.New("langfuse: prompt not found")
	// ErrPromptUnauthorized is returned when the configured credentials are
	// rejected. Distinct from not-found because the remedy is different:
	// one is a config typo, the other is a missing prompt.
	ErrPromptUnauthorized = errors.New("langfuse: prompt fetch unauthorized")
	// ErrPromptEmpty is returned when a prompt resolves to whitespace.
	// Running an agent on an empty system prompt is never what the author
	// meant, and it fails in a way that is very hard to trace back here.
	ErrPromptEmpty = errors.New("langfuse: prompt resolved to empty text")
	// ErrPromptsDisabled is returned when a reference is resolved while
	// prompt management is not configured.
	ErrPromptsDisabled = errors.New("langfuse: prompt management is not enabled")
)

// ResolvedPrompt is one prompt fetched from Langfuse.
type ResolvedPrompt struct {
	Path    string
	Label   string
	Version int
	Text    string
}

// PromptOptions tunes the prompt client. Zero values take the defaults.
type PromptOptions struct {
	// DefaultLabel is used when a reference names no label.
	DefaultLabel string
	// CacheTTL bounds how long a resolved prompt is reused before it is
	// re-fetched. Lower values pick up UI edits sooner at the cost of more
	// requests; the cache is per process, not per flow run.
	CacheTTL time.Duration
	// Timeout bounds a single HTTP fetch.
	Timeout time.Duration
}

// PromptClient resolves Langfuse prompt references to text.
//
// Safe for concurrent use. A nil client is valid and reports Enabled() ==
// false, so callers can hold one unconditionally.
type PromptClient struct {
	baseURL      string
	auth         string
	defaultLabel string
	ttl          time.Duration
	http         *http.Client
	enabled      bool

	mu      sync.Mutex
	entries map[string]*promptEntry
}

// promptEntry is one cache slot. Its own mutex is the single-flight lock:
// concurrent resolutions of the same (path,label) serialise on it, so a cold
// cache under a fan-out of parallel steps issues one request, not N.
type promptEntry struct {
	mu        sync.Mutex
	resolved  ResolvedPrompt
	fetchedAt time.Time
	valid     bool
}

// NewPromptClient builds a prompt client. Credentials and baseURL follow the
// same resolution as the tracing client: an explicit value, an "env:VAR"
// indirection, or the conventional LANGFUSE_* environment variable. Missing
// credentials yield a disabled client rather than an error, so that a config
// which enables prompts on a machine without secrets degrades to "no prompt
// references resolve" instead of failing the boot.
func NewPromptClient(publicKey, secretKey, baseURL string, opts PromptOptions) *PromptClient {
	pk := resolveKey(publicKey, "LANGFUSE_PUBLIC_KEY")
	sk := resolveKey(secretKey, "LANGFUSE_SECRET_KEY")
	if pk == "" || sk == "" {
		return &PromptClient{enabled: false}
	}

	bu := resolveKey(baseURL, "LANGFUSE_BASE_URL")
	if bu == "" {
		bu = defaultBaseURL
	}

	label := opts.DefaultLabel
	if label == "" {
		label = DefaultPromptLabel
	}
	ttl := opts.CacheTTL
	if ttl <= 0 {
		ttl = defaultPromptCacheTTL
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultPromptTimeout
	}

	return &PromptClient{
		baseURL:      strings.TrimRight(bu, "/"),
		auth:         "Basic " + base64.StdEncoding.EncodeToString([]byte(pk+":"+sk)),
		defaultLabel: label,
		ttl:          ttl,
		http:         &http.Client{Timeout: timeout},
		enabled:      true,
		entries:      make(map[string]*promptEntry),
	}
}

// Enabled reports whether the client has usable credentials.
func (c *PromptClient) Enabled() bool {
	return c != nil && c.enabled
}

// DefaultLabel returns the label used for references that name none.
func (c *PromptClient) DefaultLabel() string {
	if c == nil || c.defaultLabel == "" {
		return DefaultPromptLabel
	}
	return c.defaultLabel
}

// Resolve returns the prompt text stored at path under label. An empty label
// selects the client's default label.
//
// A fresh cache entry is returned without contacting Langfuse. A stale entry
// triggers a re-fetch, and if that re-fetch fails the stale text is returned
// anyway (with a warning) — the alternative is failing a step over a blip in
// a service that is not on the critical path of anything else. Only a miss
// with no cached copy at all propagates the error.
func (c *PromptClient) Resolve(ctx context.Context, path, label string) (ResolvedPrompt, error) {
	if !c.Enabled() {
		return ResolvedPrompt{}, ErrPromptsDisabled
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return ResolvedPrompt{}, fmt.Errorf("langfuse: empty prompt path")
	}
	if label = strings.TrimSpace(label); label == "" {
		label = c.defaultLabel
	}

	entry := c.entryFor(path + "\x00" + label)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	if entry.valid && time.Since(entry.fetchedAt) < c.ttl {
		return entry.resolved, nil
	}

	resolved, err := c.fetch(ctx, path, label)
	if err != nil {
		if entry.valid {
			logging.Warn("langfuse: prompt fetch failed, serving cached copy",
				"path", path, "label", label, "age", time.Since(entry.fetchedAt).String(), "error", err)
			return entry.resolved, nil
		}
		return ResolvedPrompt{}, err
	}

	entry.resolved = resolved
	entry.fetchedAt = time.Now()
	entry.valid = true
	return resolved, nil
}

func (c *PromptClient) entryFor(key string) *promptEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		return e
	}
	e := &promptEntry{}
	c.entries[key] = e
	return e
}

// promptResponse is the subset of Langfuse's prompt payload we consume.
// Prompt is left as raw JSON because its shape depends on Type: a string for
// a text prompt, an array of role/content blocks for a chat prompt.
type promptResponse struct {
	Name    string          `json:"name"`
	Version int             `json:"version"`
	Type    string          `json:"type"`
	Prompt  json.RawMessage `json:"prompt"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (c *PromptClient) fetch(ctx context.Context, path, label string) (ResolvedPrompt, error) {
	// Prompt names may contain slashes — Langfuse renders them as folders
	// in its UI — so the name is a single escaped path segment, not a
	// sub-path. url.PathEscape leaves "/" alone, hence QueryEscape.
	endpoint := fmt.Sprintf("%s/api/public/v2/prompts/%s?label=%s",
		c.baseURL, url.QueryEscape(path), url.QueryEscape(label))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ResolvedPrompt{}, fmt.Errorf("langfuse: building prompt request: %w", err)
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return ResolvedPrompt{}, fmt.Errorf("langfuse: fetching prompt %q (label %q): %w", path, label, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ResolvedPrompt{}, fmt.Errorf("%w: %q (label %q)", ErrPromptNotFound, path, label)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ResolvedPrompt{}, fmt.Errorf("%w: %q (HTTP %d)", ErrPromptUnauthorized, path, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return ResolvedPrompt{}, fmt.Errorf("langfuse: fetching prompt %q (label %q): HTTP %d", path, label, resp.StatusCode)
	}

	var payload promptResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return ResolvedPrompt{}, fmt.Errorf("langfuse: decoding prompt %q: %w", path, err)
	}

	text, err := flattenPrompt(path, payload)
	if err != nil {
		return ResolvedPrompt{}, err
	}
	if strings.TrimSpace(text) == "" {
		return ResolvedPrompt{}, fmt.Errorf("%w: %q (label %q, version %d)", ErrPromptEmpty, path, label, payload.Version)
	}

	return ResolvedPrompt{
		Path:    path,
		Label:   label,
		Version: payload.Version,
		Text:    text,
	}, nil
}

// flattenPrompt renders a Langfuse prompt payload as plain text.
//
// A chat prompt is accepted and flattened by joining its blocks as
// "role: content" in source order, with a log line so the flattening is
// visible. Both consumers here — a flow step's user prompt and an agent
// type's system prompt — are single strings; a chat prompt is a shape
// Langfuse offers, not a shape opencode has anywhere to put.
func flattenPrompt(path string, payload promptResponse) (string, error) {
	// Langfuse omits `type` on older text prompts, so switch on the
	// payload's actual JSON shape and treat `type` as a hint only.
	var text string
	if err := json.Unmarshal(payload.Prompt, &text); err == nil {
		return text, nil
	}

	var msgs []chatMessage
	if err := json.Unmarshal(payload.Prompt, &msgs); err != nil {
		return "", fmt.Errorf("langfuse: prompt %q has unsupported payload type %q", path, payload.Type)
	}

	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		if m.Role == "" {
			parts = append(parts, m.Content)
			continue
		}
		parts = append(parts, m.Role+": "+m.Content)
	}
	logging.Info("langfuse: flattened chat prompt to text", "path", path, "blocks", len(parts))
	return strings.Join(parts, "\n\n"), nil
}

// Warm pre-fetches the given references so the first real resolution is a
// cache hit rather than a cold round trip on the critical path of a run.
//
// Best-effort by contract: every failure is logged and swallowed. A prompt
// that cannot be pre-fetched will simply be fetched (and fail loudly) at use
// time, which is the right moment to fail — never at boot, where it would
// take down a process whose other flows are perfectly runnable.
func (c *PromptClient) Warm(ctx context.Context, refs []PromptRef) {
	if !c.Enabled() || len(refs) == 0 {
		return
	}

	const maxParallel = 4
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup

	seen := make(map[PromptRef]struct{}, len(refs))
	for _, ref := range refs {
		if ref.Path == "" {
			continue
		}
		if ref.Label == "" {
			ref.Label = c.defaultLabel
		}
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}

		wg.Add(1)
		sem <- struct{}{}
		go func(ref PromptRef) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := c.Resolve(ctx, ref.Path, ref.Label); err != nil {
				logging.Warn("langfuse: prompt warm-up failed",
					"path", ref.Path, "label", ref.Label, "error", err)
			}
		}(ref)
	}
	wg.Wait()
	logging.Info("langfuse: prompt cache warmed", "references", len(seen))
}

// PromptRef is a reference to a managed prompt: where it lives and which
// label to resolve. The empty Label means "the client's default label".
type PromptRef struct {
	Path  string
	Label string
}

// --- Global singleton ---

var globalPrompts *PromptClient

// InitPrompts creates the global prompt client. Called once at startup,
// alongside Init. Returns true if the client is enabled.
func InitPrompts(publicKey, secretKey, baseURL string, opts PromptOptions) bool {
	globalPrompts = NewPromptClient(publicKey, secretKey, baseURL, opts)
	return globalPrompts.Enabled()
}

// GetPrompts returns the global prompt client. The nil client returned before
// InitPrompts is a valid receiver: it reports Enabled() == false and every
// Resolve fails with ErrPromptsDisabled.
func GetPrompts() *PromptClient {
	return globalPrompts
}

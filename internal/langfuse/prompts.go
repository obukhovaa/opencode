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

	// defaultPromptRetryFloor is how long a failed fetch suppresses further
	// attempts on the same entry.
	//
	// Without it a stale entry stays stale for the whole outage (a failed
	// re-fetch cannot refresh fetchedAt — there is nothing to refresh it
	// with), so every caller re-attempts, and because the fetch happens
	// under the entry lock those attempts serialise: N callers pay N × the
	// HTTP timeout, which at the 10s default is minutes of stall for a
	// prompt whose cached copy was sitting right there. With the floor the
	// first caller pays one timeout and the rest are served the cached copy
	// immediately.
	defaultPromptRetryFloor = 10 * time.Second
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
	retryFloor   time.Duration
	http         *http.Client
	enabled      bool

	mu      sync.Mutex
	entries map[string]*promptEntry
}

// promptEntry is one cache slot. Its own mutex is the single-flight lock:
// concurrent resolutions of the same (path,label) serialise on it, so a cold
// cache under a fan-out of parallel steps issues one request, not N.
//
// lastAttempt/lastErr are the failure half of that guarantee. A fetch that
// fails cannot advance fetchedAt, so without them the entry would be stale
// forever and every caller would re-attempt — turning the single-flight lock
// into a queue of full HTTP timeouts. See defaultPromptRetryFloor.
type promptEntry struct {
	mu          sync.Mutex
	resolved    ResolvedPrompt
	fetchedAt   time.Time
	valid       bool
	lastAttempt time.Time
	lastErr     error
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
		retryFloor:   defaultPromptRetryFloor,
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
//
// A failure is remembered for retryFloor, so a sustained outage costs one
// request per entry per floor rather than one per caller. Both a cached copy
// and a cached error are replayed during that window.
func (c *PromptClient) Resolve(ctx context.Context, path, label string) (ResolvedPrompt, error) {
	if !c.Enabled() {
		return ResolvedPrompt{}, ErrPromptsDisabled
	}
	path = strings.TrimSpace(path)
	if path == "" {
		// Deliberately not one of the sentinels: an empty path is a caller
		// bug, not a condition Langfuse reported.
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

	// A caller can sit on entry.mu for as long as another caller's HTTP
	// timeout, and a mutex wait ignores ctx. Re-check it here: otherwise a
	// caller whose deadline passed while queued goes on to issue a request
	// nobody is waiting for, or — worse — is handed a stale copy and a nil
	// error, so a cancelled run reads as a successful resolution.
	if err := ctx.Err(); err != nil {
		return ResolvedPrompt{}, fmt.Errorf("langfuse: resolving prompt %q (label %q): %w", path, label, err)
	}

	// A recent attempt already failed. Replay its outcome rather than
	// re-fetching: the backend is down either way, and re-attempting per
	// caller serialises a full HTTP timeout behind this lock for each one.
	if entry.lastErr != nil && time.Since(entry.lastAttempt) < c.retryFloor {
		if entry.valid {
			return entry.resolved, nil
		}
		return ResolvedPrompt{}, entry.lastErr
	}

	resolved, err := c.fetch(ctx, path, label)
	entry.lastAttempt = time.Now()
	if err != nil {
		entry.lastErr = err
		if entry.valid {
			logging.Warn("langfuse: prompt fetch failed, serving cached copy",
				"path", path, "label", label, "age", time.Since(entry.fetchedAt).String(), "error", err)
			return entry.resolved, nil
		}
		return ResolvedPrompt{}, err
	}

	entry.lastErr = nil
	entry.resolved = resolved
	entry.fetchedAt = entry.lastAttempt
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
	// in its UI — so the name is a single escaped path SEGMENT, not a
	// sub-path. PathEscape is the segment escaper: it encodes "/" as %2F
	// (which is what keeps a folder path in one segment) and, unlike
	// QueryEscape, encodes " " as %20 rather than "+". A "+" is a literal
	// plus inside a path, so QueryEscape would silently ask for the name
	// "team+review" when the author wrote "team review".
	//
	// label IS a query parameter, so QueryEscape is correct there.
	endpoint := fmt.Sprintf("%s/api/public/v2/prompts/%s?label=%s",
		c.baseURL, url.PathEscape(path), url.QueryEscape(label))

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
	// An absent `prompt` field is an empty prompt, not an unknown shape.
	// Reporting it as "unsupported payload type" would point the reader at
	// the type hint instead of at the missing text.
	if len(payload.Prompt) == 0 {
		return "", nil
	}

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
	recognised := 0
	for _, m := range msgs {
		// A block with a role or a content string is a chat block this
		// understands, even if its content is blank. One with neither is
		// some other shape that merely decoded without error.
		if m.Role != "" || m.Content != "" {
			recognised++
		}
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		if m.Role == "" {
			parts = append(parts, m.Content)
			continue
		}
		parts = append(parts, m.Role+": "+m.Content)
	}
	// An array of objects carrying neither role nor content unmarshals into
	// []chatMessage without error, every field zero — Langfuse's own
	// `placeholder` block is exactly that shape, as is a chat block whose
	// content is an array of content parts. Say so, rather than letting the
	// caller's ErrPromptEmpty blame the author for saving a blank prompt.
	//
	// Keyed on recognised rather than on len(parts): a prompt whose blocks
	// DO carry roles but no text really is empty, and ErrPromptEmpty is the
	// right answer there.
	if len(msgs) > 0 && recognised == 0 {
		return "", fmt.Errorf("langfuse: prompt %q (type %q) has %d message block(s) but none carry a role or text — an unsupported chat shape, not an empty prompt",
			path, payload.Type, len(msgs))
	}
	// Blocks are dropped, not just reformatted: a `placeholder` block is a
	// runtime injection point opencode has nothing to fill, and a block
	// whose content is empty contributes nothing. Log both counts so a
	// silent drop is visible rather than inferred from the prompt reading
	// oddly.
	if len(parts) != len(msgs) {
		logging.Warn("langfuse: chat prompt blocks dropped while flattening to text",
			"path", path, "blocks_seen", len(msgs), "blocks_kept", len(parts))
	}
	logging.Info("langfuse: flattened chat prompt to text",
		"path", path, "blocks_seen", len(msgs), "blocks_kept", len(parts))
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

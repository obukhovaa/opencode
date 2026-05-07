# Fetch Tool — Cloudflare Challenge Detection and Retry

**Date**: 2026-02-23
**Status**: Implemented
**Author**: AI-assisted
**Updated**: 2026-05-07 — Actualised against current codebase

## Overview

When the webfetch tool requests a Cloudflare-protected URL, it receives an HTTP 403 with a bot-challenge page instead of the actual content. This spec adds detection of Cloudflare (and similar WAF) challenges and a single retry with a browser-like User-Agent to recover from the most common failure mode.

## Motivation

### Current State

`internal/llm/tools/webfetch.go` sends every request with a minimal User-Agent and treats any non-200 response as a hard failure (lines 176, 193–195):

```go
req.Header.Set("User-Agent", "opencode/1.0")

// ...

if resp.StatusCode != http.StatusOK {
    return NewTextErrorResponse(fmt.Sprintf("Request failed with status code: %d", resp.StatusCode)), nil
}
```

There is no retry logic of any kind. The HTTP client uses Go's default transport with no custom settings. (Note: the tool does have a permission system, Accept headers, Content-Length pre-checks, and binary content detection — all of which were added by Phase 2 of the parent spec. None of these affect the retry logic.)

This creates two problems:

1. **Silent failure on documentation sites**: Many popular documentation sites (Cloudflare Docs, Vercel Docs, Tailwind CSS, etc.) are behind Cloudflare. The tool returns `"Request failed with status code: 403"` with no indication that a retry with a different User-Agent would succeed.
2. **No actionable signal**: The agent sees a generic 403 and has no way to distinguish a Cloudflare challenge from a genuine authorization failure. It typically gives up or asks the user.

### Desired State

When a Cloudflare challenge is detected, the tool retries once with a browser-like User-Agent and returns the content if the retry succeeds. If the retry also fails, the error message includes the Cloudflare context so the agent can inform the user accurately:

```
Request blocked by Cloudflare bot protection (cf-mitigated: challenge). 
Retry with browser User-Agent also failed (status: 403).
```

## Research Findings

### Cloudflare Challenge Signals

Cloudflare challenge responses are reliably identified by the `cf-mitigated: challenge` response header. This header is set by Cloudflare's edge network specifically to indicate a bot challenge page — it is not present on legitimate 403 responses from the origin server.

| Signal | Reliability | Notes |
|--------|-------------|-------|
| `cf-mitigated: challenge` header | High | Definitive — Cloudflare-specific, not set by origin |
| `cf-ray` header + 403 status | Medium | `cf-ray` is present on all Cloudflare responses, not just challenges |
| Body contains `Just a moment...` | Low | Fragile — depends on Cloudflare's HTML template, may change |
| Body contains `/cdn-cgi/challenge-platform/` | Low | Fragile — same concern |

**Key finding**: `cf-mitigated: challenge` is the only signal worth checking. The body-based heuristics are fragile and require reading the full response body before detecting the challenge, which wastes bandwidth.

### Other WAF Providers

| Provider | Detection Signal | Prevalence |
|----------|-----------------|------------|
| Cloudflare | `cf-mitigated: challenge` | Very high |
| Akamai | `x-check-cacheable` or `akamai-x-*` headers | Medium |
| AWS WAF | `x-amzn-waf-*` headers | Medium |
| Imperva | `x-iinfo` header | Low |

**Key finding**: No single header covers all WAF providers. Akamai, AWS WAF, and Imperva do not have a reliable "bot challenge" header equivalent to `cf-mitigated`. Their 403 responses are often indistinguishable from origin 403s without reading the body.

**Implication**: Implement Cloudflare detection now (high value, reliable signal). Design the detection function to be extensible but do not add other WAF providers in this iteration.

### Browser User-Agent Effectiveness

Cloudflare's JavaScript challenge is bypassed by a browser UA in many cases because Cloudflare's bot scoring uses the UA as one input. A realistic Chrome UA on macOS is sufficient for most documentation sites. Sites with stricter bot protection (e.g., requiring TLS fingerprint matching or JavaScript execution) will not be recoverable by UA alone — those are out of scope.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Detection signal | `cf-mitigated: challenge` header only | Reliable, no body read required, Cloudflare-specific |
| Retry count | 1 | A second retry with the same UA would not help; multiple UAs add complexity without clear benefit |
| Browser UA | Single hardcoded Chrome/macOS string | Sufficient for documentation sites; a UA list adds complexity with marginal gain |
| Additional retry headers | `Accept-Language: en-US,en;q=0.9` + original `Accept` | Low-cost, improves success rate on stricter configs. No `Accept-Encoding` — Go handles it |
| WAF extensibility | Internal `isWAFChallenge(resp)` function | Isolates detection logic; other providers can be added without touching retry flow |
| 429 handling | Out of scope | Related but separate concern; deserves its own spec and backoff strategy |
| Retry client | Reuse existing `client` (same timeout) | No reason to change timeout on retry; keep it simple |
| Body handling on retry | Drain + close before retry | Required for Go HTTP transport connection reuse; restructure away from `defer` |
| Error message on double failure | Include WAF context | Gives the agent accurate signal to report to the user |
| Ethical framing | Browser UA for public documentation access | Not bypassing authentication — presenting as a legitimate browser to access public content |

## Architecture

```
fetchTool.Run()
    │
    ├── parse params, validate, check permission (existing logic)
    │
    ├── build request (existing logic)
    │       └── User-Agent: "opencode/1.0"
    │       └── Accept: format-specific header
    │
    ├── client.Do(req) → resp
    │
    ├── if resp.StatusCode == 200 → process body (existing path)
    │
    ├── if isWAFChallenge(resp)
    │       │
    │       ├── drain & close initial resp.Body (release connection)
    │       │
    │       ├── build retryReq with browser User-Agent + Accept-Language
    │       │
    │       ├── client.Do(retryReq) → retryResp
    │       │
    │       ├── if retryResp.StatusCode == 200 → process body (existing path)
    │       │
    │       └── else → return WAF-aware error message
    │
    └── else → existing non-200 error (unchanged)
```

```
isWAFChallenge(resp *http.Response) bool
────────────────────────────────────────
return resp.StatusCode == http.StatusForbidden &&
       resp.Header.Get("cf-mitigated") == "challenge"
```

## Implementation Plan

### Phase 1: Detection and Retry

- [x] **1.1** Add `isWAFChallenge(resp *http.Response) bool` function in `internal/llm/tools/webfetch.go`. Check `resp.StatusCode == 403` and `resp.Header.Get("cf-mitigated") == "challenge"`.

- [x] **1.2** Add the browser User-Agent constant and retry Accept-Language header:
  ```go
  const browserUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
  ```
  (Chrome version bumped to 131 to stay current; exact version is not critical.)

- [x] **1.3** In `fetchTool.Run()`, restructure the response handling after `client.Do(req)` (currently line 187). The current code uses `defer resp.Body.Close()` followed by a `resp.StatusCode != http.StatusOK` check. Restructure as follows:
  - Remove the `defer resp.Body.Close()` and manage body closing manually to properly handle the retry case.
  - If `resp.StatusCode == http.StatusOK`: proceed to body processing (existing path), closing body at end.
  - If `isWAFChallenge(resp)`: drain and close `resp.Body` via `io.Copy(io.Discard, resp.Body)` + `resp.Body.Close()` to release the connection back to the pool. Build a new request with `browserUserAgent` and `Accept-Language: en-US,en;q=0.9`. Copy the same `Accept` header from the original request. Execute retry with the same `client` (preserves user timeout). If retry succeeds (200): fall through to body-processing logic using `retryResp`. If retry fails: return `NewTextErrorResponse` with WAF context and retry status code.
  - Else (non-200, non-WAF): existing error path (unchanged).
  - Ensure all code paths close their response bodies. A helper or reassigning `resp` after retry keeps the downstream processing code DRY.

- [x] **1.4** Update `fetchToolDescription` (line 41) to replace the limitation note `"Some websites may block automated requests"` (line 69) with `"Automatically retries with a browser User-Agent when Cloudflare bot protection is detected"`.

- [x] **1.5** Add unit tests to the existing `internal/llm/tools/webfetch_test.go` (file already exists with `TestIsBinaryContent`):
  - `TestIsWAFChallenge`: 403 + `cf-mitigated: challenge` (true), 403 without header (false), 200 + header (false), 403 + `cf-mitigated: other_value` (false).
  - `TestFetchRetryOnCloudflareChallenge`: use an `httptest.Server` with a request counter that returns 403 + `cf-mitigated: challenge` on the first request and 200 with HTML body on the second. Verify the tool returns content (not an error) and the server received exactly 2 requests. Verify the second request has the browser User-Agent and Accept-Language headers.
  - `TestFetchDoubleCloudflareFailure`: server always returns 403 + challenge header; verify error response contains `"Cloudflare"` and `"cf-mitigated"`.
  - `TestFetchNonCloudflare403`: server returns 403 without `cf-mitigated` header; verify the original error path fires and only 1 request was made.

### Phase 2: Extensibility (deferred)

- [ ] **2.1** If other WAF providers are added, extract detection into a `[]wafDetector` slice and iterate. Each detector returns a `(detected bool, providerName string)` pair. The retry logic and error messages remain unchanged.

## Edge Cases

### Retry response is also a WAF challenge

1. Initial request returns 403 + `cf-mitigated: challenge`.
2. Retry with browser UA also returns 403 + `cf-mitigated: challenge`.
3. Do not retry again — return the WAF-aware error message.
4. No infinite loop risk: retry is attempted exactly once.

### Initial response body must be drained before retry

1. `client.Do(req)` returns a response with a body.
2. Before building the retry request, drain the body with `io.Copy(io.Discard, resp.Body)` and then call `resp.Body.Close()`. Draining (not just closing) is necessary for Go's HTTP transport to reuse the connection for the retry request.
3. The current code uses `defer resp.Body.Close()` (line 192). The implementation must restructure this — either remove the defer and manage closes manually across all code paths, or reassign `resp` after retry and keep the defer only for the final response. Both approaches work; the key constraint is that every response body must be closed exactly once, and the initial body must be drained before retry to enable connection reuse.

### Cloudflare returns 403 without `cf-mitigated` (genuine origin 403)

1. `isWAFChallenge` returns false.
2. Existing error path fires: `"Request failed with status code: 403"`.
3. No retry is attempted — correct behavior.

### Custom timeout client on retry

1. User specified a custom timeout; `client` is a per-request `*http.Client`.
2. The retry uses the same `client` variable — timeout is preserved.
3. No special handling needed.

### Context cancellation between initial request and retry

1. `ctx` is cancelled after the initial response is received.
2. `http.NewRequestWithContext(ctx, ...)` for the retry request will propagate the cancellation.
3. `client.Do(retryReq)` returns a context error immediately.
4. Return the context error via the existing `fmt.Errorf("failed to fetch URL: %w", err)` path.

## Resolved Questions

1. **Should the retry use additional browser-like headers?**
   - **Decision**: Yes — add `Accept-Language: en-US,en;q=0.9` on retry only. Do not add `Accept-Encoding` — Go's transport handles this automatically. Copy the original `Accept` header from the first request.

2. **Should the initial request also use the browser UA?**
   - **Decision**: No. Keep `opencode/1.0` as the default. It's honest about the client identity for sites that don't block it. Changing the default UA is a broader policy decision out of scope.

3. **Should 429 (rate limiting) get a retry with backoff in this same change?**
   - **Decision**: No. Deferred. 429 handling requires backoff logic and respecting `Retry-After` values — different risk profile from a single immediate retry.

## Success Criteria

- [x] `isWAFChallenge` correctly identifies Cloudflare challenge responses and ignores other 403s — verified by unit tests.
- [x] Fetching a URL that returns 403 + `cf-mitigated: challenge` on the first attempt succeeds if the retry returns 200 — verified by `httptest.Server` test.
- [x] Double-failure returns an error message containing `"Cloudflare"` or `"cf-mitigated"` — verified by test.
- [x] Non-Cloudflare 403 responses are unaffected — verified by test.
- [x] No connection leak: initial response body is always closed before retry.
- [x] `go test ./internal/llm/tools/...` passes.
- [x] `make test` passes.

## References

- `internal/llm/tools/webfetch.go` — primary implementation file; `fetchTool.Run()` (line 112), `NewFetchTool()` (line 79), `fetchToolDescription` (line 41)
- `internal/llm/tools/webfetch_test.go` — existing test file with `TestIsBinaryContent`; add new tests here
- `internal/llm/tools/tools.go` — `NewTextErrorResponse`, `NewEmptyResponse`, shared response types
- `spec/20260223T133437-tools-imrovements.md` — parent spec; this feature is item 3.4

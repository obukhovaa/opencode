# Fetch Tool — Cloudflare Challenge Detection and Retry

**Date**: 2026-02-23
**Status**: Draft
**Author**: AI-assisted

## Overview

When the fetch tool requests a Cloudflare-protected URL, it receives an HTTP 403 with a bot-challenge page instead of the actual content. This spec adds detection of Cloudflare (and similar WAF) challenges and a single retry with a browser-like User-Agent to recover from the most common failure mode.

## Motivation

### Current State

`internal/llm/tools/fetch.go` sends every request with a minimal User-Agent and treats any non-200 response as a hard failure:

```go
req.Header.Set("User-Agent", "opencode/1.0")

// ...

if resp.StatusCode != http.StatusOK {
    return NewTextErrorResponse(fmt.Sprintf("Request failed with status code: %d", resp.StatusCode)), nil
}
```

There is no retry logic of any kind. The HTTP client uses Go's default transport with no custom settings.

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
| WAF extensibility | Internal `isWAFChallenge(resp)` function | Isolates detection logic; other providers can be added without touching retry flow |
| 429 handling | Out of scope | Related but separate concern; deserves its own spec and backoff strategy |
| Retry client | Reuse existing `client` (same timeout) | No reason to change timeout on retry; keep it simple |
| Error message on double failure | Include WAF context | Gives the agent accurate signal to report to the user |
| Ethical framing | Browser UA for public documentation access | Not bypassing authentication — presenting as a legitimate browser to access public content |

## Architecture

```
fetchTool.Run()
    │
    ├── build request (existing logic)
    │       └── User-Agent: "opencode/1.0"
    │
    ├── client.Do(req) → resp
    │
    ├── if resp.StatusCode == 200 → process body (existing path)
    │
    ├── if isWAFChallenge(resp)
    │       │
    │       ├── build retryReq with browser User-Agent
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

- [ ] **1.1** Add `isWAFChallenge(resp *http.Response) bool` function in `internal/llm/tools/fetch.go`. Check `resp.StatusCode == 403` and `resp.Header.Get("cf-mitigated") == "challenge"`.

- [ ] **1.2** Add the browser User-Agent constant:
  ```go
  const browserUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
  ```

- [ ] **1.3** In `fetchTool.Run()`, after the initial `client.Do(req)` call, add the WAF challenge branch before the existing non-200 error return:
  - If `isWAFChallenge(resp)`: close the initial response body, build a new request with `browserUserAgent`, execute it, and proceed with the retry response.
  - If retry succeeds (200): fall through to the existing body-processing logic.
  - If retry fails: return `NewTextErrorResponse` with a message that includes the WAF context and the retry status code.

- [ ] **1.4** Update `fetchToolDescription` to remove the limitation note `"Some websites may block automated requests"` and replace with `"Automatically retries with a browser User-Agent when Cloudflare bot protection is detected."`.

- [ ] **1.5** Add unit tests in `internal/llm/tools/fetch_test.go` (create if absent):
  - Test `isWAFChallenge` with: 403 + `cf-mitigated: challenge` (true), 403 without header (false), 200 + header (false).
  - Test retry flow using an `httptest.Server` that returns 403 + `cf-mitigated: challenge` on the first request and 200 on the second.
  - Test double-failure path: server always returns 403 + challenge header; verify error message contains WAF context.

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
2. Before building the retry request, call `resp.Body.Close()` on the initial response.
3. Failure to close leaks the connection back to the transport pool.

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

## Open Questions

1. **Should the retry use additional browser-like headers (e.g., `Accept-Language`, `Accept-Encoding`)?**
   - Cloudflare's scoring considers multiple signals. Adding `Accept-Language: en-US,en;q=0.9` and `Accept-Encoding: gzip, deflate, br` makes the request look more like a real browser.
   - Options: (a) add a small set of common browser headers on retry, (b) keep it minimal (UA only).
   - **Recommendation**: Add `Accept-Language: en-US,en;q=0.9` on retry only. It's low-cost and improves success rate on stricter configurations. Do not add `Accept-Encoding` — Go's transport handles this automatically.

2. **Should the initial request also use the browser UA?**
   - Using `browserUserAgent` for all requests would eliminate the need for detection and retry entirely.
   - Options: (a) always use browser UA, (b) use `opencode/1.0` first and retry with browser UA on challenge.
   - **Recommendation**: Keep `opencode/1.0` as the default. It's honest about the client identity for sites that don't block it, and the retry handles the Cloudflare case. Changing the default UA for all requests is a broader policy decision that should be made explicitly.

3. **Should 429 (rate limiting) get a retry with backoff in this same change?**
   - The retry infrastructure added here could be extended to handle 429 with a `Retry-After` header.
   - Options: (a) add 429 handling now, (b) defer to a separate spec.
   - **Recommendation**: Defer. 429 handling requires backoff logic and respecting `Retry-After` values that could be large (minutes). That's a different risk profile from a single immediate retry.

## Success Criteria

- [ ] `isWAFChallenge` correctly identifies Cloudflare challenge responses and ignores other 403s — verified by unit tests.
- [ ] Fetching a URL that returns 403 + `cf-mitigated: challenge` on the first attempt succeeds if the retry returns 200 — verified by `httptest.Server` test.
- [ ] Double-failure returns an error message containing `"Cloudflare"` or `"cf-mitigated"` — verified by test.
- [ ] Non-Cloudflare 403 responses are unaffected — verified by test.
- [ ] No connection leak: initial response body is always closed before retry.
- [ ] `go test ./internal/llm/tools/...` passes.
- [ ] `make test` passes.

## References

- `internal/llm/tools/fetch.go` — primary implementation file; `fetchTool.Run()`, `NewFetchTool()`, `fetchToolDescription`
- `internal/llm/tools/tools.go` — `NewTextErrorResponse`, `NewEmptyResponse`, shared response types
- `spec/20260223T133437-tools-imrovements.md` — parent spec; this feature is item 3.4

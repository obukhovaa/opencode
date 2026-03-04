# Web Search Tool

**Date**: 2026-03-03
**Status**: Draft
**Author**: AI-assisted

## Overview

Add a `websearch` tool that lets agents search the web via configurable search providers. The tool calls a standard `/v1/search` HTTP endpoint (following the LiteLLM/Perplexity search API spec) and returns results as formatted text. Search providers are configured in `.opencode.json` and injected into the tool via a registry, making the tool provider-agnostic.

## Motivation

### Current State

Agents have no way to access current web information. The `fetch` tool can retrieve a known URL, and `sourcegraph` can search public code — but neither supports general web search.

```go
// internal/llm/agent/tools.go — current viewer tools
viewerToolNames = []string{
    tools.LSToolName,
    tools.GlobToolName,
    tools.GrepToolName,
    tools.ViewToolName,
    tools.ViewImageToolName,
    tools.FetchToolName,
    tools.SkillToolName,
    tools.SourcegraphToolName,
}
```

This creates problems:

1. **No access to current information**: Agents cannot look up documentation, error messages, recent releases, or any information beyond their training cutoff.
2. **Workaround fragility**: Users paste URLs manually, or agents use `fetch` to scrape search engine HTML — both unreliable and wasteful of context.
3. **No provider flexibility**: Different teams use different search providers (Brave, DuckDuckGo, Tavily, etc.). There is no abstraction for this.

### Desired State

Agents can search the web using a dedicated tool. Providers are configured per-project or globally and the LLM selects which provider to use based on the available options presented in the tool description.

```json
{
  "webSearch": {
    "providers": {
      "ddg": {
        "baseUrl": "https://litellm.proxy.io/v1/search/ddg"
      },
      "brave": {
        "baseUrl": "https://litellm.proxy.io/v1/search/brave",
        "description": "Brave — privacy-focused web search with independent index"
      }
    }
  }
}
```

```
> agent uses websearch tool
Searching the web: "kubernetes pod restart policy"...

## Results

1. **Restart Policy - Kubernetes Documentation**
   https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#restart-policy
   A PodSpec has a restartPolicy field with possible values Always, OnFailure, and Never...

2. **Configure Pod restart policy | Google Kubernetes Engine**
   https://cloud.google.com/kubernetes-engine/docs/how-to/restart-policy
   You can set a restart policy for each container in your Pod specification...
```

## Research Findings

### Standard Search API (LiteLLM / Perplexity Format)

LiteLLM's `/v1/search` endpoint follows the Perplexity API specification. Key aspects:

- Provider is selected via URL path: `POST /v1/search/{provider-name}`
- Alternatively via body field: `{"search_tool_name": "provider-name"}`
- Request body: `{"query": "...", "max_results": 10}`
- Auth: `Authorization: Bearer <api_key>`

**Response format** (Perplexity-compatible):

```json
{
  "object": "search",
  "results": [
    {
      "title": "Page Title",
      "url": "https://example.com/page",
      "snippet": "Relevant text excerpt from the page...",
      "date": "2026-01-15"
    }
  ]
}
```

**Key finding**: The response format is standardised across all LiteLLM search providers (DuckDuckGo, Brave, Tavily, Exa, Perplexity, etc.). We only need to support one response schema.

**Implication**: The tool can be fully provider-agnostic. Provider selection happens at the URL/config level, not in code.

### Tavily Search API (Direct)

Tavily's native API at `api.tavily.com/search` uses a richer request schema (supports `search_depth`, `topic`, `time_range`, `include_domains`, `exclude_domains`, `include_raw_content`, etc.) but the core request/response aligns with the LiteLLM standard: `query` + `max_results` in, `results[]` with `title`/`url`/`content` out.

**Key finding**: Tavily uses `content` instead of `snippet` for the text excerpt. The tool should normalize both field names.

### Reference Implementation (TypeScript/OpenCode fork)

The anomalyco/opencode fork implements `WebSearchTool` using Exa AI's MCP protocol. Key patterns:

- Permission model: `ctx.ask({permission: "websearch", patterns: [params.query]})` — user approves each search query
- Timeout: 25 seconds via `AbortController`
- Tool description includes the current year so the LLM searches for recent info

**Key finding**: Including the current year in the tool description is a simple but effective way to prevent the LLM from searching for outdated information.

### Existing Tool Patterns in OpenCode

| Pattern | Fetch Tool | Sourcegraph Tool | MCP Tools |
|---------|-----------|-----------------|-----------|
| Permission | `permissions.Request()` | None | `EvaluatePermission()` + `Request()` |
| Config injection | None (stateless) | None (stateless) | `MCPRegistry` injected into `NewToolSet()` |
| HTTP client | Own `*http.Client` | Own `*http.Client` | MCP client per call |
| Tool category | `viewerToolNames` | `viewerToolNames` | Separate goroutine |
| Auth | None | None (public API) | Per-server config |

**Key finding**: The web search tool most closely resembles `fetch` (permission-gated, HTTP-based, viewer tool) but needs config injection like MCP tools.

**Implication**: A `SearchProviderRegistry` should be created, similar to `MCPRegistry`, and injected into the tool constructor. The registry reads from config and provides the list of available providers.

### API Key Resolution

The existing `LOCAL_ENDPOINT_API_KEY` environment variable is used by the local LLM provider (in `internal/llm/models/local.go`) to authenticate against a LiteLLM proxy. Since web search providers typically sit behind the same LiteLLM proxy, this key serves as a natural fallback.

**Resolution order for each provider's API key**:
1. Per-provider `apiKey` field (literal string or `env:VAR_NAME`)
2. `LOCAL_ENDPOINT_API_KEY` environment variable (fallback)

This means a typical LiteLLM setup needs zero API key configuration in the `webSearch` section — just the base URLs.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| API standard | LiteLLM/Perplexity `/v1/search` format | De facto standard supported by 10+ providers via LiteLLM proxy. Avoids vendor lock-in. |
| Provider selection | LLM chooses via required `provider` tool parameter from options listed in tool description | Agents pick the best provider for the query (e.g., news vs. code). Follows existing pattern where tool descriptions list available options (see skill tool). Required param avoids default-selection complexity. |
| Config section | `webSearch.providers` map in `.opencode.json` | Consistent with `mcpServers` pattern. Each provider has `baseUrl` + optional `apiKey` + optional `description`. |
| API key fallback | Per-provider `apiKey` → `LOCAL_ENDPOINT_API_KEY` env var | LiteLLM setups share one key across all endpoints. Reusing the existing env var means zero extra config for the common case. |
| Provider description | Optional `description` field in config, with sensible code defaults per provider name | Keeps config minimal while still giving the LLM useful context. Defaults like `"DuckDuckGo web search"` for `ddg` are better than raw names. |
| Permission model | `permissions.Request()` like fetch tool | Web searches send queries to external services — user should approve. Granular permission via `permission.rules.websearch`. |
| Response formatting | Render results as numbered markdown list | Compact, readable, fits context window well. |
| Tool category | `viewerToolNames` (read-only) | Search is read-only. Available to all viewer agents (coder, hivemind, explorer). |
| Request params | Minimal: `query` (required), `provider` (required), `max_results` (optional) | Start simple with what LiteLLM supports across all providers. Extend later if needed. |
| Result limit default | 10 results, max 20 | Matches LiteLLM/Perplexity defaults. Prevents context bloat. |
| Provider registry | `SearchProviderRegistry` interface in `tools/websearch.go` | Follows `MCPRegistry` pattern. Keeps tool testable and decoupled from config. Simple enough to live alongside the tool. |
| Snippet field normalization | Accept both `snippet` and `content` | Tavily uses `content`, Perplexity uses `snippet`. Normalize to handle both. |
| Year in tool description | Include current year dynamically | Prevents LLM from searching outdated information (learned from reference impl). |

## Architecture

### Component Relationships

```
┌─────────────────────────────────────────────────────────┐
│  .opencode.json                                         │
│  └── webSearch.providers                                │
│       ├── "ddg":   {baseUrl}                            │
│       └── "brave": {baseUrl, apiKey?, description?}     │
└────────────────────┬────────────────────────────────────┘
                     │ parsed by
                     ▼
┌─────────────────────────────────────────────────────────┐
│  config.Config                                          │
│  └── WebSearch WebSearchConfig                          │
│       └── Providers map[string]SearchProvider            │
│            └── BaseURL, APIKey, Description string       │
└────────────────────┬────────────────────────────────────┘
                     │ consumed by
                     ▼
┌─────────────────────────────────────────────────────────┐
│  SearchProviderRegistry (interface)                      │
│  ├── Providers() []ProviderInfo                          │
│  └── GetProvider(name) (*ResolvedProvider, error)        │
│                                                          │
│  API key resolution:                                     │
│    provider.APIKey → env:VAR_NAME → LOCAL_ENDPOINT_API_KEY│
└────────────────────┬────────────────────────────────────┘
                     │ injected into
                     ▼
┌─────────────────────────────────────────────────────────┐
│  websearchTool (implements BaseTool)                     │
│  ├── registry SearchProviderRegistry                     │
│  ├── permissions permission.Service                      │
│  ├── client *http.Client                                │
│  │                                                       │
│  ├── Info() → dynamic description with provider list     │
│  └── Run()                                               │
│       1. Parse params (query, provider, max_results)     │
│       2. Resolve provider from registry                  │
│       3. Request permission                              │
│       4. POST to provider.BaseURL with resolved API key  │
│       5. Parse response, normalize fields                │
│       6. Format as markdown                              │
└─────────────────────────────────────────────────────────┘
```

### Request Flow

```
STEP 1: Tool invocation
────────────────────────
LLM calls websearch tool with:
  {"query": "kubernetes pod restart", "provider": "ddg", "max_results": 5}

STEP 2: Provider resolution
────────────────────────────
Registry looks up "ddg" → returns ResolvedProvider{BaseURL, APIKey}
API key resolved: provider.APIKey > env:VAR expansion > LOCAL_ENDPOINT_API_KEY
If provider not found → return error listing available providers

STEP 3: Permission check
──────────────────────────
permissions.Request() with query as the permission pattern
User sees: "Web Search — Query: kubernetes pod restart" → approve/deny

STEP 4: HTTP request
──────────────────────
POST {baseUrl}
Authorization: Bearer {resolvedApiKey}
Content-Type: application/json
{"query": "kubernetes pod restart", "max_results": 5}

STEP 5: Response parsing
─────────────────────────
Parse JSON response → normalize snippet/content field
Truncate if exceeds MaxToolResponseTokens

STEP 6: Format output
───────────────────────
Return numbered markdown list with title, URL, snippet for each result
```

### Config Structure

```go
// internal/config/config.go

type WebSearchConfig struct {
    Providers map[string]SearchProvider `json:"providers"`
}

type SearchProvider struct {
    BaseURL     string `json:"baseUrl"`               // Full URL to POST search queries to (required)
    APIKey      string `json:"apiKey,omitempty"`       // Per-provider API key or "env:VAR_NAME"; falls back to LOCAL_ENDPOINT_API_KEY
    Description string `json:"description,omitempty"`  // Human-readable description shown to LLM; defaults generated in code
}
```

### Default Provider Descriptions

When `Description` is empty, the registry generates a default based on the provider name:

```go
var defaultProviderDescriptions = map[string]string{
    "ddg":        "DuckDuckGo web search",
    "brave":      "Brave search — privacy-focused with independent index",
    "tavily":     "Tavily search — optimized for LLM agents",
    "perplexity": "Perplexity AI search",
    "exa":        "Exa AI search — neural search engine",
    "google_pse": "Google Programmable Search Engine",
    "searxng":    "SearXNG — self-hosted metasearch engine",
}

// Fallback for unknown names:
// "my-custom-search" → "my-custom-search web search"
```

### API Key Resolution

```go
// ResolveAPIKey returns the API key for a provider, applying fallback chain:
//   1. provider.APIKey (literal or env:VAR_NAME expanded)
//   2. os.Getenv("LOCAL_ENDPOINT_API_KEY")
//   3. "" (empty — request sent without Authorization header)
func (r *searchProviderRegistry) ResolveAPIKey(provider config.SearchProvider) string {
    if provider.APIKey != "" {
        if strings.HasPrefix(provider.APIKey, "env:") {
            envVar := strings.TrimPrefix(provider.APIKey, "env:")
            if val := os.Getenv(envVar); val != "" {
                return val
            }
            return ""
        }
        return provider.APIKey
    }
    if val := os.Getenv("LOCAL_ENDPOINT_API_KEY"); val != "" {
        return val
    }
    return ""
}
```

### Tool Parameters

```go
// internal/llm/tools/websearch.go

type WebSearchParams struct {
    Query      string `json:"query"`       // required — the search query
    Provider   string `json:"provider"`    // required — which provider to use (from available list)
    MaxResults int    `json:"max_results"` // optional — default 10, max 20
}
```

### Tool Description (Dynamic)

The tool description is built dynamically based on configured providers, following the skill tool pattern, inject current year and providers into description, e.g.

```
Search the web for current information using configured search providers.
Returns relevant web pages with titles, URLs, and content snippets.

Use this tool when you need:
- Current information beyond your knowledge cutoff
- Documentation, API references, or technical articles
- Recent news, releases, or announcements
- Verification of facts or current state of projects

The current year is 2026. When searching for recent information,
include the year in your query to get up-to-date results.

Available search providers (use the provider parameter to select one):
<available_providers>
  <provider>
    <name>ddg</name>
    <description>DuckDuckGo web search</description>
  </provider>
  <provider>
    <name>brave</name>
    <description>Brave search — privacy-focused with independent index</description>
  </provider>
</available_providers>
```

### SearchProviderRegistry Interface

```go
// internal/llm/tools/websearch.go

type SearchProviderRegistry interface {
    // Providers returns metadata for all configured providers (name + description).
    Providers() []SearchProviderInfo
    // GetProvider returns the resolved provider config (with API key resolved).
    // Returns error if provider name is not found.
    GetProvider(name string) (*ResolvedProvider, error)
}

type SearchProviderInfo struct {
    Name        string // config map key (e.g., "ddg")
    Description string // from config or default
}

type ResolvedProvider struct {
    BaseURL string
    APIKey  string // fully resolved (env vars expanded, fallback applied)
}
```

A concrete `searchProviderRegistry` struct reads from `config.Config.WebSearch.Providers`, applies default descriptions, and resolves API keys on `GetProvider()` calls. Constructed in `app.go` or `factory.go` and passed through to `NewToolSet()`.

## Implementation Plan

### Phase 1: Config and Registry

- [x] **1.1** Add `WebSearchConfig` and `SearchProvider` structs to `internal/config/config.go`
- [x] **1.2** Add `WebSearch WebSearchConfig` field to `Config` struct
- [x] **1.3** Implement API key resolution with fallback chain: per-provider `apiKey` (with `env:VAR_NAME` expansion) → `LOCAL_ENDPOINT_API_KEY` env var → empty string
- [x] **1.4** Update schema generator `cmd/schema/main.go` to include `webSearch` section:
  - `providers`: object with additionalProperties, each having `baseUrl` (required string), `apiKey` (optional string), `description` (optional string)
- [x] **1.5** Regenerate `opencode-schema.json`

### Phase 2: Tool Implementation

- [x] **2.1** Create `internal/llm/tools/websearch.go`:
  - Define `WebSearchToolName = "websearch"` constant
  - Define `SearchProviderRegistry` interface and supporting types (`SearchProviderInfo`, `ResolvedProvider`)
  - Implement concrete `searchProviderRegistry` struct reading from config
  - Implement default provider description map and fallback logic
  - Implement `websearchTool` struct with `registry`, `permissions`, `client` fields
  - Implement `NewWebSearchTool(registry SearchProviderRegistry, permissions permission.Service) BaseTool` constructor
  - Implement `Info()` with dynamic description listing available providers with their descriptions
  - Implement `Run()` with provider resolution, permission check, HTTP POST, response parsing, markdown formatting
- [x] **2.2** Handle both `snippet` and `content` response fields (normalize to one)
- [x] **2.3** Handle optional `date` field in results
- [x] **2.4** Enforce response size via `validateAndTruncate()` (existing helper)
- [x] **2.5** Set timeout (default 30s) and respect context cancellation
- [x] **2.6** Return clear error messages: provider not found (list available), HTTP errors, empty results, missing API key

### Phase 3: Tool Registration and Wiring

- [x] **3.1** Add `tools.WebSearchToolName` to `viewerToolNames` in `internal/llm/agent/tools.go`
- [x] **3.2** Add `case tools.WebSearchToolName:` branch in `createTool()` switch in `tools.go`
- [x] **3.3** Construct `SearchProviderRegistry` from config in the tool creation path (created inline in `createTool()` from `config.Get()`)
- [x] **3.4** Wire through `agentFactory` → `NewAgent()` → `NewToolSet()` → `createTool()` (no factory changes needed — registry created from global config)

### Phase 4: TUI Integration

- [x] **4.1** Update `toolName()` in `internal/tui/components/chat/message.go` — add `case tools.WebSearchToolName: return "Web Search"`
- [x] **4.2** Update `getToolAction()` — add `case tools.WebSearchToolName: return "Searching the web..."`
- [x] **4.3** Update `renderToolParams()` — display query, provider, and max_results
- [x] **4.4** Update `renderToolResponse()` — render search results as markdown
- [x] **4.5** Update `internal/tui/components/dialog/permission.go` — add case for websearch permission display (show query as the permission pattern, similar to how fetch shows URL)

### Phase 5: Permission Integration

- [x] **5.1** The tool uses `permissions.Request()` with a `PermissionRequest` containing `Tool: "websearch"` and the query as pattern — same approach as fetch tool
- [x] **5.2** Support granular permissions in `permission.rules.websearch` (works via existing permission framework)
- [x] **5.3** Support per-agent tool enable/disable: `agents.explorer.tools.websearch = false` (works via existing `IsToolEnabled` check)

### Phase 6: Testing

- [x] **6.1** Unit tests for `websearchTool.Info()` — description generation with various provider configs, default descriptions, custom descriptions
- [x] **6.2** Unit tests for `Run()` — mock HTTP responses, test response parsing, `snippet` vs `content` normalization, error cases
- [x] **6.3** Unit tests for `searchProviderRegistry` — provider lookup, missing provider error with available list, empty config
- [x] **6.4** Unit tests for API key resolution — per-provider literal, `env:VAR_NAME`, `LOCAL_ENDPOINT_API_KEY` fallback, no key at all
- [x] **6.5** Unit tests for default provider descriptions — known names get defaults, unknown names get `"{name} web search"` fallback
- [x] **6.6** Test permission flow — mock permission service, verify request contains query

### Phase 7: Documentation

- [ ] **7.1** Create `docs/web-search.md` with setup guide, provider configuration examples, API key resolution, permission configuration
- [ ] **7.2** Update `AGENTS.md` / `CLAUDE.md` with websearch tool reference
- [ ] **7.3** Update `README.md` if there is a tools section

> **Note**: Phase 7 (Documentation) is left for a follow-up, as it's non-code and can be done independently.

## Edge Cases

### No providers configured

1. `webSearch` section missing or empty in config
2. Tool is still registered but `Info()` description says "No search providers configured. Add providers to webSearch.providers in .opencode.json."
3. `Run()` returns an error: "No search providers available. Configure providers in .opencode.json under webSearch.providers."

### Invalid provider name from LLM

1. LLM calls tool with `{"provider": "google"}` but only `ddg` and `brave` are configured
2. Tool returns error: "Provider 'google' not found. Available providers: ddg, brave"
3. LLM retries with a valid provider

### No API key available

1. Provider has no `apiKey` in config and `LOCAL_ENDPOINT_API_KEY` env var is not set
2. Request is sent without `Authorization` header (some providers may allow unauthenticated access)
3. If provider returns 401, tool surfaces: "Search failed (HTTP 401): Unauthorized. Set an API key for provider 'ddg' in .opencode.json or set the LOCAL_ENDPOINT_API_KEY environment variable."

### API key via env: prefix — variable not set

1. Config has `"apiKey": "env:MY_SEARCH_KEY"` but `MY_SEARCH_KEY` is not set
2. Resolution falls through to `LOCAL_ENDPOINT_API_KEY` env var
3. If that is also not set, request sent without auth (same as "No API key available" case)

### Provider returns non-200 response

1. Provider returns 401 (bad key), 429 (rate limit), 500 (server error)
2. Tool returns error with status code and response body snippet: "Search failed (HTTP 429): Rate limit exceeded. Try again later."

### Provider returns empty results

1. Query returns `{"results": []}` or results array is empty
2. Tool returns: "No results found for query: 'your search query'. Try different search terms."

### Response exceeds token limit

1. Many results with long snippets push response past `MaxToolResponseTokens`
2. `validateAndTruncate()` handles this automatically (existing behavior for all tools)

### Multiple providers share the same LiteLLM instance

1. Two providers configured pointing at the same LiteLLM proxy but different URL paths (e.g., `/v1/search/ddg` and `/v1/search/brave`)
2. Both inherit the same `LOCAL_ENDPOINT_API_KEY` — this is the expected common case
3. No special handling needed

## Open Questions

All initial questions have been resolved during review. Decisions are captured in the Design Decisions table above. Summary of resolutions:

1. **`provider` parameter**: Required. LLM sees all available providers in the tool description and picks explicitly.
2. **Provider descriptions**: Optional `description` field in config. Code provides sensible defaults for known provider names (ddg, brave, tavily, etc.) and a `"{name} web search"` fallback for unknown names.
3. **Additional search parameters**: Start minimal — `query`, `provider`, `max_results` only. Extend in a follow-up if agents need domain filtering, time ranges, etc.
4. **API key fallback**: Per-provider `apiKey` field (with `env:` expansion) → `LOCAL_ENDPOINT_API_KEY` env var → no auth. No config-level global key needed.
5. **Registry location**: `SearchProviderRegistry` interface and implementation live in `internal/llm/tools/websearch.go`. Extract to separate package if complexity grows.

## Success Criteria

- [ ] `websearch` tool appears in viewer tool list for coder, hivemind, and explorer agents
- [ ] Tool description dynamically lists configured providers with descriptions
- [ ] Default descriptions are generated for known provider names when not specified in config
- [ ] LLM can call the tool with a query and provider, and receive formatted search results
- [ ] Permission dialog shows the search query and requires user approval
- [ ] Tool works with LiteLLM proxy endpoints (DuckDuckGo, Brave) using `LOCAL_ENDPOINT_API_KEY`
- [ ] Per-provider `apiKey` with `env:VAR_NAME` expansion works and overrides the fallback
- [ ] Tool handles errors gracefully (no providers, bad keys, HTTP errors, empty results)
- [ ] TUI renders tool invocations with proper name, action, params, and response
- [ ] Schema file is updated and valid
- [ ] Tests cover core tool logic, registry, API key resolution, and error paths
- [ ] Documentation covers setup and configuration

## References

- `internal/llm/tools/fetch.go` — Closest existing tool pattern (HTTP + permissions)
- `internal/llm/tools/sourcegraph.go` — Another search-like viewer tool
- `internal/llm/tools/tools.go` — Tool interface and constants
- `internal/llm/tools/skill.go` — Dynamic description pattern with available items list
- `internal/llm/agent/tools.go` — Tool registration (viewerToolNames, createTool switch)
- `internal/llm/agent/mcp-tool.go` — MCPRegistry pattern for registry injection
- `internal/llm/agent/factory.go` — AgentFactory wiring
- `internal/llm/models/local.go` — `LOCAL_ENDPOINT_API_KEY` usage pattern
- `internal/tui/components/chat/message.go` — TUI tool rendering (toolName, getToolAction, renderToolParams, renderToolResponse)
- `internal/tui/components/dialog/permission.go` — Permission dialog rendering
- `internal/config/config.go` — Config struct and loading
- `cmd/schema/main.go` — JSON schema generator
- LiteLLM Search API docs: https://docs.litellm.ai/docs/search/
- Tavily Search API: https://docs.tavily.com/documentation/api-reference/endpoint/search
- Reference TypeScript impl: https://github.com/anomalyco/opencode/blob/2a2082233d9e8bda4674ce596f04b61b3b32522d/packages/opencode/src/tool/websearch.ts

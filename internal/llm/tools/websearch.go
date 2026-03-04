package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/permission"
)

const (
	WebSearchToolName = "websearch"

	defaultMaxResults = 10
	maxMaxResults     = 20
	searchTimeout     = 30 * time.Second
)

type WebSearchParams struct {
	Query      string `json:"query"`
	Provider   string `json:"provider"`
	MaxResults int    `json:"max_results"`
}

type WebSearchPermissionsParams struct {
	Query    string `json:"query"`
	Provider string `json:"provider"`
}

type SearchProviderInfo struct {
	Name        string
	Description string
}

type ResolvedProvider struct {
	BaseURL string
	APIKey  string
}

type SearchProviderRegistry interface {
	Providers() []SearchProviderInfo
	GetProvider(name string) (*ResolvedProvider, error)
}

type searchProviderRegistry struct {
	providers map[string]config.WebSearchProvider
}

var defaultProviderDescriptions = map[string]string{
	"ddg":        "DuckDuckGo web search",
	"brave":      "Brave search — privacy-focused with independent index",
	"tavily":     "Tavily search — optimized for LLM agents",
	"perplexity": "Perplexity AI search",
	"exa":        "Exa AI search — neural search engine",
	"google_pse": "Google Programmable Search Engine",
	"searxng":    "SearXNG — self-hosted metasearch engine",
}

func NewSearchProviderRegistry(cfg *config.Config) SearchProviderRegistry {
	providers := make(map[string]config.WebSearchProvider)
	if cfg != nil && cfg.WebSearch != nil && cfg.WebSearch.Providers != nil {
		for k, v := range cfg.WebSearch.Providers {
			providers[k] = v
		}
	}
	return &searchProviderRegistry{providers: providers}
}

func (r *searchProviderRegistry) Providers() []SearchProviderInfo {
	result := make([]SearchProviderInfo, 0, len(r.providers))
	for name, p := range r.providers {
		desc := p.Description
		if desc == "" {
			desc = defaultDescription(name)
		}
		result = append(result, SearchProviderInfo{Name: name, Description: desc})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (r *searchProviderRegistry) GetProvider(name string) (*ResolvedProvider, error) {
	p, ok := r.providers[name]
	if !ok {
		available := make([]string, 0, len(r.providers))
		for k := range r.providers {
			available = append(available, k)
		}
		sort.Strings(available)
		return nil, fmt.Errorf("provider %q not found. Available providers: %s", name, strings.Join(available, ", "))
	}
	return &ResolvedProvider{
		BaseURL: p.BaseURL,
		APIKey:  resolveAPIKey(p),
	}, nil
}

func resolveAPIKey(p config.WebSearchProvider) string {
	if p.APIKey != "" {
		if strings.HasPrefix(p.APIKey, "env:") {
			envVar := strings.TrimPrefix(p.APIKey, "env:")
			if val := os.Getenv(envVar); val != "" {
				return val
			}
			// env var not set — fall through to LOCAL_ENDPOINT_API_KEY fallback
		} else {
			return p.APIKey
		}
	}
	if val := os.Getenv("LOCAL_ENDPOINT_API_KEY"); val != "" {
		return val
	}
	return ""
}

func defaultDescription(name string) string {
	if desc, ok := defaultProviderDescriptions[name]; ok {
		return desc
	}
	return name + " web search"
}

type websearchTool struct {
	registry    SearchProviderRegistry
	permissions permission.Service
	client      *http.Client
}

func NewWebSearchTool(registry SearchProviderRegistry, permissions permission.Service) BaseTool {
	return &websearchTool{
		registry:    registry,
		permissions: permissions,
		client:      &http.Client{Timeout: searchTimeout},
	}
}

func (t *websearchTool) Info() ToolInfo {
	return ToolInfo{
		Name:        WebSearchToolName,
		Description: t.buildDescription(),
		Parameters: map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The search query",
			},
			"provider": map[string]any{
				"type":        "string",
				"description": "Which search provider to use (see available providers in tool description)",
			},
			"max_results": map[string]any{
				"type":        "number",
				"description": fmt.Sprintf("Maximum number of results to return (default %d, max %d)", defaultMaxResults, maxMaxResults),
			},
		},
		Required: []string{"query", "provider"},
	}
}

func (t *websearchTool) buildDescription() string {
	year := time.Now().Year()
	providers := t.registry.Providers()

	var sb strings.Builder
	sb.WriteString(`Search the web for current information using configured search providers.
Returns relevant web pages with titles, URLs, and content snippets.

Use this tool when you need:
- Current information beyond your knowledge cutoff
- Documentation, API references, or technical articles
- Recent news, releases, or announcements
- Verification of facts or current state of projects

`)
	sb.WriteString(fmt.Sprintf("The current year is %d. When searching for recent information,\ninclude the year in your query to get up-to-date results.\n", year))

	if len(providers) == 0 {
		sb.WriteString("\nNo search providers configured. Add providers to webSearch.providers in .opencode.json.")
	} else {
		sb.WriteString("\nAvailable search providers (use the provider parameter to select one):\n<available_providers>\n")
		for _, p := range providers {
			sb.WriteString(fmt.Sprintf("  <provider>\n    <name>%s</name>\n    <description>%s</description>\n  </provider>\n", p.Name, p.Description))
		}
		sb.WriteString("</available_providers>")
	}

	return sb.String()
}

type searchResponse struct {
	Results []searchResult `json:"results"`
}

type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Content string `json:"content"`
	Date    string `json:"date"`
}

func (t *websearchTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params WebSearchParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse("Failed to parse parameters: " + err.Error()), nil
	}

	if params.Query == "" {
		return NewTextErrorResponse("Query parameter is required"), nil
	}
	if params.Provider == "" {
		return NewTextErrorResponse("Provider parameter is required"), nil
	}

	providers := t.registry.Providers()
	if len(providers) == 0 {
		return NewTextErrorResponse("No search providers available. Configure providers in .opencode.json under webSearch.providers."), nil
	}

	provider, err := t.registry.GetProvider(params.Provider)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	maxResults := params.MaxResults
	if maxResults <= 0 {
		maxResults = defaultMaxResults
	}
	if maxResults > maxMaxResults {
		maxResults = maxMaxResults
	}

	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return NewEmptyResponse(), fmt.Errorf("session ID and message ID are required")
	}

	p := t.permissions.Request(
		permission.CreatePermissionRequest{
			SessionID:   sessionID,
			Path:        config.WorkingDirectory(),
			ToolName:    WebSearchToolName,
			Action:      "websearch",
			Description: fmt.Sprintf("Web search query: %s", params.Query),
			Params:      WebSearchPermissionsParams{Query: params.Query, Provider: params.Provider},
		},
	)
	if !p {
		return NewEmptyResponse(), permission.ErrorPermissionDenied
	}

	reqBody, _ := json.Marshal(map[string]any{
		"query":       params.Query,
		"max_results": maxResults,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", provider.BaseURL, bytes.NewReader(reqBody))
	if err != nil {
		return NewTextErrorResponse("Failed to create request: " + err.Error()), nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "opencode/1.0")
	if provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return NewTextErrorResponse("Search request failed: " + err.Error()), nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return NewTextErrorResponse("Failed to read response: " + err.Error()), nil
	}

	if resp.StatusCode != http.StatusOK {
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		msg := fmt.Sprintf("Search failed (HTTP %d): %s", resp.StatusCode, snippet)
		if resp.StatusCode == http.StatusUnauthorized {
			msg += fmt.Sprintf("\nSet an API key for provider %q in .opencode.json or set the LOCAL_ENDPOINT_API_KEY environment variable.", params.Provider)
		}
		return NewTextErrorResponse(msg), nil
	}

	var searchResp searchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return NewTextErrorResponse("Failed to parse search response: " + err.Error()), nil
	}

	if len(searchResp.Results) == 0 {
		return NewTextResponse(fmt.Sprintf("No results found for query: %q. Try different search terms.", params.Query)), nil
	}

	return NewTextResponse(formatResults(searchResp.Results)), nil
}

func formatResults(results []searchResult) string {
	var sb strings.Builder
	sb.WriteString("## Search Results\n\n")
	for i, r := range results {
		snippet := r.Snippet
		if snippet == "" {
			snippet = r.Content
		}
		sb.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, r.Title))
		sb.WriteString(fmt.Sprintf("   %s\n", r.URL))
		if snippet != "" {
			sb.WriteString(fmt.Sprintf("   %s\n", snippet))
		}
		if r.Date != "" {
			sb.WriteString(fmt.Sprintf("   _Date: %s_\n", r.Date))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

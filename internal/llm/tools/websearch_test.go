package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/permission"
	permMocks "github.com/opencode-ai/opencode/internal/permission/mocks"
	"go.uber.org/mock/gomock"
)

func TestSearchProviderRegistry_Providers(t *testing.T) {
	tests := []struct {
		name      string
		providers map[string]config.WebSearchProvider
		want      int
	}{
		{
			name:      "empty config",
			providers: nil,
			want:      0,
		},
		{
			name: "single provider",
			providers: map[string]config.WebSearchProvider{
				"ddg": {BaseURL: "http://example.com"},
			},
			want: 1,
		},
		{
			name: "multiple providers",
			providers: map[string]config.WebSearchProvider{
				"ddg":   {BaseURL: "http://example.com/ddg"},
				"brave": {BaseURL: "http://example.com/brave"},
			},
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			if tt.providers != nil {
				cfg.WebSearch = &config.WebSearchConfig{Providers: tt.providers}
			}
			reg := NewSearchProviderRegistry(cfg)
			got := reg.Providers()
			if len(got) != tt.want {
				t.Errorf("Providers() returned %d providers, want %d", len(got), tt.want)
			}
		})
	}
}

func TestSearchProviderRegistry_GetProvider(t *testing.T) {
	cfg := &config.Config{
		WebSearch: &config.WebSearchConfig{
			Providers: map[string]config.WebSearchProvider{
				"ddg": {BaseURL: "http://example.com/ddg", APIKey: "test-key"},
			},
		},
	}
	reg := NewSearchProviderRegistry(cfg)

	t.Run("found", func(t *testing.T) {
		p, err := reg.GetProvider("ddg")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.BaseURL != "http://example.com/ddg" {
			t.Errorf("BaseURL = %q, want %q", p.BaseURL, "http://example.com/ddg")
		}
		if p.APIKey != "test-key" {
			t.Errorf("APIKey = %q, want %q", p.APIKey, "test-key")
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := reg.GetProvider("google")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestSearchProviderRegistry_DefaultDescriptions(t *testing.T) {
	cfg := &config.Config{
		WebSearch: &config.WebSearchConfig{
			Providers: map[string]config.WebSearchProvider{
				"ddg":    {BaseURL: "http://example.com/ddg"},
				"brave":  {BaseURL: "http://example.com/brave"},
				"custom": {BaseURL: "http://example.com/custom"},
			},
		},
	}
	reg := NewSearchProviderRegistry(cfg)
	providers := reg.Providers()

	descMap := make(map[string]string)
	for _, p := range providers {
		descMap[p.Name] = p.Description
	}

	if descMap["ddg"] != "DuckDuckGo web search" {
		t.Errorf("ddg description = %q, want %q", descMap["ddg"], "DuckDuckGo web search")
	}
	if descMap["brave"] != "Brave search — privacy-focused with independent index" {
		t.Errorf("brave description = %q", descMap["brave"])
	}
	if descMap["custom"] != "custom web search" {
		t.Errorf("custom description = %q, want %q", descMap["custom"], "custom web search")
	}
}

func TestSearchProviderRegistry_CustomDescription(t *testing.T) {
	cfg := &config.Config{
		WebSearch: &config.WebSearchConfig{
			Providers: map[string]config.WebSearchProvider{
				"ddg": {BaseURL: "http://example.com", Description: "My custom DDG"},
			},
		},
	}
	reg := NewSearchProviderRegistry(cfg)
	providers := reg.Providers()

	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}
	if providers[0].Description != "My custom DDG" {
		t.Errorf("description = %q, want %q", providers[0].Description, "My custom DDG")
	}
}

func TestResolveAPIKey(t *testing.T) {
	tests := []struct {
		name     string
		provider config.WebSearchProvider
		envVars  map[string]string
		want     string
	}{
		{
			name:     "literal key",
			provider: config.WebSearchProvider{APIKey: "my-key"},
			want:     "my-key",
		},
		{
			name:     "env: prefix with set var",
			provider: config.WebSearchProvider{APIKey: "env:TEST_WEBSEARCH_KEY"},
			envVars:  map[string]string{"TEST_WEBSEARCH_KEY": "env-key-value"},
			want:     "env-key-value",
		},
		{
			name:     "no key falls back to LOCAL_ENDPOINT_API_KEY",
			provider: config.WebSearchProvider{},
			envVars:  map[string]string{"LOCAL_ENDPOINT_API_KEY": "fallback-key"},
			want:     "fallback-key",
		},
		{
			name:     "env: prefix with unset var falls back to LOCAL_ENDPOINT_API_KEY",
			provider: config.WebSearchProvider{APIKey: "env:UNSET_WEBSEARCH_KEY"},
			envVars:  map[string]string{"LOCAL_ENDPOINT_API_KEY": "fallback-key"},
			want:     "fallback-key",
		},
		{
			name:     "no key and no env returns empty",
			provider: config.WebSearchProvider{},
			envVars:  map[string]string{"LOCAL_ENDPOINT_API_KEY": ""},
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}
			got := resolveAPIKey(tt.provider)
			if got != tt.want {
				t.Errorf("resolveAPIKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWebSearchTool_Info(t *testing.T) {
	t.Run("with providers", func(t *testing.T) {
		cfg := &config.Config{
			WebSearch: &config.WebSearchConfig{
				Providers: map[string]config.WebSearchProvider{
					"ddg": {BaseURL: "http://example.com"},
				},
			},
		}
		reg := NewSearchProviderRegistry(cfg)
		tool := NewWebSearchTool(reg, nil)
		info := tool.Info()

		if info.Name != WebSearchToolName {
			t.Errorf("Name = %q, want %q", info.Name, WebSearchToolName)
		}
		if len(info.Required) != 2 {
			t.Errorf("Required = %v, want [query, provider]", info.Required)
		}
	})

	t.Run("no providers", func(t *testing.T) {
		cfg := &config.Config{}
		reg := NewSearchProviderRegistry(cfg)
		tool := NewWebSearchTool(reg, nil)
		info := tool.Info()

		if info.Name != WebSearchToolName {
			t.Errorf("Name = %q, want %q", info.Name, WebSearchToolName)
		}
	})
}

func newTestCtx() context.Context {
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = context.WithValue(ctx, MessageIDContextKey, "test-message")
	return ctx
}

func TestWebSearchTool_Run(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Bearer test-key, got %s", r.Header.Get("Authorization"))
		}

		resp := searchResponse{
			Results: []searchResult{
				{Title: "Result 1", URL: "http://example.com/1", Snippet: "Snippet 1"},
				{Title: "Result 2", URL: "http://example.com/2", Content: "Content 2", Date: "2026-01-15"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	ctrl := gomock.NewController(t)
	mockPerms := permMocks.NewMockService(ctrl)
	mockPerms.EXPECT().Request(gomock.Any()).Return(true)

	cfg := &config.Config{
		WebSearch: &config.WebSearchConfig{
			Providers: map[string]config.WebSearchProvider{
				"test": {BaseURL: server.URL, APIKey: "test-key"},
			},
		},
	}
	reg := NewSearchProviderRegistry(cfg)
	tool := NewWebSearchTool(reg, mockPerms)

	input, _ := json.Marshal(WebSearchParams{Query: "test query", Provider: "test"})
	resp, err := tool.Run(newTestCtx(), ToolCall{ID: "1", Name: WebSearchToolName, Input: string(input)})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("unexpected error response: %s", resp.Content)
	}
	if resp.Content == "" {
		t.Fatal("expected non-empty response")
	}
}

func TestWebSearchTool_Run_NoProviders(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockPerms := permMocks.NewMockService(ctrl)

	cfg := &config.Config{}
	reg := NewSearchProviderRegistry(cfg)
	tool := NewWebSearchTool(reg, mockPerms)

	input, _ := json.Marshal(WebSearchParams{Query: "test", Provider: "ddg"})
	resp, err := tool.Run(newTestCtx(), ToolCall{ID: "1", Name: WebSearchToolName, Input: string(input)})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("expected error response")
	}
}

func TestWebSearchTool_Run_InvalidProvider(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockPerms := permMocks.NewMockService(ctrl)

	cfg := &config.Config{
		WebSearch: &config.WebSearchConfig{
			Providers: map[string]config.WebSearchProvider{
				"ddg": {BaseURL: "http://example.com"},
			},
		},
	}
	reg := NewSearchProviderRegistry(cfg)
	tool := NewWebSearchTool(reg, mockPerms)

	input, _ := json.Marshal(WebSearchParams{Query: "test", Provider: "nonexistent"})
	resp, err := tool.Run(newTestCtx(), ToolCall{ID: "1", Name: WebSearchToolName, Input: string(input)})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("expected error response")
	}
}

func TestWebSearchTool_Run_PermissionDenied(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockPerms := permMocks.NewMockService(ctrl)
	mockPerms.EXPECT().Request(gomock.Any()).Return(false)

	cfg := &config.Config{
		WebSearch: &config.WebSearchConfig{
			Providers: map[string]config.WebSearchProvider{
				"ddg": {BaseURL: "http://example.com"},
			},
		},
	}
	reg := NewSearchProviderRegistry(cfg)
	tool := NewWebSearchTool(reg, mockPerms)

	input, _ := json.Marshal(WebSearchParams{Query: "test", Provider: "ddg"})
	_, err := tool.Run(newTestCtx(), ToolCall{ID: "1", Name: WebSearchToolName, Input: string(input)})

	if err != permission.ErrorPermissionDenied {
		t.Fatalf("expected permission denied error, got: %v", err)
	}
}

func TestWebSearchTool_Run_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "Unauthorized")
	}))
	defer server.Close()

	ctrl := gomock.NewController(t)
	mockPerms := permMocks.NewMockService(ctrl)
	mockPerms.EXPECT().Request(gomock.Any()).Return(true)

	cfg := &config.Config{
		WebSearch: &config.WebSearchConfig{
			Providers: map[string]config.WebSearchProvider{
				"test": {BaseURL: server.URL},
			},
		},
	}
	reg := NewSearchProviderRegistry(cfg)
	tool := NewWebSearchTool(reg, mockPerms)

	input, _ := json.Marshal(WebSearchParams{Query: "test", Provider: "test"})
	resp, err := tool.Run(newTestCtx(), ToolCall{ID: "1", Name: WebSearchToolName, Input: string(input)})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("expected error response")
	}
}

func TestWebSearchTool_Run_EmptyResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(searchResponse{Results: []searchResult{}})
	}))
	defer server.Close()

	ctrl := gomock.NewController(t)
	mockPerms := permMocks.NewMockService(ctrl)
	mockPerms.EXPECT().Request(gomock.Any()).Return(true)

	cfg := &config.Config{
		WebSearch: &config.WebSearchConfig{
			Providers: map[string]config.WebSearchProvider{
				"test": {BaseURL: server.URL},
			},
		},
	}
	reg := NewSearchProviderRegistry(cfg)
	tool := NewWebSearchTool(reg, mockPerms)

	input, _ := json.Marshal(WebSearchParams{Query: "obscure query", Provider: "test"})
	resp, err := tool.Run(newTestCtx(), ToolCall{ID: "1", Name: WebSearchToolName, Input: string(input)})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Fatal("should not be error response for empty results")
	}
}

func TestFormatResults(t *testing.T) {
	results := []searchResult{
		{Title: "Test", URL: "http://example.com", Snippet: "A snippet"},
		{Title: "Test 2", URL: "http://example.com/2", Content: "Content field", Date: "2026-01-01"},
	}

	output := formatResults(results)
	if output == "" {
		t.Fatal("expected non-empty output")
	}
}

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	mock_agent "github.com/opencode-ai/opencode/internal/agent/mocks"
	"github.com/opencode-ai/opencode/internal/permission"
	mock_permission "github.com/opencode-ai/opencode/internal/permission/mocks"
	"go.uber.org/mock/gomock"
)

func TestIsBinaryContent(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        []byte
		want        bool
	}{
		{
			name:        "JAR file by content type",
			contentType: "application/java-archive",
			body:        []byte("PK\x03\x04"),
			want:        true,
		},
		{
			name:        "octet-stream",
			contentType: "application/octet-stream",
			body:        []byte{0x00, 0x01, 0x02},
			want:        true,
		},
		{
			name:        "zip file",
			contentType: "application/zip",
			body:        []byte("PK\x03\x04"),
			want:        true,
		},
		{
			name:        "PDF file",
			contentType: "application/pdf",
			body:        []byte("%PDF-1.4"),
			want:        true,
		},
		{
			name:        "image PNG",
			contentType: "image/png",
			body:        []byte{0x89, 0x50, 0x4E, 0x47},
			want:        true,
		},
		{
			name:        "audio mpeg",
			contentType: "audio/mpeg",
			body:        []byte{0xFF, 0xFB},
			want:        true,
		},
		{
			name:        "video mp4",
			contentType: "video/mp4",
			body:        []byte{0x00, 0x00},
			want:        true,
		},
		{
			name:        "font woff2",
			contentType: "font/woff2",
			body:        []byte{0x77, 0x4F, 0x46, 0x32},
			want:        true,
		},
		{
			name:        "content type with charset",
			contentType: "application/pdf; charset=binary",
			body:        []byte("%PDF"),
			want:        true,
		},
		{
			name:        "plain text",
			contentType: "text/plain",
			body:        []byte("Hello, world!"),
			want:        false,
		},
		{
			name:        "HTML",
			contentType: "text/html; charset=utf-8",
			body:        []byte("<html><body>test</body></html>"),
			want:        false,
		},
		{
			name:        "JSON",
			contentType: "application/json",
			body:        []byte(`{"key": "value"}`),
			want:        false,
		},
		{
			name:        "unknown content type but valid UTF-8 body",
			contentType: "",
			body:        []byte("This is valid UTF-8 text"),
			want:        false,
		},
		{
			name:        "unknown content type with invalid UTF-8 body",
			contentType: "",
			body:        []byte{0x80, 0x81, 0x82, 0xFF, 0xFE, 0x00, 0x01},
			want:        true,
		},
		{
			name:        "text content type but binary body",
			contentType: "text/plain",
			body:        []byte{0x80, 0x81, 0x82, 0xFF, 0xFE},
			want:        true,
		},
		{
			name:        "empty body with text content type",
			contentType: "text/plain",
			body:        []byte{},
			want:        false,
		},
		{
			name:        "empty body with no content type",
			contentType: "",
			body:        []byte{},
			want:        false,
		},
		{
			name:        "wasm binary",
			contentType: "application/wasm",
			body:        []byte{0x00, 0x61, 0x73, 0x6D},
			want:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isBinaryContent(tt.contentType, tt.body)
			if got != tt.want {
				t.Errorf("isBinaryContent(%q, body) = %v, want %v", tt.contentType, got, tt.want)
			}
		})
	}
}

func TestIsWAFChallenge(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		headers    map[string]string
		want       bool
	}{
		{
			name:       "403 with cf-mitigated challenge",
			statusCode: http.StatusForbidden,
			headers:    map[string]string{"Cf-Mitigated": "challenge"},
			want:       true,
		},
		{
			name:       "403 without cf-mitigated header",
			statusCode: http.StatusForbidden,
			headers:    map[string]string{},
			want:       false,
		},
		{
			name:       "200 with cf-mitigated header",
			statusCode: http.StatusOK,
			headers:    map[string]string{"Cf-Mitigated": "challenge"},
			want:       false,
		},
		{
			name:       "403 with cf-mitigated non-challenge value",
			statusCode: http.StatusForbidden,
			headers:    map[string]string{"Cf-Mitigated": "captcha"},
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tt.statusCode,
				Header:     http.Header{},
			}
			for k, v := range tt.headers {
				resp.Header.Set(k, v)
			}
			got := isWAFChallenge(resp)
			if got != tt.want {
				t.Errorf("isWAFChallenge() = %v, want %v", got, tt.want)
			}
		})
	}
}

// newTestFetchTool creates a fetchTool with mocked dependencies that allow all permissions.
func newTestFetchTool(t *testing.T) *fetchTool {
	t.Helper()
	ctrl := gomock.NewController(t)
	mockRegistry := mock_agent.NewMockRegistry(ctrl)
	mockPerms := mock_permission.NewMockService(ctrl)

	mockRegistry.EXPECT().
		EvaluatePermission(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(permission.ActionAllow).
		AnyTimes()

	return &fetchTool{
		agentRegistry: mockRegistry,
		client:        http.DefaultClient,
		permissions:   mockPerms,
	}
}

// fetchToolCtx returns a context with required session/message IDs.
func fetchToolCtx() context.Context {
	ctx := context.Background()
	ctx = context.WithValue(ctx, SessionIDContextKey, "test-session")
	ctx = context.WithValue(ctx, MessageIDContextKey, "test-message")
	return ctx
}

func TestFetchRetryOnCloudflareChallenge(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestCount.Add(1)
		if n == 1 {
			// First request: return Cloudflare challenge
			w.Header().Set("Cf-Mitigated", "challenge")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "Just a moment...")
			return
		}
		// Second request: verify browser UA and Accept-Language, return success
		if ua := r.Header.Get("User-Agent"); ua != browserUserAgent {
			t.Errorf("retry User-Agent = %q, want %q", ua, browserUserAgent)
		}
		if al := r.Header.Get("Accept-Language"); al != "en-US,en;q=0.9" {
			t.Errorf("retry Accept-Language = %q, want %q", al, "en-US,en;q=0.9")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "<html><body><p>Hello from docs</p></body></html>")
	}))
	defer server.Close()

	tool := newTestFetchTool(t)
	input, _ := json.Marshal(FetchParams{URL: server.URL, Format: "text"})
	resp, err := tool.Run(fetchToolCtx(), ToolCall{ID: "1", Name: WebFetchToolName, Input: string(input)})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("expected success, got error: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "Hello from docs") {
		t.Errorf("response content = %q, want to contain %q", resp.Content, "Hello from docs")
	}
	if got := requestCount.Load(); got != 2 {
		t.Errorf("server received %d requests, want 2", got)
	}
}

func TestFetchDoubleCloudflareFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cf-Mitigated", "challenge")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "Just a moment...")
	}))
	defer server.Close()

	tool := newTestFetchTool(t)
	input, _ := json.Marshal(FetchParams{URL: server.URL, Format: "text"})
	resp, err := tool.Run(fetchToolCtx(), ToolCall{ID: "1", Name: WebFetchToolName, Input: string(input)})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("expected error response, got success")
	}
	if !strings.Contains(resp.Content, "Cloudflare") {
		t.Errorf("error message should mention Cloudflare, got: %s", resp.Content)
	}
	if !strings.Contains(resp.Content, "cf-mitigated") {
		t.Errorf("error message should mention cf-mitigated, got: %s", resp.Content)
	}
}

func TestFetchNonCloudflare403(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "Forbidden")
	}))
	defer server.Close()

	tool := newTestFetchTool(t)
	input, _ := json.Marshal(FetchParams{URL: server.URL, Format: "text"})
	resp, err := tool.Run(fetchToolCtx(), ToolCall{ID: "1", Name: WebFetchToolName, Input: string(input)})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("expected error response, got success")
	}
	if !strings.Contains(resp.Content, "403") {
		t.Errorf("error message should contain status code 403, got: %s", resp.Content)
	}
	if strings.Contains(resp.Content, "Cloudflare") {
		t.Errorf("error message should NOT mention Cloudflare for non-WAF 403, got: %s", resp.Content)
	}
	if got := requestCount.Load(); got != 1 {
		t.Errorf("server received %d requests, want 1 (no retry)", got)
	}
}

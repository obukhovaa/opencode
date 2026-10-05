package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"

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
		agentRegistry:  mockRegistry,
		client:         http.DefaultClient,
		permissions:    mockPerms,
		maxOutputBytes: webFetchMaxOutputBytes,
	}
}

// newTestFetchToolWithCap is newTestFetchTool with an explicit output cap
// (a negative value means unbounded), for the size-cap tests.
func newTestFetchToolWithCap(t *testing.T, maxOutputBytes int) *fetchTool {
	t.Helper()
	tool := newTestFetchTool(t)
	tool.maxOutputBytes = maxOutputBytes
	return tool
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

func TestResolveWebFetchMaxOutputBytes(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want int
	}{
		{"nil config", nil, webFetchMaxOutputBytes},
		{"no webFetch block", &config.Config{}, webFetchMaxOutputBytes},
		{"zero means default", &config.Config{WebFetch: &config.WebFetchConfig{MaxOutputBytes: 0}}, webFetchMaxOutputBytes},
		{"positive override", &config.Config{WebFetch: &config.WebFetchConfig{MaxOutputBytes: 4096}}, 4096},
		{"negative disables the cap", &config.Config{WebFetch: &config.WebFetchConfig{MaxOutputBytes: -1}}, -1},
		{"any negative normalizes to -1", &config.Config{WebFetch: &config.WebFetchConfig{MaxOutputBytes: -9000}}, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveWebFetchMaxOutputBytes(tt.cfg); got != tt.want {
				t.Errorf("resolveWebFetchMaxOutputBytes() = %d, want %d", got, tt.want)
			}
		})
	}
}

// bigHTMLPage builds an HTML document whose markdown conversion comfortably
// exceeds any small test cap, with a unique needle buried in the middle so a
// test can prove the needle is recoverable from the spill file even when it
// is absent from the preview.
func bigHTMLPage(needle string) string {
	var sb strings.Builder
	sb.WriteString("<html><body><h1>Big Page</h1>")
	for i := range 400 {
		if i == 200 {
			fmt.Fprintf(&sb, "<p>%s</p>", needle)
		}
		fmt.Fprintf(&sb, "<p>paragraph %d with enough filler text to add up over four hundred repetitions</p>", i)
	}
	sb.WriteString("</body></html>")
	return sb.String()
}

func serveHTML(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func runFetch(t *testing.T, tool *fetchTool, url, format string) ToolResponse {
	t.Helper()
	input, _ := json.Marshal(FetchParams{URL: url, Format: format})
	resp, err := tool.Run(fetchToolCtx(), ToolCall{ID: "1", Name: WebFetchToolName, Input: string(input)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError {
		t.Fatalf("expected success, got error: %s", resp.Content)
	}
	return resp
}

// spillPathFrom extracts the temp-file path the overflow header advertises.
func spillPathFrom(t *testing.T, content string) string {
	t.Helper()
	const marker = "Full output saved to: "
	i := strings.Index(content, marker)
	if i < 0 {
		t.Fatalf("no spill-file marker in content: %.300s", content)
	}
	rest := content[i+len(marker):]
	path, _, _ := strings.Cut(rest, "\n")
	path = strings.TrimSpace(path)
	if path == "" {
		t.Fatal("spill-file marker present but path is empty")
	}
	return path
}

func TestFetchUnderCapIsUnchanged(t *testing.T) {
	t.Cleanup(CleanupTempDir)
	server := serveHTML(t, "<html><body><p>Hello from docs</p></body></html>")

	resp := runFetch(t, newTestFetchToolWithCap(t, webFetchMaxOutputBytes), server.URL, "markdown")

	if strings.Contains(resp.Content, "Full output saved to:") {
		t.Errorf("small page was spilled to a file: %q", resp.Content)
	}
	if strings.Contains(resp.Content, "truncated") {
		t.Errorf("small page carries a truncation header: %q", resp.Content)
	}
	if want := "Hello from docs"; !strings.Contains(resp.Content, want) {
		t.Errorf("content = %q, want to contain %q", resp.Content, want)
	}
}

func TestFetchCapsOversizedContent(t *testing.T) {
	t.Cleanup(CleanupTempDir)
	const needle = "NEEDLE-a7f3c9-buried-in-the-middle"
	server := serveHTML(t, bigHTMLPage(needle))

	const capBytes = 2048
	resp := runFetch(t, newTestFetchToolWithCap(t, capBytes), server.URL, "markdown")

	// Header names the size and the file, and points at the right tools.
	for _, want := range []string{"output truncated:", "Full output saved to: ", "grep tool", "sed in bash"} {
		if !strings.Contains(resp.Content, want) {
			t.Errorf("overflow header missing %q; got: %.400s", want, resp.Content)
		}
	}
	if !strings.Contains(resp.Content, "bytes elided") {
		t.Errorf("preview has no elision marker between head and tail: %.400s", resp.Content)
	}

	// The whole point: the reply is small, the full page is on disk.
	path := spillPathFrom(t, resp.Content)
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading spill file: %v", err)
	}
	if len(resp.Content) >= len(saved) {
		t.Errorf("reply (%d bytes) is not smaller than the saved output (%d bytes)", len(resp.Content), len(saved))
	}
	if len(saved) <= capBytes {
		t.Errorf("saved output is %d bytes, expected more than the %d-byte cap", len(saved), capBytes)
	}

	// The needle is recoverable from the file precisely because it did not
	// survive into the preview — the behaviour the change exists for.
	if strings.Contains(resp.Content, needle) {
		t.Fatalf("needle appeared in the preview; the fixture is too small to prove the spill path")
	}
	if !strings.Contains(string(saved), needle) {
		t.Errorf("needle %q missing from the spill file", needle)
	}
}

func TestFetchCapAppliesAfterConversion(t *testing.T) {
	t.Cleanup(CleanupTempDir)
	server := serveHTML(t, bigHTMLPage("needle"))

	resp := runFetch(t, newTestFetchToolWithCap(t, 2048), server.URL, "markdown")
	saved, err := os.ReadFile(spillPathFrom(t, resp.Content))
	if err != nil {
		t.Fatalf("reading spill file: %v", err)
	}

	// The file holds the converted markdown, not the source HTML, so a grep
	// over it sees the same document the preview showed.
	if strings.Contains(string(saved), "<p>") {
		t.Errorf("spill file contains raw HTML tags; the cap was applied before conversion")
	}
	if !strings.Contains(string(saved), "# Big Page") {
		t.Errorf("spill file is not the converted markdown: %.200s", saved)
	}
}

func TestFetchCapAppliesToEveryFormat(t *testing.T) {
	for _, format := range []string{"text", "markdown", "html"} {
		t.Run(format, func(t *testing.T) {
			t.Cleanup(CleanupTempDir)
			server := serveHTML(t, bigHTMLPage("needle"))

			resp := runFetch(t, newTestFetchToolWithCap(t, 2048), server.URL, format)

			if !strings.Contains(resp.Content, "Full output saved to: ") {
				t.Fatalf("format %q was not capped: %.300s", format, resp.Content)
			}
			if _, err := os.Stat(spillPathFrom(t, resp.Content)); err != nil {
				t.Errorf("spill file missing for format %q: %v", format, err)
			}
		})
	}
}

func TestFetchNegativeCapReturnsEverything(t *testing.T) {
	t.Cleanup(CleanupTempDir)
	const needle = "NEEDLE-unbounded-path"
	server := serveHTML(t, bigHTMLPage(needle))

	resp := runFetch(t, newTestFetchToolWithCap(t, -1), server.URL, "markdown")

	if strings.Contains(resp.Content, "Full output saved to:") {
		t.Errorf("content was capped although the cap is disabled: %.300s", resp.Content)
	}
	if !strings.Contains(resp.Content, needle) {
		t.Error("needle missing although the whole page should have been returned")
	}
}

func TestFetchReportsBodyTruncation(t *testing.T) {
	t.Cleanup(CleanupTempDir)
	// No Content-Length: the pre-check above the read cannot catch this, so
	// only the read-one-past-the-limit probe can.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.(http.Flusher).Flush()
		chunk := strings.Repeat("x", 64*1024)
		for written := 0; written < 6*1024*1024; written += len(chunk) {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	resp := runFetch(t, newTestFetchToolWithCap(t, webFetchMaxOutputBytes), server.URL, "text")

	if !strings.Contains(resp.Content, "was truncated before conversion") {
		t.Errorf("oversized body carries no truncation notice: %.400s", resp.Content)
	}
	if !strings.HasPrefix(resp.Content, "<webfetch: response exceeded") {
		t.Errorf("truncation notice is not the first thing the model reads: %.200s", resp.Content)
	}
}

func TestFetchWithinBodyLimitHasNoTruncationNotice(t *testing.T) {
	t.Cleanup(CleanupTempDir)
	server := serveHTML(t, "<html><body><p>small</p></body></html>")

	resp := runFetch(t, newTestFetchToolWithCap(t, webFetchMaxOutputBytes), server.URL, "markdown")

	if strings.Contains(resp.Content, "response exceeded") {
		t.Errorf("in-limit body was flagged as truncated: %q", resp.Content)
	}
}

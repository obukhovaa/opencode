package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/PuerkitoBio/goquery"
	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/permission"
)

type FetchParams struct {
	URL     string `json:"url"`
	Format  string `json:"format"`
	Timeout int    `json:"timeout,omitempty"`
}

type FetchPermissionsParams struct {
	URL     string `json:"url"`
	Format  string `json:"format"`
	Timeout int    `json:"timeout,omitempty"`
}

type fetchTool struct {
	agentRegistry agentregistry.Registry
	client        *http.Client
	permissions   permission.Service
	// maxOutputBytes caps the content this tool keeps in the model context;
	// -1 means unbounded. Resolved once at construction from webFetch
	// .maxOutputBytes — see resolveWebFetchMaxOutputBytes.
	maxOutputBytes int
}

const (
	WebFetchToolName = "webfetch"
	browserUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	// webFetchMaxOutputBytes is the default cap on the content a single
	// webfetch call keeps in the model context. Matches the bash tool's
	// MaxOutputBytes and the MCP default (mcpCallToolMaxOutputBytes) so
	// operators learn one number. Overridable via webFetch.maxOutputBytes.
	webFetchMaxOutputBytes = 50 * 1024 // 50KB
	fetchToolDescription   = `Fetches text-based content (HTML, JSON, plain text, XML) from an HTTP/HTTPS URL and returns it as text, markdown (default), or html.

- Max response size 5MB; no authentication or cookies; binary content (archives, PDFs, images, executables) is rejected — use bash with curl for downloads.
- Large pages are truncated in the reply and saved in full to a temp file named in the output: search that file with grep (or sed in bash) for what you need instead of re-fetching the URL.
- Retries with a browser User-Agent when Cloudflare bot protection is detected.
- If another available tool offers better fetching for the target (e.g. an MCP tool), prefer it.`
	// bodyTruncationNotice is prepended to the returned content when the
	// response body hit the 5MB read limit, so the agent never treats a
	// torn document as a complete one.
	bodyTruncationNotice = "<webfetch: response exceeded the %d-byte limit and was truncated before conversion; the end of the page is missing>\n\n"
)

func NewFetchTool(cfg *config.Config, agents agentregistry.Registry, permissions permission.Service) BaseTool {
	return &fetchTool{
		agentRegistry: agents,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		permissions:    permissions,
		maxOutputBytes: resolveWebFetchMaxOutputBytes(cfg),
	}
}

// resolveWebFetchMaxOutputBytes returns the per-call output-size cap (bytes).
// A positive webFetch.maxOutputBytes overrides the default; a negative value
// disables the cap (-1, "unlimited"); zero or an absent config falls back to
// webFetchMaxOutputBytes. Mirrors resolveCallToolMaxOutputBytes for MCP.
func resolveWebFetchMaxOutputBytes(cfg *config.Config) int {
	if cfg == nil || cfg.WebFetch == nil {
		return webFetchMaxOutputBytes
	}
	switch {
	case cfg.WebFetch.MaxOutputBytes < 0:
		return -1
	case cfg.WebFetch.MaxOutputBytes > 0:
		return cfg.WebFetch.MaxOutputBytes
	}
	return webFetchMaxOutputBytes
}

func (t *fetchTool) Info() ToolInfo {
	return ToolInfo{
		Name:        WebFetchToolName,
		Description: fetchToolDescription,
		Parameters: map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "The URL to fetch content from",
			},
			"format": map[string]any{
				"type":        "string",
				"description": "The format to return the content in (text, markdown, or html)",
				"enum":        []string{"text", "markdown", "html"},
			},
			"timeout": map[string]any{
				"type":        "number",
				"description": "Optional timeout in seconds (max 120)",
			},
		},
		Required: []string{"url"},
	}
}

func (t *fetchTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params FetchParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse("Failed to parse fetch parameters: " + err.Error()), nil
	}

	if params.URL == "" {
		return NewTextErrorResponse("URL parameter is required"), nil
	}

	format := strings.ToLower(params.Format)
	if format == "" {
		format = "markdown"
	}
	if format != "text" && format != "markdown" && format != "html" {
		return NewTextErrorResponse("Format must be one of: text, markdown, html"), nil
	}

	if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
		return NewTextErrorResponse("URL must start with http:// or https://"), nil
	}

	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return NewEmptyResponse(), fmt.Errorf("session ID and message ID are required for creating a new file")
	}

	action := t.agentRegistry.EvaluatePermission(string(GetAgentID(ctx)), WebFetchToolName, params.URL)
	switch action {
	case permission.ActionAllow:
		// Allowed by config, skip interactive permission
	case permission.ActionDeny:
		return NewEmptyResponse(), permission.ErrorPermissionDenied
	default:
		if !t.permissions.Request(ctx,
			permission.CreatePermissionRequest{
				SessionID:   sessionID,
				Path:        config.WorkingDirectory(),
				ToolName:    WebFetchToolName,
				Action:      "webfetch",
				Description: fmt.Sprintf("Fetch content from URL: %s", params.URL),
				Params:      FetchPermissionsParams(params),
			},
		) {
			return NewEmptyResponse(), permission.ErrorPermissionDenied
		}
	}

	client := t.client
	if params.Timeout > 0 {
		maxTimeout := 120 // 2 minutes
		if params.Timeout > maxTimeout {
			params.Timeout = maxTimeout
		}
		client = &http.Client{
			Timeout: time.Duration(params.Timeout) * time.Second,
		}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", params.URL, nil)
	if err != nil {
		return NewEmptyResponse(), fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "opencode/1.0")

	switch format {
	case "text":
		req.Header.Set("Accept", "text/plain;q=1.0, text/html;q=0.5")
	case "markdown":
		req.Header.Set("Accept", "text/markdown;q=1.0, text/html;q=0.7")
	case "html":
		req.Header.Set("Accept", "text/html;q=1.0")
	}

	resp, err := client.Do(req)
	if err != nil {
		return NewEmptyResponse(), fmt.Errorf("failed to fetch URL: %w", err)
	}

	// Handle non-200 responses, including WAF challenge detection and retry.
	if resp.StatusCode != http.StatusOK {
		if isWAFChallenge(resp) {
			// Drain and close the initial response to release the connection.
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()

			retryReq, err := http.NewRequestWithContext(ctx, "GET", params.URL, nil)
			if err != nil {
				return NewEmptyResponse(), fmt.Errorf("failed to create retry request: %w", err)
			}
			retryReq.Header.Set("User-Agent", browserUserAgent)
			retryReq.Header.Set("Accept-Language", "en-US,en;q=0.9")
			retryReq.Header.Set("Accept", req.Header.Get("Accept"))

			resp, err = client.Do(retryReq)
			if err != nil {
				return NewEmptyResponse(), fmt.Errorf("failed to fetch URL: %w", err)
			}
			// If retry also failed, return a WAF-aware error.
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				return NewTextErrorResponse(fmt.Sprintf(
					"Request blocked by Cloudflare bot protection (cf-mitigated: challenge). "+
						"Retry with browser User-Agent also failed (status: %d).",
					resp.StatusCode,
				)), nil
			}
			// Retry succeeded — fall through to body processing below.
		} else {
			resp.Body.Close()
			return NewTextErrorResponse(fmt.Sprintf("Request failed with status code: %d", resp.StatusCode)), nil
		}
	}
	defer resp.Body.Close()

	maxSize := int64(5 * 1024 * 1024) // 5MB
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if size, err := strconv.ParseInt(cl, 10, 64); err == nil && size > maxSize {
			return NewTextErrorResponse(fmt.Sprintf("Response too large: %d bytes (max %d bytes)", size, maxSize)), nil
		}
	}

	// Read one byte past the limit so an oversized body is detectable: a
	// chunked or compressed response carries no Content-Length, so the check
	// above cannot catch it and the read would otherwise stop silently
	// mid-document and be converted as though complete.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return NewTextErrorResponse("Failed to read response body: " + err.Error()), nil
	}
	bodyTruncated := int64(len(body)) > maxSize
	if bodyTruncated {
		// Cut back to the limit, snapped to a rune boundary so the tail of
		// the body is never a split UTF-8 character.
		cut := int(maxSize)
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut]
	}

	if isBinaryContent(resp.Header.Get("Content-Type"), body) {
		return NewTextErrorResponse(fmt.Sprintf(
			"URL returned binary content (Content-Type: %s, %d bytes). "+
				"The webfetch tool only supports text-based content. "+
				"Use the bash tool with curl to download binary files instead.",
			resp.Header.Get("Content-Type"), len(body),
		)), nil
	}

	content := string(body)
	contentType := resp.Header.Get("Content-Type")

	// Each branch formats the body; the cap is applied once below, on the
	// formatted result. Measuring post-conversion is deliberate: it is the
	// converted text that enters the context (a 433KB HTML page becomes
	// 374KB of markdown), and it keeps the spilled file byte-identical to
	// what the preview shows, so grepping the file and reading the preview
	// see the same document.
	var out string
	switch format {
	case "text":
		if strings.Contains(contentType, "text/html") {
			text, err := extractTextFromHTML(content)
			if err != nil {
				return NewTextErrorResponse("Failed to extract text from HTML: " + err.Error()), nil
			}
			out = text
		} else {
			out = content
		}

	case "markdown":
		if strings.Contains(contentType, "text/html") {
			markdown, err := convertHTMLToMarkdown(content)
			if err != nil {
				return NewTextErrorResponse("Failed to convert HTML to Markdown: " + err.Error()), nil
			}
			out = markdown
		} else {
			out = "```\n" + content + "\n```"
		}

	default: // "html" and any future format: return the body as fetched
		out = content
	}

	preview, filePath := PersistLargeOutput(out, "webfetch", WebFetchToolName, t.maxOutputBytes)
	if filePath != "" {
		logging.Info("webfetch output capped",
			"url", params.URL, "format", format, "totalBytes", len(out),
			"maxOutputBytes", t.maxOutputBytes, "file", filePath)
	}
	// The notice goes on after capping: it describes the fetch, not the
	// document, so it stays out of the spilled file and always sits at the
	// top of what the model reads.
	if bodyTruncated {
		preview = fmt.Sprintf(bodyTruncationNotice, maxSize) + preview
	}
	return NewTextResponse(preview), nil
}

func (t *fetchTool) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
	return true
}

func (t *fetchTool) IsBaseline() bool { return true }

// isWAFChallenge detects Cloudflare bot challenge responses.
// The cf-mitigated header is set by Cloudflare's edge network specifically
// to indicate a bot challenge page — it is not present on legitimate 403s.
func isWAFChallenge(resp *http.Response) bool {
	return resp.StatusCode == http.StatusForbidden &&
		resp.Header.Get("Cf-Mitigated") == "challenge"
}

func extractTextFromHTML(html string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}

	text := doc.Text()
	text = strings.Join(strings.Fields(text), " ")

	return text, nil
}

func convertHTMLToMarkdown(html string) (string, error) {
	markdown, err := htmltomarkdown.ConvertString(html)
	if err != nil {
		return "", err
	}
	return markdown, nil
}

var binaryContentTypes = []string{
	"application/octet-stream",
	"application/zip",
	"application/gzip",
	"application/x-tar",
	"application/java-archive",
	"application/x-java-archive",
	"application/pdf",
	"application/wasm",
	"application/x-executable",
	"application/x-mach-binary",
	"application/x-sharedlib",
	"image/",
	"audio/",
	"video/",
	"font/",
}

func isBinaryContent(contentType string, body []byte) bool {
	ct := strings.ToLower(contentType)
	for _, prefix := range binaryContentTypes {
		if strings.HasPrefix(ct, prefix) || strings.Contains(ct, prefix) {
			return true
		}
	}

	if len(body) > 0 {
		sampleSize := min(8192, len(body))
		if !utf8.Valid(body[:sampleSize]) {
			return true
		}
	}

	return false
}

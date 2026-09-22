// Command webfetch-e2e is a black-box driver for the webfetch-output-limit
// feature, invoked from scripts/test/webfetch_output_limit.sh with cwd set to
// a prepared sandbox. It mirrors cmd/context-e2e: the same config.Load the
// real binary uses, then the production surfaces on top of it — the real
// toolset wiring via agent.NewToolSet, the real webfetch/grep/read tools —
// against a local HTTP server, emitting a JSON verdict for the shell script
// to assert with jq.
//
// What only an e2e can show, and unit tests cannot: that webFetch
// .maxOutputBytes survives .opencode.json → viper → config → the tool's
// constructor, and that the spilled page is genuinely recoverable through a
// DIFFERENT tool (grep, then read) at the path the header advertises. That
// cross-tool recovery is the entire promise of the feature — a preview the
// agent cannot follow up on would be a regression, not a fix.
//
// Modes (-check):
//
//   - default_cap:    no webFetch config ⇒ the built-in 50KB cap applies; a
//     large page comes back small, names a spill file, and a
//     needle buried mid-page is ABSENT from the reply yet
//     recoverable with grep + read over that file.
//   - configured_cap: webFetch.maxOutputBytes in .opencode.json is honoured
//     (a much smaller reply than the default cap produces).
//   - unbounded:      webFetch.maxOutputBytes: -1 returns the whole page
//     inline with no spill file — the documented escape hatch.
//   - under_cap:      a small page is returned byte-identically to the
//     pre-feature conversion, with no header and no file.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	llmagent "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

const (
	sessionID = "webfetch-e2e-session"
	messageID = "webfetch-e2e-msg"
	// needle sits in the middle of the generated page, far outside both the
	// head and the tail of any preview the cap can produce.
	needle = "NEEDLE-4f21ab-buried-mid-document"
)

type result struct {
	OK          bool     `json:"ok"`
	Checks      []string `json:"checks"`
	Errors      []string `json:"errors"`
	ReplyBytes  int      `json:"reply_bytes,omitempty"`
	SavedBytes  int      `json:"saved_bytes,omitempty"`
	SpilledPath string   `json:"spilled_path,omitempty"`
}

func (r *result) pass(name string)        { r.Checks = append(r.Checks, name) }
func (r *result) fail(f string, a ...any) { r.Errors = append(r.Errors, fmt.Sprintf(f, a...)) }

func emit(r *result) {
	r.OK = len(r.Errors) == 0
	out, _ := json.Marshal(r)
	fmt.Println(string(out))
	if !r.OK {
		os.Exit(1)
	}
	os.Exit(0)
}

// noopLsp satisfies lsp.LspService without any server: the read tool calls
// NotifyOpenFile/FormatDiagnostics unconditionally and a nil interface would
// panic. Init is never called, so no LSP process ever starts.
type noopLsp struct {
	*pubsub.Broker[lsp.LSPServerEvent]
}

func (noopLsp) Init(context.Context)                       {}
func (noopLsp) Shutdown(context.Context)                   {}
func (noopLsp) ForceShutdown()                             {}
func (noopLsp) Clients() map[string]*lsp.Client            { return nil }
func (noopLsp) ClientsForFile(string) []*lsp.Client        { return nil }
func (noopLsp) NotifyOpenFile(context.Context, string)     {}
func (noopLsp) WaitForDiagnostics(context.Context, string) {}
func (noopLsp) FormatDiagnostics(string) string            { return "" }
func (noopLsp) ClientsCh() <-chan *lsp.Client {
	ch := make(chan *lsp.Client)
	close(ch)
	return ch
}

// bigPage builds an HTML document that converts to several hundred KB of
// markdown — comfortably past the 50KB default cap — with the needle at the
// midpoint.
func bigPage() string {
	var sb strings.Builder
	sb.WriteString("<html><body><h1>Reference</h1>")
	const paragraphs = 4000
	for i := range paragraphs {
		if i == paragraphs/2 {
			fmt.Fprintf(&sb, "<p>%s</p>", needle)
		}
		fmt.Fprintf(&sb, "<p>Section %d: filler prose that exists so the converted document is much larger than any cap under test.</p>", i)
	}
	sb.WriteString("</body></html>")
	return sb.String()
}

// mediumPage converts to well under the read tool's MaxReadSize ceiling but
// far over a small configured cap — the shape where a spilled page is still
// openable with the read tool.
func mediumPage() string {
	var sb strings.Builder
	sb.WriteString("<html><body><h1>Guide</h1>")
	for i := range 700 {
		if i == 350 {
			fmt.Fprintf(&sb, "<p>%s</p>", needle)
		}
		fmt.Fprintf(&sb, "<p>Item %d in a page of moderate length.</p>", i)
	}
	sb.WriteString("</body></html>")
	return sb.String()
}

func smallPage() string {
	return "<html><body><h1>Small</h1><p>One short paragraph.</p></body></html>"
}

func serve(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}))
}

// toolset returns the production-wired viewer tools for the explorer agent —
// the same NewToolSet the real binary calls, reading the same loaded config.
func toolset(r *result) map[string]tools.BaseTool {
	reg := agentregistry.GetRegistry()
	info, ok := reg.Get(string(config.AgentExplorer))
	if !ok {
		r.fail("explorer agent not in registry")
		return nil
	}
	perms := permission.NewPermissionService()
	perms.AutoApproveSession(sessionID)
	mcpReg := llmagent.NewMCPRegistry(context.Background(), perms, reg)
	lspSvc := noopLsp{Broker: pubsub.NewBroker[lsp.LSPServerEvent]()}

	out := map[string]tools.BaseTool{}
	for t := range llmagent.NewToolSet(&info, reg, perms, nil, lspSvc, nil, nil, mcpReg, nil, "") {
		out[t.Info().Name] = t
	}
	for _, name := range []string{tools.WebFetchToolName, tools.GrepToolName, tools.ReadToolName} {
		if out[name] == nil {
			r.fail("%s absent from the explorer toolset", name)
			return nil
		}
	}
	return out
}

func toolCtx() context.Context {
	ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, sessionID)
	return context.WithValue(ctx, tools.MessageIDContextKey, messageID)
}

func run(r *result, tool tools.BaseTool, input any) (string, bool) {
	raw, _ := json.Marshal(input)
	resp, err := tool.Run(toolCtx(), tools.ToolCall{ID: "1", Name: tool.Info().Name, Input: string(raw)})
	if err != nil {
		r.fail("%s returned err: %v", tool.Info().Name, err)
		return "", false
	}
	if resp.IsError {
		r.fail("%s returned an error response: %.200s", tool.Info().Name, resp.Content)
		return "", false
	}
	return resp.Content, true
}

func fetch(r *result, ts map[string]tools.BaseTool, url string) (string, bool) {
	return run(r, ts[tools.WebFetchToolName], map[string]string{"url": url, "format": "markdown"})
}

// spillPath extracts the path the overflow header advertises.
func spillPath(content string) string {
	const marker = "Full output saved to: "
	i := strings.Index(content, marker)
	if i < 0 {
		return ""
	}
	path, _, _ := strings.Cut(content[i+len(marker):], "\n")
	return strings.TrimSpace(path)
}

func main() {
	check := flag.String("check", "", "default_cap | configured_cap | unbounded | under_cap")
	flag.Parse()

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Getwd:", err)
		os.Exit(2)
	}
	// Same call internal/app makes at startup: .opencode.json → viper →
	// Config, the exact pipeline unit tests bypass.
	if _, err := config.Load(cwd, false); err != nil {
		fmt.Fprintln(os.Stderr, "config.Load:", err)
		os.Exit(2)
	}

	r := &result{}
	switch *check {
	case "default_cap":
		runDefaultCap(r)
	case "configured_cap":
		runConfiguredCap(r)
	case "unbounded":
		runUnbounded(r)
	case "under_cap":
		runUnderCap(r)
	default:
		fmt.Fprintln(os.Stderr, "unknown -check:", *check)
		os.Exit(2)
	}
	emit(r)
}

// runDefaultCap is the incident scenario: a page far larger than the context
// budget, fetched by an agent that configured nothing.
func runDefaultCap(r *result) {
	ts := toolset(r)
	if ts == nil {
		return
	}
	server := serve(bigPage())
	defer server.Close()

	reply, ok := fetch(r, ts, server.URL)
	if !ok {
		return
	}
	r.ReplyBytes = len(reply)

	path := spillPath(reply)
	r.SpilledPath = path
	if path == "" {
		r.fail("no spill-file marker in the reply: %.300s", reply)
		return
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		r.fail("spill file unreadable at the advertised path %q: %v", path, err)
		return
	}
	r.SavedBytes = len(saved)

	// The cap did its job: a big page, a small reply.
	switch {
	case len(saved) <= 50*1024:
		r.fail("fixture too small: saved output is %d bytes, expected > 50KB", len(saved))
	case len(reply) >= len(saved)/4:
		r.fail("reply is %d bytes against %d saved — the cap barely shrank anything", len(reply), len(saved))
	default:
		r.pass("large_page_reply_is_small")
	}

	if strings.Contains(reply, needle) {
		r.fail("needle survived into the reply; the fixture cannot prove the spill path")
		return
	}
	r.pass("mid_page_content_absent_from_reply")

	if !strings.Contains(string(saved), needle) {
		r.fail("needle missing from the spill file — content was lost, not relocated")
		return
	}
	r.pass("full_page_preserved_on_disk")

	// The promise the header makes to the agent, exercised through the
	// agent's OWN tools rather than os.ReadFile.
	grepOut, ok := run(r, ts[tools.GrepToolName], map[string]any{
		"pattern":      needle,
		"path":         filepath.Dir(path),
		"literal_text": true,
		"output_mode":  "content",
	})
	if ok {
		if strings.Contains(grepOut, needle) {
			r.pass("grep_tool_finds_needle_in_spill_file")
		} else {
			r.fail("grep over the spill dir did not surface the needle: %.300s", grepOut)
		}
	}

	// A spill this large is past the read tool's MaxReadSize ceiling, which
	// rejects a file on size before it ever looks at offset/limit. The
	// header must therefore not send the agent to read here — grep and sed
	// are the paths that work, and the guidance has to say so. (The read
	// tool is exercised on a spill within its ceiling by configured_cap.)
	if _, readable := run(&result{}, ts[tools.ReadToolName], map[string]any{"file_path": path}); readable {
		r.fail("read accepted a %d-byte spill; MaxReadSize behaviour changed and the header guidance is now stale", len(saved))
	} else {
		r.pass("oversized_spill_is_beyond_read_as_documented")
	}

	// The header must tell the agent what actually works, or none of the
	// above helps.
	for _, want := range []string{"grep tool", "sed in bash", "Do not re-run the tool"} {
		if !strings.Contains(reply, want) {
			r.fail("overflow header missing guidance %q; got: %.300s", want, reply)
			return
		}
	}
	r.pass("header_points_at_the_recovery_tools")
}

// runConfiguredCap proves the new field survives .opencode.json → viper →
// config → the tool constructor: the sandbox sets 4096, so the reply must be
// far smaller than the 50KB default would produce.
func runConfiguredCap(r *result) {
	cfg := config.Get()
	switch {
	case cfg.WebFetch == nil:
		r.fail("webFetch block did not survive config load")
		return
	case cfg.WebFetch.MaxOutputBytes != 4096:
		r.fail("webFetch.maxOutputBytes loaded as %d, want 4096", cfg.WebFetch.MaxOutputBytes)
		return
	}
	r.pass("config_field_round_trips_through_viper")

	ts := toolset(r)
	if ts == nil {
		return
	}
	server := serve(mediumPage())
	defer server.Close()

	reply, ok := fetch(r, ts, server.URL)
	if !ok {
		return
	}
	r.ReplyBytes = len(reply)

	// Preview is ~one cap of content plus the header; allow generous slack
	// for the header, but it must be nowhere near the 50KB default.
	if len(reply) < 4096+8192 {
		r.pass("configured_cap_bounds_the_reply")
	} else {
		r.fail("reply is %d bytes with a 4096-byte cap configured", len(reply))
	}
	path := spillPath(reply)
	r.SpilledPath = path
	if path == "" {
		r.fail("configured cap produced no spill file")
		return
	}
	r.pass("configured_cap_still_spills")

	saved, err := os.ReadFile(path)
	if err != nil {
		r.fail("spill file unreadable: %v", err)
		return
	}
	r.SavedBytes = len(saved)
	if len(saved) > tools.MaxReadSize {
		r.fail("fixture is %d bytes, past the read tool ceiling this check exists to cover", len(saved))
		return
	}

	// A spill within the read tool's ceiling must be openable with it, and
	// must carry the content the preview dropped.
	readOut, ok := run(r, ts[tools.ReadToolName], map[string]any{"file_path": path})
	if !ok {
		return
	}
	switch {
	case !strings.Contains(readOut, "# Guide"):
		r.fail("read of the spill file did not return the document: %.300s", readOut)
	case strings.Contains(reply, needle):
		r.fail("needle survived into the reply; the fixture cannot prove recovery")
	case !strings.Contains(string(saved), needle):
		r.fail("needle missing from the spill file")
	default:
		r.pass("read_tool_opens_spill_file")
	}
}

// runUnbounded proves the documented escape hatch: -1 restores the
// pre-feature behaviour exactly.
func runUnbounded(r *result) {
	cfg := config.Get()
	if cfg.WebFetch == nil || cfg.WebFetch.MaxOutputBytes != -1 {
		r.fail("sandbox did not load webFetch.maxOutputBytes: -1")
		return
	}
	ts := toolset(r)
	if ts == nil {
		return
	}
	server := serve(bigPage())
	defer server.Close()

	reply, ok := fetch(r, ts, server.URL)
	if !ok {
		return
	}
	r.ReplyBytes = len(reply)

	if strings.Contains(reply, "Full output saved to:") {
		r.fail("content was capped although the cap is disabled")
		return
	}
	r.pass("no_spill_when_cap_disabled")

	if strings.Contains(reply, needle) {
		r.pass("whole_page_returned_inline")
	} else {
		r.fail("needle missing although the whole page should have been returned")
	}
}

// runUnderCap is the back-compat check: a page within the cap must come back
// byte-identical to what the pre-feature code path returned — the conversion
// alone, no header, no file.
func runUnderCap(r *result) {
	ts := toolset(r)
	if ts == nil {
		return
	}
	body := smallPage()
	server := serve(body)
	defer server.Close()

	reply, ok := fetch(r, ts, server.URL)
	if !ok {
		return
	}
	r.ReplyBytes = len(reply)

	expected, err := htmltomarkdown.ConvertString(body)
	if err != nil {
		r.fail("reference conversion failed: %v", err)
		return
	}
	if reply == expected {
		r.pass("small_page_bytes_identical_to_pre_feature")
	} else {
		r.fail("reply differs from the plain conversion:\n got: %q\nwant: %q", reply, expected)
	}
	if strings.Contains(reply, "Full output saved to:") {
		r.fail("small page was spilled to a file")
	} else {
		r.pass("no_file_written_under_the_cap")
	}
}

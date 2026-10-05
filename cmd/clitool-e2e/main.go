// Command clitool-e2e is a black-box driver for the shared limit knobs of
// workspace CLI tools (docs/cli-tools.md, "Limits"), invoked from
// scripts/test/cli_tools_limits.sh with cwd set to a prepared sandbox. It
// mirrors cmd/webfetch-e2e: the same config.Load the real binary uses, then
// the production toolset wiring — agent.NewToolSet for the explorer agent,
// which the sandbox's .opencode.json grants the sandbox manifests — and a
// real tool.Run, emitting a JSON verdict the shell script asserts with jq.
//
// What only this can show, and unit tests cannot: that cliTools.timeout and
// cliTools.maxOutputBytes (and their OPENCODE_CLI_TOOLS_* overrides) survive
// .opencode.json → viper → config → clitool.Discover → the NATIVE tool's
// Run, on the same values `opencode tools serve` applies — the script drives
// that second surface itself, against the same sandbox.
//
// Modes (-check):
//
//   - configured_cap:     cliTools.maxOutputBytes bounds a native reply and
//     spills the whole output to the advertised file.
//   - configured_timeout: cliTools.timeout kills a native call that runs
//     longer, as an error response naming the timeout.
//   - env_override:       OPENCODE_CLI_TOOLS_MAX_OUTPUT_BYTES=-1 wins over
//     the config cap: the whole output comes back inline.
//   - manifest_wins:      a manifest's own `timeout` beats the config default.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	llmagent "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

const (
	sessionID = "clitool-e2e-session"
	messageID = "clitool-e2e-msg"
)

type result struct {
	OK          bool     `json:"ok"`
	Checks      []string `json:"checks"`
	Errors      []string `json:"errors"`
	ReplyBytes  int      `json:"reply_bytes,omitempty"`
	SavedBytes  int      `json:"saved_bytes,omitempty"`
	SpilledPath string   `json:"spilled_path,omitempty"`
	ElapsedMs   int64    `json:"elapsed_ms,omitempty"`
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

// noopLsp satisfies lsp.LspService without any server (the built-in viewer
// tools in the explorer toolset take one; none is ever started).
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

// toolset is the production-wired toolset of the explorer agent — the same
// NewToolSet the real binary calls, reading the same loaded config, so the
// sandbox manifests reach it exactly as they reach a running agent.
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
	return out
}

func toolCtx() context.Context {
	ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, sessionID)
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, messageID)
	return context.WithValue(ctx, tools.AgentIDContextKey, config.AgentExplorer)
}

// call runs one native CLI tool with an empty argument vector and records
// how long it took.
func call(r *result, ts map[string]tools.BaseTool, name string) (tools.ToolResponse, bool) {
	tool := ts[name]
	if tool == nil {
		r.fail("%s absent from the explorer toolset (grant missing in .opencode.json, or the manifest did not load)", name)
		return tools.ToolResponse{}, false
	}
	start := time.Now()
	resp, err := tool.Run(toolCtx(), tools.ToolCall{ID: "1", Name: name, Input: `{"args":[]}`})
	r.ElapsedMs = time.Since(start).Milliseconds()
	if err != nil {
		r.fail("%s returned a Go error (that ends an agent run): %v", name, err)
		return resp, false
	}
	r.ReplyBytes = len(resp.Content)
	return resp, true
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
	check := flag.String("check", "", "configured_cap | configured_timeout | env_override | manifest_wins")
	flag.Parse()

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Getwd:", err)
		os.Exit(2)
	}
	// Same call internal/app makes at startup: .opencode.json → viper →
	// Config — the exact pipeline the unit tests bypass.
	if _, err := config.Load(cwd, false); err != nil {
		fmt.Fprintln(os.Stderr, "config.Load:", err)
		os.Exit(2)
	}

	r := &result{}
	ts := toolset(r)
	if ts == nil {
		emit(r)
	}
	switch *check {
	case "configured_cap":
		runConfiguredCap(r, ts)
	case "configured_timeout":
		runConfiguredTimeout(r, ts)
	case "env_override":
		runEnvOverride(r, ts)
	case "manifest_wins":
		runManifestWins(r, ts)
	default:
		fmt.Fprintln(os.Stderr, "unknown -check:", *check)
		os.Exit(2)
	}
	emit(r)
}

// runConfiguredCap: cliTools.maxOutputBytes (1024 in the sandbox) bounds the
// reply of `spew`, a tool that prints ~20KB and sets no cap of its own.
func runConfiguredCap(r *result, ts map[string]tools.BaseTool) {
	resp, ok := call(r, ts, "spew")
	if !ok {
		return
	}
	if resp.IsError {
		r.fail("spew returned an error response: %.300s", resp.Content)
		return
	}
	if !strings.Contains(resp.Content, "<spew output truncated:") {
		r.fail("no overflow header — the config cap did not reach the native tool: %.300s", resp.Content)
		return
	}
	r.pass("config_cap_round_trips_to_native_tool")
	if len(resp.Content) > 4096 {
		r.fail("reply is %d bytes under a 1024-byte cap", len(resp.Content))
	} else {
		r.pass("native_reply_bounded_by_config_cap")
	}
	path := spillPath(resp.Content)
	r.SpilledPath = path
	saved, err := os.ReadFile(path)
	if err != nil {
		r.fail("spill file unreadable at the advertised path %q: %v", path, err)
		return
	}
	r.SavedBytes = len(saved)
	if len(saved) < 10000 || !strings.Contains(string(saved), "line 399") {
		r.fail("spill file does not hold the whole output (%d bytes)", len(saved))
	} else {
		r.pass("spill_file_holds_full_output")
	}
	if strings.Contains(resp.Metadata, `"temp_file_path"`) {
		r.pass("metadata_names_spill_file")
	} else {
		r.fail("metadata lacks temp_file_path: %s", resp.Metadata)
	}
}

// runConfiguredTimeout: cliTools.timeout ("1s" in the sandbox) kills `nap`
// (sleep 5), a tool that sets no timeout of its own.
func runConfiguredTimeout(r *result, ts map[string]tools.BaseTool) {
	resp, ok := call(r, ts, "nap")
	if !ok {
		return
	}
	if !resp.IsError || !strings.Contains(resp.Content, "timed out after 1s") {
		r.fail("expected a timeout error after 1s, got isError=%v: %.300s", resp.IsError, resp.Content)
		return
	}
	r.pass("config_timeout_round_trips_to_native_tool")
	if r.ElapsedMs > 4000 {
		r.fail("call took %dms; the process group was not killed at the configured timeout", r.ElapsedMs)
	} else {
		r.pass("native_call_killed_at_config_timeout")
	}
	if strings.Contains(resp.Metadata, `"timed_out":true`) {
		r.pass("metadata_reports_timeout")
	} else {
		r.fail("metadata lacks timed_out: %s", resp.Metadata)
	}
}

// runEnvOverride: with OPENCODE_CLI_TOOLS_MAX_OUTPUT_BYTES=-1 in the
// environment the config cap no longer applies — the whole output is inline.
func runEnvOverride(r *result, ts map[string]tools.BaseTool) {
	resp, ok := call(r, ts, "spew")
	if !ok {
		return
	}
	if resp.IsError {
		r.fail("spew returned an error response: %.300s", resp.Content)
		return
	}
	if strings.Contains(resp.Content, "output truncated") || len(resp.Content) < 10000 {
		r.fail("env override not applied: %d bytes, truncated=%v", len(resp.Content), strings.Contains(resp.Content, "output truncated"))
		return
	}
	r.pass("env_unbounded_overrides_config_cap")
}

// runManifestWins: cliTools.timeout is 1s, but `nap_long` declares
// `timeout: 10s` and sleeps 2s — the manifest's own field must win.
func runManifestWins(r *result, ts map[string]tools.BaseTool) {
	resp, ok := call(r, ts, "nap_long")
	if !ok {
		return
	}
	if resp.IsError || !strings.Contains(resp.Content, "exit status 0") {
		r.fail("manifest timeout did not beat the config default: isError=%v %.300s", resp.IsError, resp.Content)
		return
	}
	r.pass("manifest_timeout_beats_config_default")
}

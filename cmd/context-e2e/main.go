// Command context-e2e is a black-box driver for the scoped-context-files
// feature, invoked from scripts/test/scoped_context.sh with cwd set to a
// prepared sandbox. It mirrors cmd/hooks-e2e: the same config.Load the real
// binary uses, then the production surfaces on top of it — prompt assembly
// via prompt.GetAgentPrompt and the real toolset wiring via agent.NewToolSet
// — and emits a JSON verdict for the shell script to assert with jq.
//
// Modes (-check):
//
//   - backcompat: a sandbox with a root AGENTS.md and no context config must
//     produce a system prompt whose context section is byte-identical to the
//     pre-feature construction (header quirk included) and carry no manifest.
//     The prompt's sha256 is emitted so the script can A/B two driver
//     processes for cross-process determinism.
//   - manifest: a sandbox with nested services/*/AGENTS.md files must list
//     them (with labels) in a byte-stable manifest section while keeping
//     their bodies OUT of the prompt.
//   - disclosure: the first successful read into services/auth/ must inject
//     the nested body as a <system-reminder> in the tool result; a second
//     read into the same directory must not re-inject; the system prompt
//     must be unchanged by activation.
//   - override: an .opencode.json agent-level context override
//     (paths: [AGENTS.runtime.md], mode: replace) loaded through the real
//     viper pipeline must exclude the root AGENTS.md content — the
//     isolated-config exclusion scenario this change exists for.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	llmagent "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/prompt"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

type result struct {
	OK           bool     `json:"ok"`
	Checks       []string `json:"checks"`
	Errors       []string `json:"errors"`
	PromptSHA256 string   `json:"prompt_sha256,omitempty"`
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
// NotifyOpenFile/FormatDiagnostics unconditionally and a nil interface
// would panic. Init is never called, so no LSP process ever starts.
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

func main() {
	check := flag.String("check", "", "backcompat | manifest | disclosure | override")
	flag.Parse()

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Getwd:", err)
		os.Exit(2)
	}
	// Same call internal/app makes at startup: .opencode.json → viper →
	// Config, including the contextDiscovery defaults and the agents map
	// merge — the exact pipeline unit tests bypass.
	if _, err := config.Load(cwd, false); err != nil {
		fmt.Fprintln(os.Stderr, "config.Load:", err)
		os.Exit(2)
	}
	cfg := config.Get()
	if cfg == nil {
		fmt.Fprintln(os.Stderr, "config.Get returned nil")
		os.Exit(2)
	}

	r := &result{}
	switch *check {
	case "backcompat":
		runBackcompat(r, cfg)
	case "manifest":
		runManifest(r, cfg)
	case "disclosure":
		runDisclosure(r, cfg)
	case "override":
		runOverride(r, cfg)
	default:
		fmt.Fprintln(os.Stderr, "unknown -check:", *check)
		os.Exit(2)
	}
	emit(r)
}

func mustRead(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixture read:", err)
		os.Exit(2)
	}
	return string(b)
}

// runBackcompat asserts the no-context-config prompt against an
// independently constructed pre-feature reference: the exact header (with
// its historical leading space) plus one "# From:<abs>\n<body>" block for
// the sandbox's sole context file. This is the A side of the A/B; the B
// side is history — the construction below is the byte format the retired
// getContextFromPaths() produced.
func runBackcompat(r *result, cfg *config.Config) {
	first := prompt.GetAgentPrompt(config.AgentCoder, models.ProviderAnthropic)
	second := prompt.GetAgentPrompt(config.AgentCoder, models.ProviderAnthropic)
	if first == second {
		r.pass("prompt_stable_across_turns")
	} else {
		r.fail("prompt differs between two builds in the same process")
	}

	rootAgents := filepath.Join(cfg.WorkingDir, "AGENTS.md")
	expected := "\n\n# Project-Specific Context\n Make sure to follow the instructions in the context below\n" +
		"# From:" + rootAgents + "\n" + mustRead(rootAgents)
	if strings.HasSuffix(first, expected) {
		r.pass("context_block_matches_pre_feature_bytes")
	} else {
		tail := first
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		r.fail("context section is not the pre-feature bytes; prompt tail: %q", tail)
	}

	if !strings.Contains(first, "# Nested Context Files") {
		r.pass("no_manifest_without_nested_files")
	} else {
		r.fail("manifest section present although the sandbox has no nested context files")
	}

	sum := sha256.Sum256([]byte(first))
	r.PromptSHA256 = hex.EncodeToString(sum[:])
}

func runManifest(r *result, cfg *config.Config) {
	first := prompt.GetAgentPrompt(config.AgentCoder, models.ProviderAnthropic)
	second := prompt.GetAgentPrompt(config.AgentCoder, models.ProviderAnthropic)

	if strings.Contains(first, "# Nested Context Files") {
		r.pass("manifest_present")
	} else {
		r.fail("manifest section missing although nested context files exist")
	}
	for check, line := range map[string]string{
		"manifest_lists_auth_with_heading_label":        "- " + filepath.Join("services", "auth", "AGENTS.md") + ": Auth service rules",
		"manifest_lists_billing_with_frontmatter_label": "- " + filepath.Join("services", "billing", "AGENTS.md") + ": Billing invariants for the e2e fixture",
	} {
		if strings.Contains(first, line) {
			r.pass(check)
		} else {
			r.fail("manifest line missing: %q", line)
		}
	}
	if first == second {
		r.pass("manifest_byte_stable")
	} else {
		r.fail("prompt with manifest differs between two builds")
	}

	rootAgents := filepath.Join(cfg.WorkingDir, "AGENTS.md")
	if strings.Contains(first, "# From:"+rootAgents) {
		r.pass("root_context_still_inline")
	} else {
		r.fail("root AGENTS.md block missing from prompt")
	}
	// Bodies stay out of the prompt — that is the entire point of the
	// manifest. The fixture bodies carry these unique markers.
	if !strings.Contains(first, "NESTED-AUTH-BODY") && !strings.Contains(first, "NESTED-BILLING-BODY") {
		r.pass("nested_bodies_not_inlined")
	} else {
		r.fail("nested file body leaked into the system prompt")
	}

	sum := sha256.Sum256([]byte(first))
	r.PromptSHA256 = hex.EncodeToString(sum[:])
}

func runDisclosure(r *result, cfg *config.Config) {
	const sessionID = "context-e2e-session"

	reg := agentregistry.GetRegistry()
	info, ok := reg.Get(string(config.AgentExplorer))
	if !ok {
		r.fail("explorer agent not in registry")
		return
	}
	perms := permission.NewPermissionService()
	perms.AutoApproveSession(sessionID)
	mcpReg := llmagent.NewMCPRegistry(context.Background(), perms, reg)

	// The production wiring: NewToolSet decides whether the disclosure
	// wrapper is installed, from the same loaded config the prompt uses.
	// Explorer is read-only + subagent, so the heavyweight services
	// (history, sessions, messages, factory) are never touched.
	lspSvc := noopLsp{Broker: pubsub.NewBroker[lsp.LSPServerEvent]()}
	var readTool tools.BaseTool
	for t := range llmagent.NewToolSet(&info, reg, perms, nil, lspSvc, nil, nil, mcpReg, nil) {
		if t.Info().Name == tools.ReadToolName {
			readTool = t
		}
	}
	if readTool == nil {
		r.fail("read tool absent from explorer toolset")
		return
	}

	promptBefore := prompt.GetAgentPrompt(config.AgentExplorer, models.ProviderAnthropic)

	ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, sessionID)
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "context-e2e-msg")

	authAgents := filepath.Join(cfg.WorkingDir, "services", "auth", "AGENTS.md")
	reminder := "<system-reminder>\n# From:" + authAgents + "\n" + mustRead(authAgents)

	run := func(rel string) (tools.ToolResponse, error) {
		input, _ := json.Marshal(map[string]string{"file_path": rel})
		return readTool.Run(ctx, tools.ToolCall{ID: "1", Name: tools.ReadToolName, Input: string(input)})
	}

	resp, err := run("services/auth/handler.go")
	switch {
	case err != nil || resp.IsError:
		r.fail("first read failed: err=%v content=%.200s", err, resp.Content)
	case !strings.HasPrefix(resp.Content, "<file>"):
		r.fail("read output missing before injection: %.200s", resp.Content)
	case !strings.Contains(resp.Content, reminder):
		r.fail("first read into services/auth did not inject the nested body: %.400s", resp.Content)
	default:
		r.pass("first_read_injects_nested_body")
	}

	resp, err = run("services/auth/util.go")
	switch {
	case err != nil || resp.IsError:
		r.fail("second read failed: err=%v content=%.200s", err, resp.Content)
	case strings.Contains(resp.Content, "<system-reminder>"):
		r.fail("second touch of services/auth re-injected: %.400s", resp.Content)
	default:
		r.pass("second_touch_does_not_reinject")
	}

	if prompt.GetAgentPrompt(config.AgentExplorer, models.ProviderAnthropic) == promptBefore {
		r.pass("system_prompt_unchanged_by_activation")
	} else {
		r.fail("system prompt bytes changed after body injection")
	}
}

func runOverride(r *result, cfg *config.Config) {
	built := prompt.GetAgentPrompt(config.AgentCoder, models.ProviderAnthropic)

	runtimeAgents := filepath.Join(cfg.WorkingDir, "AGENTS.runtime.md")
	rootAgents := filepath.Join(cfg.WorkingDir, "AGENTS.md")

	if strings.Contains(built, "# From:"+runtimeAgents) && strings.Contains(built, "RUNTIME-MARKER") {
		r.pass("override_includes_runtime_context")
	} else {
		r.fail("agent context override did not resolve AGENTS.runtime.md")
	}
	if !strings.Contains(built, "# From:"+rootAgents) && !strings.Contains(built, "ROOT-MARKER") {
		r.pass("override_excludes_root_context")
	} else {
		r.fail("mode: replace failed to exclude the root AGENTS.md content")
	}
}

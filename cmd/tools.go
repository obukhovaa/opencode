package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/clitool"
	"github.com/opencode-ai/opencode/internal/clitool/mcpserve"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/version"
)

var toolsCmd = &cobra.Command{
	Use:   "tools",
	Short: "Inspect and serve workspace-defined CLI tools",
	Long: `Workspace-defined CLI tools are declarative manifests (.agents/tools/<name>.yaml)
that wrap a host binary as a first-class opencode tool, executed argv-only
(no shell) under the manifest's argument, environment, cwd, time and output
policy. See docs/cli-tools.md.

  opencode tools list   audits the manifests the working directory resolves
  opencode tools serve  exposes them over stdio MCP (Claude Code, other clients)`,
}

var toolsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the CLI tool manifests resolved for the working directory",
	Example: `
  # Human-readable audit of every manifest, with invalid and shadowed files
  opencode tools list

  # Fail (exit 1) when any manifest is invalid or a cliTools limit setting
  # (config or OPENCODE_CLI_TOOLS_*) was ignored — for a workspace CI job
  opencode tools list --strict

  # Which tools a given agent holds, and whether they are deferred
  opencode tools list --agent piano-snowflake-explorer

  # Machine-readable
  opencode tools list --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadToolsConfig(cmd)
		if err != nil {
			return err
		}
		asJSON, _ := cmd.Flags().GetBool("json")
		strict, _ := cmd.Flags().GetBool("strict")
		agentID, _ := cmd.Flags().GetString("agent")

		set := clitool.Discover(cmd.Context(), cfg.WorkingDir, cfg.CLITools)
		var info *agentregistry.AgentInfo
		if agentID != "" {
			a, ok := agentregistry.GetRegistry().Get(agentID)
			if !ok {
				return fmt.Errorf("agent %q is not defined in this workspace", agentID)
			}
			info = &a
		}
		report := buildToolsReport(set, info)

		cmd.SilenceUsage = true
		if asJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(report); err != nil {
				return err
			}
		} else {
			printToolsReport(cmd, report)
		}
		if strict && (report.Invalid > 0 || len(report.Warnings) > 0) {
			return fmt.Errorf("%d invalid CLI tool manifest(s), %d ignored limit setting(s)", report.Invalid, len(report.Warnings))
		}
		return nil
	},
}

var toolsServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve the CLI tool manifests over stdio MCP",
	Long: `Start a Model Context Protocol server on stdin/stdout whose tools are the
valid manifests of the working directory. Each call runs the same argument
policy and executor as the native opencode tool. Without an agent there is no
tools: gating and no human in the loop: a manifest default permission of
"deny" refuses the call, "ask" and "allow" run it — the MCP client (e.g.
Claude Code's mcp__<server>__<tool> rules) owns the prompt. The limits a
manifest leaves unset (timeout, maxTimeout, maxOutputBytes) come from the
same cliTools config block and OPENCODE_CLI_TOOLS_* variables the native
tools use, so both surfaces agree. Logs go to stderr; stdout carries only
protocol messages.`,
	Example: `
  # .mcp.json for Claude Code
  {"mcpServers": {"cli": {"command": "opencode", "args": ["tools", "serve", "--cwd", "/path/to/workspace"]}}}

  # Serve only some tools
  opencode tools serve --only snow_dcs,snow_logging`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadToolsConfig(cmd)
		if err != nil {
			return err
		}
		only, _ := cmd.Flags().GetString("only")
		set := clitool.Discover(cmd.Context(), cfg.WorkingDir, cfg.CLITools)
		manifests := set.Manifests
		if only != "" {
			want := map[string]bool{}
			for _, n := range strings.Split(only, ",") {
				if n = strings.TrimSpace(n); n != "" {
					want[n] = true
				}
			}
			var filtered []*clitool.Manifest
			for _, m := range manifests {
				if want[m.Name] {
					filtered = append(filtered, m)
					delete(want, m.Name)
				}
			}
			for missing := range want {
				logging.Warn("--only names a tool that is not defined", "tool", missing)
			}
			manifests = filtered
		}
		for _, d := range set.Diagnostics {
			if !d.Shadowed {
				logging.Warn("CLI tool manifest not served", "path", d.Path, "reason", d.Reason)
			}
		}
		for _, w := range set.Warnings {
			logging.Warn("CLI tool limit setting ignored", "detail", w)
		}
		names := make([]string, 0, len(manifests))
		for _, m := range manifests {
			names = append(names, m.Name)
		}
		// The limits are the same ones an agent's native toolset applies:
		// resolved from this --cwd's .opencode.json and the environment.
		logging.Info("Serving CLI tools over stdio MCP", "tools", strings.Join(names, ", "), "cwd", cfg.WorkingDir,
			"timeout", set.Defaults.Timeout.String(), "maxTimeout", set.Defaults.MaxTimeout.String(),
			"maxOutputBytes", set.Defaults.MaxOutputBytes)

		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigCh
			cancel()
		}()
		srv := mcpserve.New(manifests, version.Version)
		err = mcpserve.Serve(ctx, srv, os.Stdin, os.Stdout, log.New(os.Stderr, "clitool-mcp ", log.LstdFlags))
		if err != nil && (errors.Is(err, context.Canceled) || ctx.Err() != nil) {
			return nil
		}
		return err
	},
}

// loadToolsConfig applies --cwd / --debug and loads the configuration with
// logging on stderr so stdout stays clean for the listing or the protocol.
func loadToolsConfig(cmd *cobra.Command) (*config.Config, error) {
	cwd, _ := cmd.Flags().GetString("cwd")
	debug, _ := cmd.Flags().GetBool("debug")
	if cwd != "" {
		if err := os.Chdir(cwd); err != nil {
			return nil, fmt.Errorf("failed to change directory: %w", err)
		}
	}
	// Always resolve to the absolute path: `--cwd .` (the .mcp.json recipe
	// in docs/cli-tools.md) must not leave config.WorkingDir — and with it
	// every manifest location and the cwd-confinement root — relative.
	c, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current working directory: %w", err)
	}
	cwd = c
	cfg, err := config.Load(cwd, debug)
	if err != nil {
		return nil, err
	}
	level := slog.LevelWarn
	if debug {
		level = slog.LevelDebug
	}
	logging.SetupStderrLogging(level)
	return cfg, nil
}

// toolsReport is the `tools list` payload (also the --json shape).
type toolsReport struct {
	Disabled    bool               `json:"disabled"`
	Dirs        []string           `json:"dirs"`
	Defaults    defaultsReport     `json:"defaults"`
	Warnings    []string           `json:"warnings"`
	Tools       []toolReport       `json:"tools"`
	Diagnostics []diagnosticReport `json:"diagnostics"`
	Invalid     int                `json:"invalid"`
	Shadowed    int                `json:"shadowed"`
}

// defaultsReport is the resolved shared-limit layer (docs/cli-tools.md,
// "Limits"): what every manifest inherits for the fields it leaves unset,
// and which layer — builtin, config or env — supplied each value.
type defaultsReport struct {
	Timeout              string `json:"timeout"`
	TimeoutSource        string `json:"timeoutSource"`
	MaxTimeout           string `json:"maxTimeout"`
	MaxTimeoutSource     string `json:"maxTimeoutSource"`
	MaxOutputBytes       int    `json:"maxOutputBytes"`
	MaxOutputBytesSource string `json:"maxOutputBytesSource"`
}

type toolReport struct {
	Name           string           `json:"name"`
	File           string           `json:"file"`
	Mode           string           `json:"mode"`
	Grant          string           `json:"grant"`
	Command        string           `json:"command"`
	Resolved       string           `json:"resolved,omitempty"`
	Found          bool             `json:"found"`
	PrefixArgs     []string         `json:"prefixArgs,omitempty"`
	Deny           []string         `json:"deny"`
	Allow          []string         `json:"allow"`
	Stdin          bool             `json:"stdin"`
	EnvInherit     bool             `json:"envInherit"`
	EnvPass        []string         `json:"envPass,omitempty"`
	EnvSet         []string         `json:"envSet,omitempty"`
	Cwd            string           `json:"cwd"`
	Timeout        string           `json:"timeout"`
	MaxTimeout     string           `json:"maxTimeout"`
	MaxOutputBytes int              `json:"maxOutputBytes"`
	HelpCaptured   bool             `json:"helpCaptured"`
	HelpBytes      int              `json:"helpBytes,omitempty"`
	Permission     map[string]any   `json:"permission,omitempty"`
	Parameters     []string         `json:"parameters,omitempty"`
	Required       []string         `json:"required,omitempty"`
	TemplateRefs   []string         `json:"templateRefs,omitempty"`
	Agent          *agentToolReport `json:"agent,omitempty"`
}

type agentToolReport struct {
	ID       string `json:"id"`
	Granted  bool   `json:"granted"`
	Deferred bool   `json:"deferred"`
	Rule     string `json:"rule"`
}

type diagnosticReport struct {
	Path     string `json:"path"`
	Reason   string `json:"reason"`
	Shadowed bool   `json:"shadowed"`
}

func buildToolsReport(set *clitool.Set, info *agentregistry.AgentInfo) toolsReport {
	r := toolsReport{Disabled: set.Disabled, Dirs: set.Dirs, Warnings: orEmpty(set.Warnings),
		Tools: []toolReport{}, Diagnostics: []diagnosticReport{}}
	if r.Dirs == nil {
		r.Dirs = []string{}
	}
	if !set.Disabled {
		d := set.Defaults
		r.Defaults = defaultsReport{
			Timeout: d.Timeout.String(), TimeoutSource: d.TimeoutSource,
			MaxTimeout: d.MaxTimeout.String(), MaxTimeoutSource: d.MaxTimeoutSource,
			MaxOutputBytes: d.MaxOutputBytes, MaxOutputBytesSource: d.MaxOutputBytesSource,
		}
	}
	for _, m := range set.Manifests {
		tr := toolReport{
			Name: m.Name, File: m.Location, Mode: string(m.Mode), Grant: string(m.Grant),
			Command: m.Command, Resolved: m.ResolvedCommand, Found: m.CommandFound(),
			PrefixArgs: m.PrefixArgs, Deny: orEmpty(m.Args.Deny), Allow: orEmpty(m.Args.Allow),
			Stdin: m.Stdin, EnvInherit: m.EnvInherit(), EnvPass: m.Env.Pass, Cwd: m.ResolvedCwd,
			Timeout: m.TimeoutDefault().String(), MaxTimeout: m.TimeoutMax().String(),
			MaxOutputBytes: m.MaxOutput(), HelpCaptured: m.HelpText != "", HelpBytes: len(m.HelpText),
			Permission: m.PermissionDefault(),
		}
		for k := range m.Env.Set {
			tr.EnvSet = append(tr.EnvSet, k)
		}
		sort.Strings(tr.EnvSet)
		if m.Mode == clitool.ModeStructured {
			for k := range m.Parameters {
				tr.Parameters = append(tr.Parameters, k)
			}
			sort.Strings(tr.Parameters)
			tr.Required = m.Required
			tr.TemplateRefs = m.TemplateRefs()
		}
		if info != nil {
			ar := &agentToolReport{ID: info.ID}
			if m.Grant == clitool.GrantImplicit {
				ar.Granted = info.ToolEnabled(m.Name)
				ar.Rule = "implicit (enabled unless denied in tools/allowTools)"
			} else {
				ar.Granted = info.ToolExplicitlyEnabled(m.Name)
				ar.Rule = "explicit (must be named in tools/allowTools)"
			}
			ar.Deferred = ar.Granted && permission.IsToolDeferred(m.Name, info.DeferredTools)
			tr.Agent = ar
		}
		r.Tools = append(r.Tools, tr)
	}
	for _, d := range set.Diagnostics {
		r.Diagnostics = append(r.Diagnostics, diagnosticReport{Path: d.Path, Reason: d.Reason, Shadowed: d.Shadowed})
		if d.Shadowed {
			r.Shadowed++
		} else {
			r.Invalid++
		}
	}
	return r
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func printToolsReport(cmd *cobra.Command, r toolsReport) {
	w := cmd.OutOrStdout()
	if r.Disabled {
		fmt.Fprintln(w, "CLI tools are disabled (cliTools.disabled or OPENCODE_DISABLE_CLI_TOOLS).")
		return
	}
	if len(r.Dirs) == 0 {
		fmt.Fprintln(w, "No CLI tool directories found (.agents/tools, .opencode/tools, ~/.config/opencode/tools, ~/.agents/tools, cliTools.paths).")
	} else {
		fmt.Fprintf(w, "Scanned: %s\n", strings.Join(r.Dirs, ", "))
	}
	fmt.Fprintf(w, "Limits:  timeout %s (%s); max timeout %s (%s); output cap %d bytes (%s)\n",
		r.Defaults.Timeout, r.Defaults.TimeoutSource, r.Defaults.MaxTimeout, r.Defaults.MaxTimeoutSource,
		r.Defaults.MaxOutputBytes, r.Defaults.MaxOutputBytesSource)
	for _, msg := range r.Warnings {
		fmt.Fprintf(w, "WARNING: %s\n", msg)
	}
	if len(r.Tools) == 0 {
		fmt.Fprintln(w, "No CLI tools.")
	}
	for _, t := range r.Tools {
		found := t.Resolved
		if !t.Found {
			found = "NOT FOUND"
		}
		fmt.Fprintf(w, "\n%s  (%s, grant: %s)\n", t.Name, t.Mode, t.Grant)
		fmt.Fprintf(w, "  file:     %s\n", t.File)
		fmt.Fprintf(w, "  command:  %s -> %s", t.Command, found)
		if len(t.PrefixArgs) > 0 {
			fmt.Fprintf(w, "  prefixArgs: %q", t.PrefixArgs)
		}
		fmt.Fprintln(w)
		env := "inherit"
		if !t.EnvInherit {
			env = fmt.Sprintf("restricted (PATH, HOME, TMPDIR + %d passed)", len(t.EnvPass))
		}
		if len(t.EnvSet) > 0 {
			env += fmt.Sprintf(", set %s", strings.Join(t.EnvSet, ","))
		}
		fmt.Fprintf(w, "  policy:   deny %d, allow %d; stdin: %v; env: %s\n", len(t.Deny), len(t.Allow), t.Stdin, env)
		fmt.Fprintf(w, "  limits:   timeout %s (max %s); output cap %d bytes; cwd %s\n", t.Timeout, t.MaxTimeout, t.MaxOutputBytes, t.Cwd)
		if t.HelpCaptured {
			fmt.Fprintf(w, "  help:     captured %d bytes into the description\n", t.HelpBytes)
		}
		if len(t.Permission) > 0 {
			raw, _ := json.Marshal(t.Permission)
			fmt.Fprintf(w, "  default permission: %s\n", raw)
		}
		if t.Mode == string(clitool.ModeStructured) {
			fmt.Fprintf(w, "  parameters: %s (required: %s)\n", strings.Join(t.Parameters, ", "), strings.Join(t.Required, ", "))
		}
		if t.Agent != nil {
			state := "not granted"
			if t.Agent.Granted {
				state = "granted"
				if t.Agent.Deferred {
					state += ", deferred"
				}
			}
			fmt.Fprintf(w, "  agent %s: %s — %s\n", t.Agent.ID, state, t.Agent.Rule)
		}
	}
	if len(r.Diagnostics) > 0 {
		fmt.Fprintln(w, "\nDiagnostics:")
		for _, d := range r.Diagnostics {
			kind := "INVALID "
			if d.Shadowed {
				kind = "shadowed"
			}
			fmt.Fprintf(w, "  %s %s: %s\n", kind, d.Path, d.Reason)
		}
	}
}

func init() {
	for _, c := range []*cobra.Command{toolsListCmd, toolsServeCmd} {
		c.Flags().StringP("cwd", "c", "", "Working directory for the workspace")
		c.Flags().BoolP("debug", "d", false, "Enable debug logging (stderr)")
	}
	toolsListCmd.Flags().Bool("json", false, "Print the report as JSON")
	toolsListCmd.Flags().Bool("strict", false, "Exit 1 when any manifest is invalid")
	toolsListCmd.Flags().String("agent", "", "Show whether this agent holds each tool")
	toolsServeCmd.Flags().String("only", "", "Comma-separated tool names to serve (default: all)")
	toolsCmd.AddCommand(toolsListCmd, toolsServeCmd)
	rootCmd.AddCommand(toolsCmd)
}

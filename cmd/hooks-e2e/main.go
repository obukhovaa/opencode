// Command hooks-e2e is a black-box driver that exercises the full
// .opencode.json → viper → Config.Hooks → hooks.Registry pipeline
// against a temporary sandbox. It exists to give scripts/test/hooks.sh
// a deterministic harness without needing an LLM or a running serve
// instance — hooks fire during tool dispatch, but the dispatch path
// can be simulated by calling Registry.RunPreTool / RunPostTool
// directly.
//
// The driver is intentionally small. It:
//
//  1. Calls config.Load(cwd) — same loader the real binary uses.
//  2. Builds a Registry whose getter reads Config.Hooks.
//  3. Fires PreToolUse + PostToolUse with synthetic events.
//  4. Prints the decisions as JSON on stdout.
//
// Any divergence between this and production (e.g. a viper version that
// changes key case-folding, or a config field rename that breaks
// unmarshal) shows up as a non-matching JSON output in the shell test —
// which is exactly the signal the integration-tests-bypass-viper class
// of regressions otherwise hide.
//
// Usage: invoked from scripts/test/hooks.sh; cwd is expected to be the
// sandbox directory containing the .opencode.json + hook scripts.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/hooks"
)

func main() {
	tool := flag.String("tool", "bash", "tool name passed to PreToolUse/PostToolUse")
	cmd := flag.String("cmd", "git status", "value for tool_input.command")
	output := flag.String("output", "200 lines of noisy output", "value for PostToolUse tool_output")
	skipPre := flag.Bool("skip-pre", false, "do not fire PreToolUse")
	skipPost := flag.Bool("skip-post", false, "do not fire PostToolUse")
	flag.Parse()

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Getwd:", err)
		os.Exit(2)
	}
	// Match production: this is exactly the call internal/app/app.go
	// makes during real startup. If viper changes or Config.Hooks moves,
	// this driver fails the same way the real binary would.
	if _, err := config.Load(cwd, false); err != nil {
		fmt.Fprintln(os.Stderr, "config.Load:", err)
		os.Exit(2)
	}
	cfg := config.Get()
	if cfg == nil {
		fmt.Fprintln(os.Stderr, "config.Get returned nil")
		os.Exit(2)
	}
	reg := hooks.NewRegistry(func() map[string][]hooks.MatcherGroup {
		c := config.Get()
		if c == nil {
			return nil
		}
		return c.Hooks
	}, cwd)

	type result struct {
		Pre  *hooks.PreToolDecision  `json:"pre,omitempty"`
		Post *hooks.PostToolDecision `json:"post,omitempty"`
		// HooksKeys reports the literal keys present in the loaded
		// config after viper.Unmarshal — invaluable for diagnosing why a
		// hook never fired (e.g. viper lowercased the key and the
		// matcher group is filed under "pretooluse" instead of
		// "PreToolUse"). The script's assertions don't rely on this
		// field, but the human reading the script's output does.
		HooksKeys []string `json:"hooks_keys"`
	}
	r := result{HooksKeys: keysOf(cfg.Hooks)}

	input := map[string]any{"command": *cmd}
	if !*skipPre {
		d := reg.RunPreTool(context.Background(), "e2e-session", cwd, *tool, input)
		r.Pre = &d
	}
	if !*skipPost {
		d := reg.RunPostTool(context.Background(), "e2e-session", cwd, *tool, input, *output)
		r.Post = &d
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(2)
	}
}

func keysOf(m map[string][]hooks.MatcherGroup) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

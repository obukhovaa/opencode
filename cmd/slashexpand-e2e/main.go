// slashexpand-e2e drives non-interactive slash-command expansion end-to-end
// and emits a JSON result for scripts/test/slash_expansion.sh to assert on.
//
// It runs the REAL pipeline the `--prompt` paths use — config.Load → project
// skill discovery (.opencode/skills/**/SKILL.md) → custom command discovery
// (.opencode/commands/*.md) → slashcmd.Expand — inside the sandbox the script
// builds as the working directory. That is the part unit tests cannot cover:
// they hand the expander a hand-built registry, whereas the failure mode worth
// guarding is a `/skill:x` in a prompt that discovery never finds.
//
// Usage: slashexpand-e2e -prompt '<text>'   (run with cwd inside the sandbox)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/format"
	"github.com/opencode-ai/opencode/internal/slashcmd"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
)

type result struct {
	OK     bool   `json:"ok"`
	Prompt string `json:"prompt"`
	Error  string `json:"error,omitempty"`
}

func main() {
	prompt := flag.String("prompt", "", "prompt text to expand")
	flag.Parse()

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "getwd:", err)
		os.Exit(2)
	}
	if _, err := config.Load(cwd, false); err != nil {
		fmt.Fprintln(os.Stderr, "config load:", err)
		os.Exit(2)
	}

	expansion, expandErr := slashcmd.Expand(*prompt, dialog.CommandRegistry(), slashcmd.ExpandOptions{
		SessionID:   "sess-e2e",
		Interactive: false,
		ShellExpand: func(content string) string {
			return format.ExpandShellMarkup(context.Background(), content, config.WorkingDirectory())
		},
	})

	res := result{OK: expandErr == nil, Prompt: expansion.Prompt}
	if expandErr != nil {
		res.Error = expandErr.Error()
	}
	out, _ := json.Marshal(res)
	fmt.Println(string(out))
}

// Command shell-isolation-e2e is a black-box driver for the persistent
// shell's terminal isolation. It exists so scripts/test/shell_isolation.sh
// can assert the invariant end to end, under a REAL pty, without needing an
// LLM or a running serve instance.
//
// The invariant is the one that makes the TUI incorruptible: the persistent
// shell runs in its own session, so /dev/tty cannot resolve to the terminal
// opencode renders on. Unit tests cover it too, but they run under `go test`,
// which usually has no controlling terminal — in that environment the check
// passes vacuously. Only a driver launched under a pty proves the real thing,
// which is why this is an e2e rather than another unit test.
//
// The driver:
//
//  1. Verifies it was itself started with a controlling terminal (otherwise
//     there is nothing to leak and the test would be meaningless).
//  2. Runs commands through the same shell.GetPersistentShell the bash tool
//     and the TUI's `!` mode use.
//  3. Prints the results as JSON on stdout.
//
// Usage: invoked from scripts/test/shell_isolation.sh under `script`, with
// cwd set to a sandbox directory.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools/shell"
)

type result struct {
	// HasControllingTerminal reports whether the DRIVER has a tty. When false
	// the tty assertions below prove nothing and the script must skip them.
	HasControllingTerminal bool `json:"has_controlling_terminal"`

	TTYWriteExitCode int    `json:"tty_write_exit_code"`
	TTYWriteStderr   string `json:"tty_write_stderr"`

	IsATTYExitCode int `json:"isatty_exit_code"`

	GitTerminalPrompt string `json:"git_terminal_prompt"`

	EchoStdout   string `json:"echo_stdout"`
	EchoExitCode int    `json:"echo_exit_code"`

	CwdAfterCd string `json:"cwd_after_cd"`

	Classified map[string]bool `json:"classified"`
}

func main() {
	timeout := flag.Int("timeout", 20000, "per-command timeout in milliseconds")
	flag.Parse()

	cwd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	if _, err := config.Load(cwd, false); err != nil {
		fail(fmt.Errorf("config.Load: %w", err))
	}

	var out result

	// Can the driver itself reach a terminal? If not, an isolated shell failing
	// to reach one proves nothing.
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		out.HasControllingTerminal = true
		f.Close()
	}

	sh := shell.GetPersistentShell(cwd)
	if sh == nil {
		fail(fmt.Errorf("failed to start persistent shell"))
	}
	defer sh.Close()

	run := func(command string) (string, string, int) {
		stdout, stderr, code, _, err := sh.Exec(context.Background(), command, *timeout)
		if err != nil {
			stderr += "\n" + err.Error()
		}
		return stdout, stderr, code
	}

	// The load-bearing assertion: a command must not be able to write to the
	// terminal the driver is attached to.
	_, stderr, code := run("echo LEAKED-TO-TERMINAL > /dev/tty")
	out.TTYWriteExitCode = code
	out.TTYWriteStderr = strings.TrimSpace(stderr)

	// A second angle on the same property.
	_, _, out.IsATTYExitCode = run("test -t 0 && test -t 1")

	stdout, _, _ := run("echo $GIT_TERMINAL_PROMPT")
	out.GitTerminalPrompt = strings.TrimSpace(stdout)

	// Isolation must be invisible to an ordinary command.
	out.EchoStdout, _, out.EchoExitCode = run("echo hello-from-shell")
	out.EchoStdout = strings.TrimSpace(out.EchoStdout)

	if _, _, code := run("cd /"); code == 0 {
		out.CwdAfterCd = sh.Cwd()
	}

	// The classifier the TUI uses to decide which commands get the terminal.
	out.Classified = map[string]bool{
		"sudo -v":                shell.ClassifyInteractive("sudo -v", nil),
		"ls -la":                 shell.ClassifyInteractive("ls -la", nil),
		"docker ps":              shell.ClassifyInteractive("docker ps", nil),
		"docker exec -it web sh": shell.ClassifyInteractive("docker exec -it web sh", nil),
		"my-tool (configured)":   shell.ClassifyInteractive("my-tool run", []string{"my-tool"}),
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "shell-isolation-e2e: %v\n", err)
	os.Exit(1)
}

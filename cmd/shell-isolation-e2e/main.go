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

	// SessionIsolated compares the shell's session id with the driver's. This
	// is the isolation invariant itself, and unlike the /dev/tty probes it is
	// assertable with or without a terminal — so the script always has a real
	// check to make and can never degrade to an all-skip green run.
	SessionIsolated bool `json:"session_isolated"`

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
	outPath := flag.String("out", "", "write the JSON result here instead of stdout")
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

	out.SessionIsolated = sessionIsolated(sh.ShellPID())

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
	// The last entry reads the list from the loaded config rather than passing
	// a literal, so this exercises the `shell.interactive` plumbing end to end
	// — config file → viper → Config → classifier — which is the part a unit
	// test cannot cover.
	var configured []string
	if cfg := config.Get(); cfg != nil {
		configured = cfg.Shell.Interactive
	}
	out.Classified = map[string]bool{
		"sudo -v":                shell.ClassifyInteractive("sudo -v", nil),
		"ls -la":                 shell.ClassifyInteractive("ls -la", nil),
		"docker ps":              shell.ClassifyInteractive("docker ps", nil),
		"docker build -t img .":  shell.ClassifyInteractive("docker build -t img .", nil),
		"docker exec -it web sh": shell.ClassifyInteractive("docker exec -it web sh", nil),
		"configured-tool":        shell.ClassifyInteractive("configured-tool run", configured),
	}

	// Write to a file when asked. Under `script(1)` — which the harness needs
	// to obtain a real pty — stdout carries the terminal's own control bytes
	// around the payload, so a caller that parsed stdout would be parsing a
	// terminal transcript. A file sidesteps that entirely.
	sink := os.Stdout
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			fail(err)
		}
		defer f.Close()
		sink = f
	}

	enc := json.NewEncoder(sink)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "shell-isolation-e2e: %v\n", err)
	os.Exit(1)
}

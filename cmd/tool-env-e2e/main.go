// Command tool-env-e2e is a black-box driver for the tool environment deny
// list. It exists so scripts/test/tool_env.sh can assert, without an LLM or a
// running server, that a command run through the shell the bash tool uses
// cannot see a denied variable, through the real config file → viper →
// toolenv path, and (on Linux) cannot read it back from opencode's own
// /proc/<pid>/environ either.
//
// Usage: invoked from scripts/test/tool_env.sh with cwd set to a sandbox
// directory holding the .opencode.json under test.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools/shell"
	"github.com/opencode-ai/opencode/internal/toolenv"
)

type result struct {
	// Env is the output of `env` run through the persistent shell.
	Env string `json:"env"`
	// Hardened reports whether the driver marked itself non-dumpable, the
	// same call every opencode entrypoint makes after config load.
	Hardened bool `json:"hardened"`
	// ProcChecked is false where there is no /proc (macOS), so the script
	// skips the read-back assertion instead of passing it vacuously.
	ProcChecked bool `json:"proc_checked"`
	// ProcEnvironReadable reports whether `cat /proc/$PPID/environ` from the
	// shell, whose parent is this driver, succeeded.
	ProcEnvironReadable bool   `json:"proc_environ_readable"`
	ProcEnviron         string `json:"proc_environ"`
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	if _, err := config.Load(cwd, false); err != nil {
		fail(fmt.Errorf("config.Load: %w", err))
	}
	var out result
	if _, hardened, err := toolenv.HardenProcess(); err != nil {
		fail(fmt.Errorf("HardenProcess: %w", err))
	} else {
		out.Hardened = hardened
	}

	sh := shell.GetPersistentShell(cwd)
	if sh == nil {
		fail(fmt.Errorf("failed to start persistent shell"))
	}
	defer sh.Close()

	stdout, stderr, code, _, err := sh.Exec(context.Background(), "env", 20000)
	if err != nil || code != 0 {
		fail(fmt.Errorf("env: exit %d, err %v, stderr %q", code, err, stderr))
	}
	out.Env = stdout

	if runtime.GOOS == "linux" {
		out.ProcChecked = true
		// tr keeps the NUL-separated block greppable in JSON. No `exit`: it
		// would end the persistent shell's session. A failed read prints
		// nothing on stdout, which is what "readable" keys on.
		stdout, _, _, _, _ := sh.Exec(context.Background(), `cat /proc/$PPID/environ 2>/dev/null | tr '\0' '\n'`, 20000)
		out.ProcEnvironReadable = strings.TrimSpace(stdout) != ""
		out.ProcEnviron = stdout
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "tool-env-e2e: %v\n", err)
	os.Exit(1)
}

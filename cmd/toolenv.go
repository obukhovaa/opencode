package cmd

import (
	"strings"

	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/toolenv"
)

// hardenToolEnv runs toolenv.HardenProcess once config is loaded, so the
// configured deny entries count. Only variable names are logged, never
// values. A failure is logged and otherwise ignored: the child-environment
// filter still holds without it.
func hardenToolEnv() {
	names, hardened, err := toolenv.HardenProcess()
	switch {
	case err != nil:
		logging.Warn("could not hide opencode's environment from child processes", "denied", strings.Join(names, ","), "error", err)
	case hardened:
		logging.Info("process marked non-dumpable: its environment holds denied variables", "denied", strings.Join(names, ","))
	}
}

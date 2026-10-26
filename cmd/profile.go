package cmd

import (
	"os"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/spf13/cobra"
)

// applyProfileFlag maps --profile onto config.ProfileEnv before config.Load.
// Registered as a persistent flag on the root command, so every subcommand
// inherits it; each RunE that loads config calls this first.
func applyProfileFlag(cmd *cobra.Command) {
	if f := cmd.Flags().Lookup("profile"); f != nil && f.Changed {
		_ = os.Setenv(config.ProfileEnv, f.Value.String())
	}
}

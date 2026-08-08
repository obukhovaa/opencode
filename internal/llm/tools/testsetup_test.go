package tools

import (
	"os"

	"github.com/opencode-ai/opencode/internal/config"
)

// The whole tools test binary depends on a loaded config: bash/ls tool
// descriptions and edit/write path resolution read the working directory
// from it. This package-level init used to live in edit_test.go, where the
// dependency was invisible to the other test files that relied on it.
// Tests MUST NOT config.Reset() — they share this package-lifetime config.
func init() {
	wd, _ := os.Getwd()
	config.Load(wd, false)
}

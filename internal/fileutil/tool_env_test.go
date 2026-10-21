package fileutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/toolenv"
)

func TestSearchHelpers_DeniedEnvironment(t *testing.T) {
	t.Setenv("OPENCODE_SERVER_PASSWORD", "api-secret")
	t.Setenv("FILEUTIL_TEST_DENIED", "deployment-secret")
	t.Setenv(toolenv.EnvVar, "FILEUTIL_TEST_DENIED")
	t.Setenv("FILEUTIL_TEST_KEPT", "kept")
	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(probe, []byte(`#!/bin/sh
printf '%s|%s|%s' "${OPENCODE_SERVER_PASSWORD-unset}" "${FILEUTIL_TEST_DENIED-unset}" "$FILEUTIL_TEST_KEPT"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	previousRg, previousFzf := rgPath, fzfPath
	rgPath, fzfPath = probe, probe
	t.Cleanup(func() { rgPath, fzfPath = previousRg, previousFzf })
	for _, tt := range []struct {
		name string
		cmd  *exec.Cmd
	}{
		{"ripgrep", GetRgCmd(context.Background(), "*.go")},
		{"fzf", GetFzfCmd("needle")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, err := tt.cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			if got := string(out); got != "unset|unset|kept" {
				t.Fatalf("helper environment = %q, want unset|unset|kept", got)
			}
		})
	}
}

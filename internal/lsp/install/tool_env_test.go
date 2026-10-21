package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/toolenv"
)

func TestInstallerHelpers_DeniedEnvironment(t *testing.T) {
	t.Setenv("OPENCODE_SERVER_PASSWORD", "api-secret")
	t.Setenv("INSTALL_TEST_DENIED", "deployment-secret")
	t.Setenv(toolenv.EnvVar, "INSTALL_TEST_DENIED")
	t.Setenv("INSTALL_TEST_KEPT", "kept")
	dir := t.TempDir()
	report := filepath.Join(dir, "report")
	t.Setenv("INSTALL_TEST_REPORT", report)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, name := range []string{"language-server", "tar", "unzip"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`#!/bin/sh
printf '%s|%s|%s' "${OPENCODE_SERVER_PASSWORD-unset}" "${INSTALL_TEST_DENIED-unset}" "$INSTALL_TEST_KEPT" > "$INSTALL_TEST_REPORT"
printf 'test-version\n'
`), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name string
		run  func() error
	}{
		{"version probe", func() error { logServerVersion(filepath.Join(dir, "language-server"), "test"); return nil }},
		{"tar extractor", func() error { return extractTarGz("archive.tar.gz", dir, "server") }},
		{"zip extractor", func() error { return extractZip("archive.zip", dir, "server") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(report, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := tt.run(); err != nil {
				t.Fatal(err)
			}
			out, err := os.ReadFile(report)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(out)); got != "unset|unset|kept" {
				t.Fatalf("helper environment = %q, want unset|unset|kept", got)
			}
		})
	}
}

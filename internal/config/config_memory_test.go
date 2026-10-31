package config

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestMemoryConfigViperRoundTrip(t *testing.T) {
	t.Setenv(MemoryHomeEnv, "")
	v := viper.New()
	v.SetConfigType("json")
	if err := v.ReadConfig(bytes.NewBufferString(`{"memory":{"enabled":true,"directory":"shared-memory","projectID":"git:example.com/Team/Repo"}}`)); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatal(err)
	}
	cfg.WorkingDir = t.TempDir()
	dir, err := cfg.MemoryDirectory()
	if err != nil || cfg.Memory == nil || !cfg.Memory.Enabled || cfg.Memory.ProjectID != "git:example.com/Team/Repo" || dir != filepath.Join(cfg.WorkingDir, "shared-memory") {
		t.Fatalf("%+v %q %v", cfg.Memory, dir, err)
	}
	if (&Config{}).Memory != nil {
		t.Fatal("memory unexpectedly enabled")
	}
}

func TestMemoryDirectoryHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(MemoryHomeEnv, "")
	for _, tc := range []struct{ directory, want string }{
		{"", filepath.Join(home, ".agents", "memory")},
		{"~", home},
		{"~/shared", filepath.Join(home, "shared")},
	} {
		cfg := Config{WorkingDir: t.TempDir(), Memory: &MemoryConfig{Directory: tc.directory}}
		if got, err := cfg.MemoryDirectory(); err != nil || got != tc.want {
			t.Errorf("%q: got %q, %v; want %q", tc.directory, got, err, tc.want)
		}
	}
}

func TestMemoryDirectoryEnvSelectsStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(MemoryHomeEnv, "~/.agents/memory-work")
	for _, directory := range []string{"", "~/.agents/memory", "/shared/memory"} {
		cfg := Config{WorkingDir: t.TempDir(), Memory: &MemoryConfig{Directory: directory}}
		want := filepath.Join(home, ".agents", "memory-work")
		if got, err := cfg.MemoryDirectory(); err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", directory, got, err, want)
		}
	}
	t.Setenv(MemoryHomeEnv, "")
	cfg := Config{WorkingDir: t.TempDir(), Memory: &MemoryConfig{Directory: "/shared/memory"}}
	if got, err := cfg.MemoryDirectory(); err != nil || got != "/shared/memory" {
		t.Errorf("empty %s: got %q, %v", MemoryHomeEnv, got, err)
	}
}

func TestMemoryDirectoryFailsClosedWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv(MemoryHomeEnv, "")
	for _, directory := range []string{"", "~/shared"} {
		cfg := Config{WorkingDir: t.TempDir(), Memory: &MemoryConfig{Directory: directory}}
		if got, err := cfg.MemoryDirectory(); err == nil {
			t.Errorf("%q resolved to %q without a home directory", directory, got)
		}
	}
}

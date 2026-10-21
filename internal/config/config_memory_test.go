package config

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestMemoryConfigViperRoundTrip(t *testing.T) {
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
	if cfg.Memory == nil || !cfg.Memory.Enabled || cfg.Memory.ProjectID != "git:example.com/Team/Repo" || cfg.MemoryDirectory() != filepath.Join(cfg.WorkingDir, "shared-memory") {
		t.Fatalf("%+v", cfg.Memory)
	}
	if (&Config{}).Memory != nil {
		t.Fatal("memory unexpectedly enabled")
	}
}

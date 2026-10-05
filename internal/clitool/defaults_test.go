package clitool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
)

// clearLimitEnv blanks the OPENCODE_CLI_TOOLS_* knobs for the test so the
// developer's shell cannot steer the resolution under test.
func clearLimitEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{TimeoutEnv, MaxTimeoutEnv, MaxOutputBytesEnv} {
		t.Setenv(k, "")
	}
}

func TestResolveDefaults_Precedence(t *testing.T) {
	cases := []struct {
		name  string
		cfg   *config.CLIToolsConfig
		env   map[string]string
		want  Defaults
		warns int
	}{
		{"builtin", nil, nil, BuiltinDefaults(), 0},
		{"config", &config.CLIToolsConfig{Timeout: "30s", MaxTimeout: "5m", MaxOutputBytes: 1024}, nil,
			Defaults{Timeout: 30 * time.Second, MaxTimeout: 5 * time.Minute, MaxOutputBytes: 1024,
				TimeoutSource: SourceConfig, MaxTimeoutSource: SourceConfig, MaxOutputBytesSource: SourceConfig}, 0},
		{"config seconds and unbounded", &config.CLIToolsConfig{Timeout: "45", MaxOutputBytes: -7}, nil,
			Defaults{Timeout: 45 * time.Second, MaxTimeout: DefaultMaxTimeout, MaxOutputBytes: -1,
				TimeoutSource: SourceConfig, MaxTimeoutSource: SourceBuiltin, MaxOutputBytesSource: SourceConfig}, 0},
		{"env over config", &config.CLIToolsConfig{Timeout: "30s", MaxOutputBytes: 1024},
			map[string]string{TimeoutEnv: "1m", MaxTimeoutEnv: "20m", MaxOutputBytesEnv: "-1"},
			Defaults{Timeout: time.Minute, MaxTimeout: 20 * time.Minute, MaxOutputBytes: -1,
				TimeoutSource: SourceEnv, MaxTimeoutSource: SourceEnv, MaxOutputBytesSource: SourceEnv}, 0},
		{"invalid env falls back to config", &config.CLIToolsConfig{Timeout: "30s"},
			map[string]string{TimeoutEnv: "soon", MaxOutputBytesEnv: "lots"},
			Defaults{Timeout: 30 * time.Second, MaxTimeout: DefaultMaxTimeout, MaxOutputBytes: DefaultMaxOutputBytes,
				TimeoutSource: SourceConfig, MaxTimeoutSource: SourceBuiltin, MaxOutputBytesSource: SourceBuiltin}, 2},
		{"invalid config falls back to builtin", &config.CLIToolsConfig{Timeout: "0", MaxTimeout: "later"}, nil,
			BuiltinDefaults(), 2},
		{"max timeout raised to timeout", &config.CLIToolsConfig{Timeout: "15m"}, nil,
			Defaults{Timeout: 15 * time.Minute, MaxTimeout: 15 * time.Minute, MaxOutputBytes: DefaultMaxOutputBytes,
				TimeoutSource: SourceConfig, MaxTimeoutSource: SourceConfig, MaxOutputBytesSource: SourceBuiltin}, 1},
		{"env zero cap means unset", nil, map[string]string{MaxOutputBytesEnv: "0"}, BuiltinDefaults(), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearLimitEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, warns := ResolveDefaults(tc.cfg)
			if got != tc.want {
				t.Errorf("defaults = %+v\nwant       %+v", got, tc.want)
			}
			if len(warns) != tc.warns {
				t.Errorf("warnings = %d %q, want %d", len(warns), warns, tc.warns)
			}
		})
	}
}

// A manifest's own fields always win; the inherited values only fill what
// it leaves out, and a global knob can never make a valid manifest fail.
func TestParseWithDefaults_ManifestFieldsWin(t *testing.T) {
	wd := t.TempDir()
	d := Defaults{Timeout: 3 * time.Second, MaxTimeout: 30 * time.Second, MaxOutputBytes: 100}
	parse := func(body string) *Manifest {
		t.Helper()
		m, err := ParseWithDefaults([]byte(body), filepath.Join(wd, "say.yaml"), wd, d)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return m
	}

	m := parse(echoManifest)
	if m.TimeoutDefault() != 3*time.Second || m.TimeoutMax() != 30*time.Second || m.MaxOutput() != 100 {
		t.Errorf("inherited limits: %v %v %d", m.TimeoutDefault(), m.TimeoutMax(), m.MaxOutput())
	}
	if m.Defaults().MaxOutputBytes != 100 || m.Defaults().TimeoutSource != SourceBuiltin {
		t.Errorf("Defaults() = %+v", m.Defaults())
	}
	if props, _ := m.InputSchema(); !strings.Contains(props["timeout"].(map[string]any)["description"].(string), "default 3, max 30") {
		t.Errorf("schema should advertise the effective limits: %v", props["timeout"])
	}

	m = parse(echoManifest + "timeout: 10s\nmaxTimeout: 1m\nmaxOutputBytes: 5000\n")
	if m.TimeoutDefault() != 10*time.Second || m.TimeoutMax() != time.Minute || m.MaxOutput() != 5000 {
		t.Errorf("explicit fields must win: %v %v %d", m.TimeoutDefault(), m.TimeoutMax(), m.MaxOutput())
	}

	m = parse(echoManifest + "timeout: 2m\n")
	if m.TimeoutDefault() != 2*time.Minute || m.TimeoutMax() != 2*time.Minute {
		t.Errorf("a manifest timeout above the inherited cap must raise the cap: %v %v", m.TimeoutDefault(), m.TimeoutMax())
	}

	m = parse(echoManifest + "maxTimeout: 1s\n")
	if m.TimeoutDefault() != time.Second || m.TimeoutMax() != time.Second {
		t.Errorf("a manifest cap below the inherited timeout must lower the timeout: %v %v", m.TimeoutDefault(), m.TimeoutMax())
	}

	if _, err := ParseWithDefaults([]byte(echoManifest+"timeout: 5s\nmaxTimeout: 1s\n"), filepath.Join(wd, "say.yaml"), wd, d); err == nil || !strings.Contains(err.Error(), "maxTimeout") {
		t.Errorf("both explicit and inconsistent must still fail: %v", err)
	}

	m, err := ParseWithDefaults([]byte(echoManifest), filepath.Join(wd, "say.yaml"), wd, Defaults{MaxOutputBytes: -1})
	if err != nil || m.MaxOutput() != -1 || m.TimeoutDefault() != DefaultTimeout || m.TimeoutMax() != DefaultMaxTimeout {
		t.Errorf("partial Defaults must be normalized: %v %+v", err, m)
	}
}

// Discover resolves the knobs once and every manifest it loads inherits
// them — the single point both the native toolset and `tools serve` go
// through.
func TestDiscover_AppliesSharedLimits(t *testing.T) {
	isolateHome(t)
	wd := t.TempDir()
	if err := os.Mkdir(filepath.Join(wd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, wd, ".agents/tools/say.yaml", echoManifest)
	writeManifest(t, wd, ".agents/tools/own.yaml", "name: own\ndescription: d\ncommand: /bin/echo\ntimeout: 7s\nmaxOutputBytes: 9\n")
	cfg := &config.CLIToolsConfig{Timeout: "4s", MaxOutputBytes: 2048}

	set := Discover(context.Background(), wd, cfg)
	byName := map[string]*Manifest{}
	for _, m := range set.Manifests {
		byName[m.Name] = m
	}
	if len(byName) != 2 {
		t.Fatalf("tools = %d (%+v)", len(byName), set.Diagnostics)
	}
	if set.Defaults.Timeout != 4*time.Second || set.Defaults.TimeoutSource != SourceConfig ||
		set.Defaults.MaxOutputBytes != 2048 || set.Defaults.MaxOutputBytesSource != SourceConfig ||
		set.Defaults.MaxTimeoutSource != SourceBuiltin || len(set.Warnings) != 0 {
		t.Errorf("Set.Defaults = %+v warnings=%q", set.Defaults, set.Warnings)
	}
	if say := byName["say"]; say.TimeoutDefault() != 4*time.Second || say.MaxOutput() != 2048 {
		t.Errorf("say must inherit the config knobs: %v %d", say.TimeoutDefault(), say.MaxOutput())
	}
	if own := byName["own"]; own.TimeoutDefault() != 7*time.Second || own.MaxOutput() != 9 {
		t.Errorf("own must keep its fields: %v %d", own.TimeoutDefault(), own.MaxOutput())
	}

	// The environment overrides the config block; a bad value is a warning
	// and the next layer applies — never a failed manifest.
	t.Setenv(TimeoutEnv, "9s")
	t.Setenv(MaxOutputBytesEnv, "many")
	set = Discover(context.Background(), wd, cfg)
	if set.Defaults.Timeout != 9*time.Second || set.Defaults.TimeoutSource != SourceEnv || set.Defaults.MaxOutputBytes != 2048 {
		t.Errorf("env layer: %+v", set.Defaults)
	}
	if len(set.Warnings) != 1 || !strings.Contains(set.Warnings[0], MaxOutputBytesEnv) {
		t.Errorf("warnings = %q", set.Warnings)
	}
	if len(set.Manifests) != 2 || set.Manifests[1].TimeoutDefault() != 9*time.Second {
		t.Errorf("manifests after env override: %+v", set.Manifests)
	}
}

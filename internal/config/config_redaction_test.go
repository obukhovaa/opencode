package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// TestConfig_RedactionRulesSurviveViper is the test CLAUDE.md mandates for any
// config carrying user-supplied names. Viper case-folds map KEYS, so redaction
// rules are modelled as an array of objects — every operator string sits in a
// value position. A regression to a map shape would lowercase the rule name
// and, far worse, corrupt a case-sensitive pattern ("[A-Z]" -> "[a-z]"),
// silently turning a working filter into one that matches nothing.
//
// A plain json.Unmarshal test passes either way, which is exactly the trap.
func TestConfig_RedactionRulesSurviveViper(t *testing.T) {
	dir := t.TempDir()
	body := `{
	  "telemetry": {
	    "redaction": {
	      "enabled": true,
	      "mode": "fingerprint",
	      "disableBuiltins": ["generic-sk"],
	      "rules": [
	        {"name": "PianoInternalID", "pattern": "PI-[A-Z0-9]{12}", "group": 0},
	        {"name": "KeyValue", "pattern": "MYKEY=(\\S+)", "group": 1, "replacement": "[GONE]"}
	      ],
	      "allowlist": ["AKIAIOSFODNN7EXAMPLE"]
	    }
	  }
	}`
	if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	v := viper.New()
	v.SetConfigName(".opencode")
	v.SetConfigType("json")
	v.AddConfigPath(dir)
	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("read: %v", err)
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	rc := cfg.Telemetry.Redaction
	if rc == nil {
		t.Fatal("telemetry.redaction did not survive the loader")
	}
	if len(rc.Rules) != 2 {
		t.Fatalf("want 2 rules, got %d: %+v", len(rc.Rules), rc.Rules)
	}

	// Names keep their case.
	if rc.Rules[0].Name != "PianoInternalID" {
		t.Errorf("rule name case-folded by viper: got %q want %q", rc.Rules[0].Name, "PianoInternalID")
	}
	// Patterns keep their case — the assertion that actually matters.
	if rc.Rules[0].Pattern != "PI-[A-Z0-9]{12}" {
		t.Errorf("rule pattern mangled by viper: got %q want %q", rc.Rules[0].Pattern, "PI-[A-Z0-9]{12}")
	}
	if rc.Rules[1].Pattern != `MYKEY=(\S+)` {
		t.Errorf("rule pattern mangled by viper: got %q want %q", rc.Rules[1].Pattern, `MYKEY=(\S+)`)
	}
	if rc.Rules[1].Group != 1 {
		t.Errorf("rule group lost: got %d want 1", rc.Rules[1].Group)
	}
	if rc.Rules[1].Replacement != "[GONE]" {
		t.Errorf("rule replacement lost: got %q", rc.Rules[1].Replacement)
	}
	if len(rc.Allowlist) != 1 || rc.Allowlist[0] != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("allowlist mangled: %v", rc.Allowlist)
	}
	if len(rc.DisableBuiltins) != 1 || rc.DisableBuiltins[0] != "generic-sk" {
		t.Errorf("disableBuiltins mangled: %v", rc.DisableBuiltins)
	}
	if !rc.IsEnabled() {
		t.Error("explicit enabled:true read as disabled")
	}
}

func TestRedactionConfig_EnabledDefaults(t *testing.T) {
	tests := []struct {
		name string
		cfg  *RedactionConfig
		want bool
	}{
		{"nil config defaults on", nil, true},
		{"unset enabled defaults on", &RedactionConfig{}, true},
		{"explicit true", &RedactionConfig{Enabled: boolPtr(true)}, true},
		{"explicit false is honoured", &RedactionConfig{Enabled: boolPtr(false)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsEnabled(); got != tc.want {
				t.Errorf("IsEnabled() = %v want %v", got, tc.want)
			}
		})
	}
}

// An explicit false must survive the loader, not be flattened into "unset".
func TestConfig_RedactionDisabledSurvivesViper(t *testing.T) {
	dir := t.TempDir()
	body := `{"telemetry":{"redaction":{"enabled":false}}}`
	if err := os.WriteFile(filepath.Join(dir, ".opencode.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	v := viper.New()
	v.SetConfigName(".opencode")
	v.SetConfigType("json")
	v.AddConfigPath(dir)
	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("read: %v", err)
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Telemetry.Redaction.IsEnabled() {
		t.Error("explicit enabled:false was lost through the loader")
	}
}

func TestValidateRedactionConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *RedactionConfig
		wantErr string
	}{
		{"nil is fine", nil, ""},
		{"empty is fine", &RedactionConfig{}, ""},
		{"valid mode", &RedactionConfig{Mode: "strict"}, ""},
		{"bad mode", &RedactionConfig{Mode: "shred"}, "unsupported mode"},
		{"unknown builtin", &RedactionConfig{DisableBuiltins: []string{"nope"}}, "unknown detector"},
		{"known builtin", &RedactionConfig{DisableBuiltins: []string{"gitlab-pat"}}, ""},
		{"valid rule", &RedactionConfig{Rules: []RedactionRule{{Name: "a", Pattern: `x\d+`}}}, ""},
		{"bad regex", &RedactionConfig{Rules: []RedactionRule{{Name: "a", Pattern: "[unclosed"}}}, "invalid pattern"},
		{"lookahead named explicitly", &RedactionConfig{Rules: []RedactionRule{{Name: "a", Pattern: "x(?=y)"}}}, "lookahead/lookbehind"},
		{"group out of range", &RedactionConfig{Rules: []RedactionRule{{Name: "a", Pattern: `x(\d)`, Group: 2}}}, "does not exist"},
		{"unnamed rule", &RedactionConfig{Rules: []RedactionRule{{Pattern: "x"}}}, "rule name is required"},
		{"duplicate names", &RedactionConfig{Rules: []RedactionRule{
			{Name: "a", Pattern: "x"}, {Name: "a", Pattern: "y"},
		}}, "duplicate rule name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRedactionConfig(tc.cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

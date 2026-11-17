package config

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestMCPCompositionViperRoundTrip(t *testing.T) {
	for _, body := range []string{`{}`, `{"mcpComposition":{"readTools":[]}}`, `{"mcpComposition":{"readTools":["GitLab_GetMR","db.read-project"]}}`} {
		v := viper.New()
		v.SetConfigType("json")
		if err := v.ReadConfig(bytes.NewBufferString(body)); err != nil {
			t.Fatal(err)
		}
		var c Config
		if err := v.Unmarshal(&c); err != nil {
			t.Fatal(err)
		}
		if body == "{}" {
			if c.MCPComposition != nil {
				t.Fatal("absent composition must stay disabled")
			}
			continue
		}
		if c.MCPComposition == nil {
			t.Fatal("composition config lost in loader")
		}
		if strings.Contains(body, "GitLab_GetMR") && !reflect.DeepEqual(c.MCPComposition.ReadTools, []string{"GitLab_GetMR", "db.read-project"}) {
			t.Fatalf("exact tool names changed: %v", c.MCPComposition.ReadTools)
		}
		if err := validateMCPComposition(c.MCPComposition); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMCPCompositionExactNames(t *testing.T) {
	for _, names := range [][]string{{""}, {"srv_*"}, {"srv_?"}, {"srv_[read]"}, {"srv_{read}"}, {"srv read"}, {"srv\nread"}, {"srv\x00read"}, {strings.Repeat("x", 257)}, {"srv_read", "srv_read"}} {
		if err := validateMCPComposition(&MCPCompositionConfig{ReadTools: names}); err == nil {
			t.Fatalf("invalid endpoint list accepted: %q", names)
		}
	}
	for _, c := range []*MCPCompositionConfig{nil, {}, {ReadTools: []string{"srv_read", "srv_Read", strings.Repeat("x", 256)}}} {
		if err := validateMCPComposition(c); err != nil {
			t.Fatal(err)
		}
	}
}

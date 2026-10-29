package config

import (
	"fmt"

	"github.com/spf13/viper"

	"github.com/opencode-ai/opencode/internal/toolenv"
)

// LoadTools reads only the settings used by tools list/serve, using the same
// profile/global/project precedence as Load. It neither initializes the full
// application config nor expands or validates unrelated runtime settings.
// tools list --agent must use Load because it needs the agent registry.
func LoadTools(workingDir string) (*Config, error) {
	decoder := scopedConfigDecoderOption(map[string]bool{"clitools": true, "toolenv": true})
	v := viper.NewWithOptions(decoder)
	configureViperInstance(v)
	if err := readConfig(v.ReadInConfig()); err != nil {
		return nil, err
	}
	if err := mergeLocalConfigInto(v, workingDir, decoder); err != nil {
		return nil, err
	}
	c := &Config{WorkingDir: workingDir}
	if err := v.Unmarshal(c); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tools config: check configuration field types")
	}
	toolenv.SetConfigured(c.ToolEnv.Deny)
	return c, nil
}

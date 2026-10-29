package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Expand parsed values, before Viper case-folds keys or evaluates provider
// defaults. Replacing raw JSON would let quotes/newlines in a secret alter
// the document. Installing a decoder also preserves Viper's precedence and
// ConfigFileUsed (needed by writeback), unlike Set-based overrides.
type configDecoderRegistry struct {
	viper.DecoderRegistry
	fields map[string]bool
}

func configDecoderOption() viper.Option {
	return scopedConfigDecoderOption(nil)
}

func scopedConfigDecoderOption(fields map[string]bool) viper.Option {
	return viper.WithDecoderRegistry(configDecoderRegistry{viper.NewCodecRegistry(), fields})
}

func (r configDecoderRegistry) Decoder(format string) (viper.Decoder, error) {
	if strings.EqualFold(format, "json") {
		return envJSONDecoder{fields: r.fields}, nil
	}
	return r.DecoderRegistry.Decoder(format)
}

type envJSONDecoder struct {
	// nil loads the full config. A scope discards unrelated sections before
	// expansion, so their credentials never become startup dependencies.
	fields map[string]bool
}

func (d envJSONDecoder) Decode(data []byte, out map[string]any) error {
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	if values == nil {
		return fmt.Errorf("configuration must be a JSON object")
	}
	for key, value := range values {
		if d.fields != nil && !d.fields[strings.ToLower(key)] {
			continue
		}
		// Hook commands are shell strings run with OPENCODE_PROJECT_DIR /
		// CLAUDE_PROJECT_DIR in their own environment, so their ${VAR}
		// belongs to that shell, not to config loading: expanding here would
		// fail the load on a variable only the hook process has.
		if !strings.EqualFold(key, "hooks") {
			expanded, err := expandConfigValues(value)
			if err != nil {
				return err
			}
			value = expanded
		}
		out[key] = value
	}
	return nil
}

func expandConfigValues(value any) (any, error) {
	switch v := value.(type) {
	case string:
		return expandConfigString(v)
	case map[string]any:
		for key, child := range v {
			expanded, err := expandConfigValues(child)
			if err != nil {
				return nil, err
			}
			v[key] = expanded
		}
	case []any:
		for i, child := range v {
			expanded, err := expandConfigValues(child)
			if err != nil {
				return nil, err
			}
			v[i] = expanded
		}
	}
	return value, nil
}

func expandConfigString(value string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(value); {
		start := i
		escaped := strings.HasPrefix(value[i:], "$${")
		if escaped {
			i++
		}
		if !strings.HasPrefix(value[i:], "${") {
			out.WriteByte(value[i])
			i++
			continue
		}
		end := strings.IndexByte(value[i+2:], '}')
		if end < 0 {
			out.WriteString(value[start:])
			break
		}
		end += i + 2
		expression := value[i+2 : end]
		name, fallback, hasFallback := strings.Cut(expression, ":-")
		if escaped {
			out.WriteString(value[i : end+1])
		} else if !validEnvName(name) || strings.ContainsAny(fallback, "{}") || expression == "agent" {
			// ${agent} and dotted ${flow.step}/${env.NAME} belong to scoped
			// context resolution. Bare $NAME belongs to subprocess shells.
			out.WriteString(value[i : end+1])
		} else {
			replacement, set := os.LookupEnv(name)
			if hasFallback && (!set || replacement == "") {
				replacement = fallback
			} else if !set {
				// Never echo the value (which can contain other secrets), a
				// map key, or a fallback in diagnostics.
				return "", fmt.Errorf("configuration environment variable %s is not set", name)
			}
			out.WriteString(replacement)
		}
		i = end + 1
	}
	return out.String(), nil
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c != '_' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

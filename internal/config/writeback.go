package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-viper/mapstructure/v2"
)

// editConfigFile keeps the raw document separate from the typed callback
// view. Serializing the expanded Config would persist secrets and discard
// unknown fields. Apply only the callback's changes to the raw document.
func editConfigFile(data []byte, update func(*Config)) (*Config, map[string]any, error) {
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber() // preserve even unknown integers larger than 2^53
	if err := decoder.Decode(&raw); err != nil {
		return nil, nil, fmt.Errorf("failed to parse config file: %w", err)
	}
	if raw == nil {
		return nil, nil, fmt.Errorf("configuration must be a JSON object")
	}
	// Parse a separate tree: expansion mutates values in place.
	expanded := make(map[string]any)
	if err := (envJSONDecoder{}).Decode(data, expanded); err != nil {
		return nil, nil, fmt.Errorf("failed to parse config file: %w", err)
	}
	var fileCfg Config
	// Viper's weak scalar decoding, with JSON tags and without case-folding
	// user map keys (e.g. hook events and header names) during persistence.
	md, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result: &fileCfg, TagName: "json", WeaklyTypedInput: true,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
		),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create config decoder: %w", err)
	}
	if err := md.Decode(expanded); err != nil {
		return nil, nil, fmt.Errorf("failed to decode config file: check configuration field types")
	}
	before := configSnapshot(reflect.ValueOf(fileCfg))
	update(&fileCfg)
	after := configSnapshot(reflect.ValueOf(fileCfg))
	return &fileCfg, patchConfigValue(raw, before, after).(map[string]any), nil
}

// Include zero-valued struct fields in snapshots. json.Marshal's omitempty
// would otherwise turn an intentional true -> false edit into a deletion,
// allowing a true Viper default to win on restart.
func configSnapshot(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return configSnapshot(v.Elem())
	case reflect.Struct:
		out := make(map[string]any)
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			out[name] = configSnapshot(v.Field(i))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		out := make(map[string]any, v.Len())
		for _, key := range v.MapKeys() {
			out[key.String()] = configSnapshot(v.MapIndex(key))
		}
		return out
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil
		}
		out := make([]any, v.Len())
		for i := range out {
			out[i] = configSnapshot(v.Index(i))
		}
		return out
	default:
		return v.Interface()
	}
}

func patchConfigValue(raw, before, after any) any {
	if reflect.DeepEqual(before, after) {
		return raw
	}
	switch next := after.(type) {
	case map[string]any:
		previous, _ := before.(map[string]any)
		result, _ := raw.(map[string]any)
		if result == nil {
			result = make(map[string]any)
		}
		for key := range previous {
			if _, exists := next[key]; !exists {
				delete(result, configRawKey(result, key))
			}
		}
		for key, value := range next {
			if reflect.DeepEqual(previous[key], value) {
				continue
			}
			rawKey := key
			if _, existed := previous[key]; existed {
				rawKey = configRawKey(result, key)
			}
			result[rawKey] = patchConfigValue(result[rawKey], previous[key], value)
		}
		return result
	case []any:
		previous, _ := before.([]any)
		original, _ := raw.([]any)
		result := make([]any, len(next))
		used := make([]bool, len(previous))
		// Match unchanged entries first, so removals/reordering/appending
		// identities keep their original placeholders and unknown fields.
		matched := make([]bool, len(next))
		for i, value := range next {
			for j, old := range previous {
				if !used[j] && j < len(original) && reflect.DeepEqual(old, value) {
					result[i], matched[i], used[j] = original[j], true, true
					break
				}
			}
		}
		for i, value := range next {
			if matched[i] {
				continue
			}
			// Bridge identities can move AND change in one edit. Match their
			// stable ID before falling back to position, or removing an earlier
			// identity could materialize the remaining identity's secret.
			if id := configArrayID(value); id != "" {
				for j, old := range previous {
					if !used[j] && j < len(original) && configArrayID(old) == id {
						result[i] = patchConfigValue(original[j], old, value)
						matched[i], used[j] = true, true
						break
					}
				}
				if matched[i] {
					continue
				}
			}
			if i < len(previous) && i < len(original) && !used[i] {
				result[i] = patchConfigValue(original[i], previous[i], value)
			} else {
				result[i] = value
			}
		}
		return result
	default:
		return after
	}
}

func configArrayID(value any) string {
	if object, ok := value.(map[string]any); ok {
		id, _ := object["id"].(string)
		return id
	}
	return ""
}

func configRawKey(object map[string]any, key string) string {
	if _, exists := object[key]; exists {
		return key
	}
	// JSON struct fields, like Viper's keys, match case-insensitively.
	// Dynamic map keys already match exactly in the typed snapshot.
	for rawKey := range object {
		if strings.EqualFold(rawKey, key) {
			return rawKey
		}
	}
	return key
}

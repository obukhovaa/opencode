package models

import (
	"regexp"
	"sync"
)

// CustomModelIDPattern is the shape of a config-defined model id
// (`customModels` in .opencode.json, validated in internal/config): only the
// bedrock provider, lower-case. The config schema accepts agent `model`
// values matching it besides the built-in enum.
const CustomModelIDPattern = `^bedrock\.[a-z0-9][a-z0-9._-]*$`

// CustomModelIDRegexp is CustomModelIDPattern compiled.
var CustomModelIDRegexp = regexp.MustCompile(CustomModelIDPattern)

var (
	customMu  sync.Mutex
	customIDs []ModelID
)

// RegisterCustomModels replaces the config-defined models in SupportedModels
// with list, which the caller has validated. An entry whose id is a built-in
// model is not registered and is returned in shadowed; the built-in wins.
//
// SupportedModels is read without locks everywhere, so this must run before
// anything that reads it concurrently — config.Load calls it once, before
// validating agents and before any agent is built.
func RegisterCustomModels(list []Model) (shadowed []ModelID) {
	customMu.Lock()
	defer customMu.Unlock()
	unregisterCustomLocked()
	for _, m := range list {
		if _, builtin := SupportedModels[m.ID]; builtin {
			shadowed = append(shadowed, m.ID)
			continue
		}
		SupportedModels[m.ID] = m
		customIDs = append(customIDs, m.ID)
	}
	return shadowed
}

// UnregisterCustomModels removes every model RegisterCustomModels added,
// restoring the built-in catalog. A no-op when none is registered, so it
// never writes SupportedModels on the common path.
func UnregisterCustomModels() {
	customMu.Lock()
	defer customMu.Unlock()
	unregisterCustomLocked()
}

func unregisterCustomLocked() {
	for _, id := range customIDs {
		delete(SupportedModels, id)
	}
	customIDs = nil
}

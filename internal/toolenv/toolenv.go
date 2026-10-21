// Package toolenv decides which environment variables the processes opencode
// starts may inherit.
//
// opencode runs with its own credentials in its environment: the HTTP API's
// Basic password, the bridge registrar password and the session store DSN.
// None of them is needed by a command the model runs, and every child process
// inherits opencode's environment by default, so `env` through the bash tool
// would print them. Every spawn site therefore builds its environment from
// Environ (or Filter) instead of os.Environ.
//
// opencode's own environment is never modified: several packages read these
// variables lazily with os.Getenv, long after startup.
//
// This package imports nothing internal so that config, hooks and every tool
// package can depend on it without a cycle; config pushes the configured deny
// entries in with SetConfigured.
package toolenv

import (
	"os"
	"path"
	"strings"
	"sync"
)

// EnvVar is the environment variable whose comma-separated entries extend the
// deny list. It suits whatever launches opencode (a container entrypoint, an
// orchestrator) because it applies to every workspace that process serves.
const EnvVar = "OPENCODE_TOOL_ENV_DENY"

// Builtin lists the engine's own secrets. They are always denied; nothing in
// configuration can remove them.
var Builtin = []string{
	"OPENCODE_SERVER_PASSWORD",
	"OPENCODE_BRIDGE_REGISTRAR_PASSWORD",
	"OPENCODE_MYSQL_DSN",
}

var (
	mu         sync.RWMutex
	configured []string
)

// SetConfigured replaces the deny entries that came from `toolEnv.deny` in
// .opencode.json. config.Load calls it; nil clears them.
func SetConfigured(entries []string) {
	mu.Lock()
	defer mu.Unlock()
	configured = append([]string(nil), entries...)
}

// DenyList returns the effective deny entries: the built-ins, the configured
// entries and those from OPENCODE_TOOL_ENV_DENY, trimmed, without empties or
// duplicates.
func DenyList() []string {
	mu.RLock()
	cfgEntries := configured
	mu.RUnlock()

	seen := make(map[string]bool)
	var out []string
	add := func(entry string) {
		entry = strings.TrimSpace(entry)
		if entry == "" || seen[entry] {
			return
		}
		seen[entry] = true
		out = append(out, entry)
	}
	for _, e := range Builtin {
		add(e)
	}
	for _, e := range cfgEntries {
		add(e)
	}
	for _, e := range strings.Split(os.Getenv(EnvVar), ",") {
		add(e)
	}
	return out
}

// Denied reports whether the variable name matches any entry. An entry with a
// glob metacharacter is matched as a shell-style pattern against the whole
// name; a malformed pattern, and any entry without metacharacters, matches only
// the identical name, so a typo can never switch filtering off. Matching is
// case-sensitive, like environment variable names on POSIX.
func Denied(name string, entries []string) bool {
	for _, entry := range entries {
		if entry == name {
			return true
		}
		if !strings.ContainsAny(entry, "*?[") {
			continue
		}
		if ok, err := path.Match(entry, name); err == nil && ok {
			return true
		}
	}
	return false
}

// Filter returns env (KEY=VALUE entries, as from os.Environ) without the
// entries whose name is denied. Entries without "=" are dropped, as exec does.
// The input slice is not modified.
func Filter(env []string, entries []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if Denied(name, entries) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Environ is os.Environ without the denied variables. Use it wherever a child
// process would otherwise inherit os.Environ.
func Environ() []string {
	return Filter(os.Environ(), DenyList())
}

// Present returns the names in env that the deny list matches, in env order.
// The startup hardening uses it to decide whether opencode holds anything
// worth hiding.
func Present(env []string, entries []string) []string {
	var names []string
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if ok && Denied(name, entries) {
			names = append(names, name)
		}
	}
	return names
}

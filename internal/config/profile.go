package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ProfileEnv selects a runtime profile per launch — like CLAUDE_CONFIG_DIR
// selects a Claude Code profile. When set (e.g. OPENCODE_PROFILE=piano, or
// --profile piano), the global config file and the global skills/agents/
// flows/commands directories come from profile-scoped locations
// (~/.config/opencode-<profile>/...), and the unprofiled shared home dirs
// (~/.agents, ~/.claude) drop out of global discovery so profiles stay
// isolated. Project-level .opencode.json still merges over the profile config.
const ProfileEnv = "OPENCODE_PROFILE"

var profileNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// ActiveProfile returns the sanitized profile name from OPENCODE_PROFILE,
// or "" when unset or invalid. An invalid value is ignored, never fatal:
// profiles address files under $HOME, so a hostile value must not escape
// the directory layout (no "..", no separators).
func ActiveProfile() string {
	p := strings.TrimSpace(os.Getenv(ProfileEnv))
	if p == "" || !profileNameRE.MatchString(p) {
		return ""
	}
	return p
}

// ProfileAppName is appName with the active profile suffix
// ("opencode-piano"), or plain appName without a profile.
func ProfileAppName() string {
	if p := ActiveProfile(); p != "" {
		return appName + "-" + p
	}
	return appName
}

// ProfileConfigDirs returns the profile's global-config search dirs in
// priority order (highest first), or nil without an active profile. Mirrors
// the XDG/home layout configureViper uses for the default profile.
func ProfileConfigDirs() []string {
	if ActiveProfile() == "" {
		return nil
	}
	name := ProfileAppName()
	dirs := []string{filepath.Join("$HOME", ".config", name)}
	if os.Getenv("XDG_CONFIG_HOME") != "" {
		dirs = append([]string{filepath.Join("$XDG_CONFIG_HOME", name)}, dirs...)
	}
	return dirs
}

// SharedGlobalDirsInScope reports whether the unprofiled shared home dirs
// (~/.agents, ~/.claude) participate in global discovery: only when no
// profile is active. A profile carries its own ~/.config/opencode-<profile>
// tree instead.
func SharedGlobalDirsInScope() bool {
	return ActiveProfile() == ""
}

// ProfileAgentsDirName is the per-profile name of the "~/.agents" ecosystem
// dir: ".agents-<profile>" with a profile active, plain ".agents" without.
// Skills, commands, agent types and flows are discovered under it, so each
// profile controls its own set.
func ProfileAgentsDirName() string {
	if p := ActiveProfile(); p != "" {
		return ".agents-" + p
	}
	return ".agents"
}

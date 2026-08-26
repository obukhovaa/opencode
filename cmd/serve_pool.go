package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// This file holds the pool-mode boot helpers for `opencode serve
// --pool-mode` (openspec change agent-pod-pool-runtime): the bridge
// inbound-disabled boot guard (design/spec Phase F) and the bound-
// workspace derivation (Phase B.6).

// validatePoolModeInbound enforces the pool-mode bridge posture: every
// identity that would launch an adapter (channel enabled AND identity
// enabled) MUST have inbound explicitly "disabled", so pool replicas
// never contend on the per-identity single-listener lock — inbound chat
// arrives exclusively via the orchestrator-mediated path.
//
// Per-channel identity lists have different keys (telegram bots, slack
// apps, mattermost instances). External consumers carry no Inbound
// field — their inbound is orchestrator-mediated by construction — so
// they are exempt. An EMPTY Inbound string means enabled
// (bridge.IsInboundDisabled) and is therefore a violation too.
//
// Runs after config.Load and before bridgesvc.New; a non-nil error
// aborts boot with a non-zero exit so K8s flags the pod as failed.
// Daemon-mode pods (no --pool-mode) are never subjected to this guard.
func validatePoolModeInbound(router *bridge.Config) error {
	if router == nil {
		return nil
	}
	var violations []string
	check := func(channelEnabled, identityEnabled bool, channel, id, inbound string) {
		if !channelEnabled || !identityEnabled {
			return
		}
		if bridge.IsInboundDisabled(inbound) {
			return
		}
		val := inbound
		if val == "" {
			val = "enabled"
		}
		violations = append(violations, fmt.Sprintf("%s:%s has inbound:%s", channel, id, val))
	}
	if t := router.Channels.Telegram; t != nil {
		for _, b := range t.Bots {
			check(t.Enabled, b.Enabled, "telegram", b.ID, b.Inbound)
		}
	}
	if sl := router.Channels.Slack; sl != nil {
		for _, a := range sl.Apps {
			check(sl.Enabled, a.Enabled, "slack", a.ID, a.Inbound)
		}
	}
	if m := router.Channels.Mattermost; m != nil {
		for _, inst := range m.Instances {
			check(m.Enabled, inst.Enabled, "mattermost", inst.ID, inst.Inbound)
		}
	}
	// External Consumers[] have no Inbound field — skip by design.
	if len(violations) > 0 {
		return fmt.Errorf("pool mode requires inbound:disabled for all bridge channels (%s)",
			strings.Join(violations, "; "))
	}
	return nil
}

// poolWorkspaceURLEnv is the entrypoint-supplied fallback signal for the
// bound workspace.
//
// An orchestrator's pod entrypoint typically bootstraps a pool pod by
// cloning the workspace into a temp dir and overlaying only the agent
// subset (`.agents/`, `AGENTS.md`, `.agents.opencode.json`,
// `.agents.plugins.json`) into the working directory — so the working
// directory is NOT a git checkout and the `.git`-based derivation below
// finds nothing. Such an entrypoint exports this variable with the URL it
// bootstrapped from, which is then the only in-process evidence the pod
// is bound.
const poolWorkspaceURLEnv = "AGENT_WORKSPACE_GIT_URL"

// derivePoolBoundWorkspace reports the workspace git URL the pod booted
// bound to, or "" when the pod is unbound (first-ever boot, or a
// post-recycle clean state).
//
// Two sources, in order:
//
//  1. `remote.origin.url` of the working directory's git checkout — the
//     shape the spec describes, and what an entrypoint that clones
//     straight into the working directory produces.
//  2. The $AGENT_WORKSPACE_GIT_URL export — what the orchestrator's pod
//     entrypoint actually produces, because its pool branch feeds the
//     sentinel URL into the pre-existing clone-to-temp-dir + overlay
//     bootstrap and leaves no `.git` in the working directory.
//
// Both must be honoured: deriving from `.git` alone left every pool pod
// reporting `boundWorkspace: null` forever, so the orchestrator's bind
// handshake could never converge and every pool-eligible job poisoned
// two pods before falling back to the per-Job runner.
//
// Failures are deliberately soft: an unreadable origin and an unset env
// var both just mean "unbound", and the orchestrator binds via POST
// /pool/bind.
func derivePoolBoundWorkspace(dir string) string {
	if url := gitOriginURL(dir); url != "" {
		return url
	}
	return strings.TrimSpace(os.Getenv(poolWorkspaceURLEnv))
}

// gitOriginURL returns dir's `remote.origin.url`, or "" when dir is not
// a git checkout or has no origin.
func gitOriginURL(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return ""
	}
	cmd := exec.Command("git", "-C", dir, "config", "--get", "remote.origin.url")
	// Drop any inherited repo-pinning git env (GIT_DIR & co.) so the
	// lookup always resolves the repository at dir via -C discovery —
	// never a repository some parent process (e.g. a git hook, which
	// exports GIT_DIR/GIT_INDEX_FILE/GIT_WORK_TREE to its children)
	// happened to be operating on.
	cmd.Env = sanitizedGitEnv()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// sanitizedGitEnv returns the process environment with the variables
// that pin git to a specific repository removed. Everything else (PATH,
// HOME, credential helpers) passes through.
func sanitizedGitEnv() []string {
	drop := []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_OBJECT_DIRECTORY=", "GIT_COMMON_DIR="}
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		dropped := false
		for _, p := range drop {
			if strings.HasPrefix(kv, p) {
				dropped = true
				break
			}
		}
		if !dropped {
			out = append(out, kv)
		}
	}
	return out
}

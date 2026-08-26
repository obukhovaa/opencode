package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/opencode-ai/opencode/internal/logging"
)

// This file implements the pool-mode HTTP surface of the openspec change
// agent-pod-pool-runtime: POST/GET /pool/bind (workspace binding via a
// sentinel-file + process-restart cycle, design D2), POST /flow/recycle
// (clean pod retirement, design D4), and the `pool` block for
// /global/health (design D5). All three routes are registered ONLY when
// the server was constructed with ServerOptions.PoolMode — per-Job and
// daemon-mode pods 404 on them.

// defaultPoolSentinelPath is where POST /pool/bind writes the requested
// workspace URL for the pod entrypoint to pick up on the next boot. Overridable
// via --pool-bind-sentinel-path.
const defaultPoolSentinelPath = "/tmp/.pool-bind"

// poolHealth is the `pool` block embedded in /global/health when the
// pod runs in pool mode. Nullable fields are pointers so they render as
// JSON null (not omitted) — the orchestrator's pool controller matches
// on them.
type poolHealth struct {
	// Mode is "available" (idle, claimable), "busy" (a run is in
	// flight), or "draining" (recycle accepted; pod exits shortly).
	Mode           string  `json:"mode"`
	BoundWorkspace *string `json:"boundWorkspace"`
	RunCount       int64   `json:"runCount"`
	LastTerminalAt *int64  `json:"lastTerminalAt"`
	CurrentRunID   *string `json:"currentRunID"`
	Draining       bool    `json:"draining"`
	// LastRunID / LastStatus describe the most recently FINISHED run, and
	// stay populated after the idle reset has cleared the live snapshot.
	// They are what lets a restarted orchestrator distinguish "the run I
	// was watching finished while I was away" (fetch it with GET
	// /flow/status?runID=) from "this pod never ran anything", without
	// which a completed job is reported as a timeout and a healthy pod
	// gets poisoned.
	LastRunID  *string `json:"lastRunID"`
	LastStatus *string `json:"lastStatus"`
}

// buildPoolHealth assembles the pool block from the server's bind/drain
// state and the flow runner's counters.
func (s *Server) buildPoolHealth() poolHealth {
	ph := poolHealth{
		Mode:     "available",
		Draining: s.poolDraining.Load(),
	}
	if s.poolBoundWorkspace != "" {
		bound := s.poolBoundWorkspace
		ph.BoundWorkspace = &bound
	}
	if s.flowRunner != nil {
		ph.RunCount = s.flowRunner.RunCount()
		if t := s.flowRunner.LastTerminalAt(); t != 0 {
			ph.LastTerminalAt = &t
		}
		if id := s.flowRunner.CurrentRunID(); id != "" {
			ph.CurrentRunID = &id
			ph.Mode = "busy"
		}
		if id, status := s.flowRunner.LastTerminal(); id != "" {
			lastID, lastStatus := id, string(status)
			ph.LastRunID = &lastID
			ph.LastStatus = &lastStatus
		}
	}
	if ph.Draining {
		ph.Mode = "draining"
	}
	return ph
}

// writePoolError writes the {"error": <msg>, ...extra} wire shape the
// agent-pod-pool-runtime spec pins for the pool endpoints' failures.
// Distinct from writeError (whose "error" field is the HTTP status text
// and message lives under "message") because the orchestrator's pool
// controller parses these bodies against the spec examples.
func writePoolError(w http.ResponseWriter, status int, msg string, extra map[string]any) {
	body := map[string]any{"error": msg}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, status, body)
}

// normalizeWorkspaceURL canonicalises a workspace git URL for equality
// checks: trims whitespace, strips a trailing "/", a trailing ".git",
// then a trailing "/" again (so ".git/" collapses too), and lowercases
// the scheme+host portion of scheme://host/path URLs. The path segment
// keeps its case (repo paths can be case-sensitive). The SAME
// normalisation applies to the allowlist entries, the boot-derived
// bound workspace, and every URL arriving over HTTP, so all comparisons
// are canonical-vs-canonical.
func normalizeWorkspaceURL(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			s = strings.ToLower(s[:i+3]+rest[:j]) + rest[j:]
		} else {
			s = strings.ToLower(s)
		}
	}
	return s
}

// parseWorkspaceAllowlist splits the WORKSPACE_GIT_URLS_ALLOWLIST CSV
// into normalised entries, dropping empties.
func parseWorkspaceAllowlist(csv string) []string {
	var out []string
	for _, entry := range strings.Split(csv, ",") {
		if norm := normalizeWorkspaceURL(entry); norm != "" {
			out = append(out, norm)
		}
	}
	return out
}

// poolAllowlisted reports whether the (already normalised) workspace URL
// is in the boot-time allowlist.
func (s *Server) poolAllowlisted(normalizedURL string) bool {
	for _, entry := range s.poolAllowlist {
		if entry == normalizedURL {
			return true
		}
	}
	return false
}

// writeSentinelAtomic writes the bind sentinel via tmp-file + rename so
// the pod entrypoint can never observe a half-written URL.
func writeSentinelAtomic(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".pool-bind-*")
	if err != nil {
		return fmt.Errorf("create temp sentinel: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp sentinel: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp sentinel: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename sentinel into place: %w", err)
	}
	return nil
}

// handlePoolBindPost implements POST /pool/bind (design D2). Guard
// order follows the spec: draining → binding-in-progress (503) →
// flow-in-flight (400) → bound-elsewhere (409) → allowlist (403) →
// idempotent same-URL (200) → fresh bind (202 + sentinel + scheduled
// exit).
func (s *Server) handlePoolBindPost(w http.ResponseWriter, r *http.Request) {
	if s.poolDraining.Load() {
		writePoolError(w, http.StatusServiceUnavailable, "pod draining", nil)
		return
	}
	if s.poolBinding.Load() {
		// A bind was already accepted; the process is inside its exit
		// grace. Accepting a second bind would write a second sentinel
		// that the (already scheduled) exit races with.
		writePoolError(w, http.StatusServiceUnavailable, "pod binding; exiting for respawn", nil)
		return
	}
	if s.flowRunner != nil && s.flowRunner.InFlight() {
		writePoolError(w, http.StatusBadRequest, "flow in progress; cannot rebind", nil)
		return
	}
	var body struct {
		Workspace string `json:"workspace"`
	}
	if err := readJSON(r, &body); err != nil {
		writePoolError(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	norm := normalizeWorkspaceURL(body.Workspace)
	if norm == "" {
		writePoolError(w, http.StatusBadRequest, "workspace is required", nil)
		return
	}
	if s.poolBoundWorkspace != "" && s.poolBoundWorkspace != norm {
		writePoolError(w, http.StatusConflict,
			fmt.Sprintf("pod bound to %s; recycle to rebind", s.poolBoundWorkspace),
			map[string]any{"boundWorkspace": s.poolBoundWorkspace})
		return
	}
	if !s.poolAllowlisted(norm) {
		writePoolError(w, http.StatusForbidden, "workspace not in allowlist", nil)
		return
	}
	if s.poolBoundWorkspace == norm {
		writeJSON(w, http.StatusOK, map[string]any{"binding": norm, "alreadyBound": true})
		return
	}
	// Latch BEFORE the sentinel write, and under the runner's lock, so a
	// POST /flow that raced the in-flight check above either was already
	// visible as in-flight (latchInFlight → 400) or now sees the gate and
	// is refused. Otherwise a run could start on a process that is
	// milliseconds from exiting for its workspace-clone respawn.
	switch s.latchBinding() {
	case latchAlreadySet:
		writePoolError(w, http.StatusServiceUnavailable, "pod binding; exiting for respawn", nil)
		return
	case latchInFlight:
		writePoolError(w, http.StatusBadRequest, "flow in progress; cannot rebind", nil)
		return
	}
	if err := writeSentinelAtomic(s.poolSentinelPath, norm); err != nil {
		s.flowRunnerUnlatch(&s.poolBinding)
		logging.Error("pool bind: sentinel write failed", "path", s.poolSentinelPath, "error", err)
		writePoolError(w, http.StatusInternalServerError, fmt.Sprintf("sentinel write failed: %v", err), nil)
		return
	}
	logging.Info("pool bind accepted — exiting for workspace clone on respawn",
		"workspace", norm, "sentinel", s.poolSentinelPath, "exitGrace", s.poolBindExitGrace)
	exit := s.poolExit
	time.AfterFunc(s.poolBindExitGrace, func() {
		logging.Info("pool bind exit grace elapsed — exiting for respawn")
		exit(0)
	})
	writeJSON(w, http.StatusAccepted, map[string]any{
		"binding":     norm,
		"exitGraceMs": s.poolBindExitGrace.Milliseconds(),
	})
}

// handlePoolBindGet implements GET /pool/bind.
func (s *Server) handlePoolBindGet(w http.ResponseWriter, _ *http.Request) {
	var bound, since any
	if s.poolBoundWorkspace != "" {
		bound = s.poolBoundWorkspace
		since = s.poolBoundSince
	}
	writeJSON(w, http.StatusOK, map[string]any{"boundWorkspace": bound, "since": since})
}

// handleFlowRecycle implements POST /flow/recycle (design D4): a clean
// operator-initiated pod retirement. 409 while ANY non-terminal run is
// in flight (running or waiting_for_input — design D8); otherwise flip
// to draining (new work refused with 503, reads stay available for
// observability) and, after --pool-drain-grace, trigger the SAME serve-
// context cancellation SIGTERM uses so cmd/serve.go's deferred
// application.Shutdown() and bridge Stop() run before the process exits
// 0. POST /global/dispose is deliberately NOT involved — it is a no-op
// stub (handler_health.go).
func (s *Server) handleFlowRecycle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &body); err != nil && !isEmptyBodyError(err) {
		writePoolError(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	// Latch draining and verify "no run in flight" as ONE step under the
	// runner's lock. The earlier shape — latch, then check, then roll the
	// latch back on conflict — had a window in which a second concurrent
	// recycle saw draining already set, answered 202, and then had the
	// first request clear it: the caller believed the pod was draining
	// while nothing had been scheduled, and the pod lived on holding a
	// pool slot forever. A repeated recycle after a successful one is
	// still an idempotent 202.
	switch s.latchDraining() {
	case latchAlreadySet:
		writeJSON(w, http.StatusAccepted, map[string]any{"draining": true})
		return
	case latchInFlight:
		writePoolError(w, http.StatusConflict, "flow in progress, abort first", nil)
		return
	}
	// Clear the bind sentinel so the respawned pod comes back UNBOUND.
	// The sentinel is the pod's durable binding record — the entrypoint
	// leaves it in place after a successful bootstrap so a container restart
	// inside the same pod re-clones the same workspace (the working
	// directory is ephemeral; /pool-state is not). Recycle is exactly the
	// transition that must break that: design D2's rebind protocol is
	// "recycle, wait for exit, then POST /pool/bind {workspace: B}" on a
	// now-empty pod. Best-effort: a failure here is logged, not fatal —
	// the pod still drains, and a stale sentinel only means the respawn
	// comes back bound to its previous workspace, which the orchestrator
	// resolves with a 409 on the next bind.
	s.clearBindSentinel("recycle")

	logging.Info("pool recycle accepted — draining",
		"reason", body.Reason, "drainGrace", s.poolDrainGrace)
	shutdown := s.poolShutdown
	time.AfterFunc(s.poolDrainGrace, func() {
		logging.Info("pool drain grace elapsed — triggering serve shutdown")
		if shutdown != nil {
			shutdown()
		}
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"draining": true})
}

// latchDraining latches poolDraining iff no run is in flight, delegating
// to the runner so the check and the latch share fr.mu. With no runner
// wired (an app without a flow service) there is nothing that could be
// in flight, so the gate is set directly.
func (s *Server) latchDraining() latchResult {
	if s.flowRunner == nil {
		if s.poolDraining.CompareAndSwap(false, true) {
			return latchAcquired
		}
		return latchAlreadySet
	}
	return s.flowRunner.latchExclusive(&s.poolDraining)
}

// latchBinding is latchDraining's counterpart for POST /pool/bind.
func (s *Server) latchBinding() latchResult {
	if s.flowRunner == nil {
		if s.poolBinding.CompareAndSwap(false, true) {
			return latchAcquired
		}
		return latchAlreadySet
	}
	return s.flowRunner.latchExclusive(&s.poolBinding)
}

// flowRunnerUnlatch clears a gate, going through the runner's lock when
// one is wired so the clear is ordered against Start's read of the gate.
func (s *Server) flowRunnerUnlatch(gate *atomic.Bool) {
	if s.flowRunner == nil {
		gate.Store(false)
		return
	}
	s.flowRunner.unlatch(gate)
}

// clearBindSentinel removes the bind sentinel, tolerating its absence.
func (s *Server) clearBindSentinel(reason string) {
	if s.poolSentinelPath == "" {
		return
	}
	if err := os.Remove(s.poolSentinelPath); err != nil && !os.IsNotExist(err) {
		logging.Warn("pool: failed to clear bind sentinel",
			"path", s.poolSentinelPath, "reason", reason, "error", err)
		return
	}
	logging.Info("pool: bind sentinel cleared", "path", s.poolSentinelPath, "reason", reason)
}

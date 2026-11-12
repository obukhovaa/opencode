package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
)

type sessionFlowAdmission struct {
	SessionID string         `json:"sessionId"`
	JobID     string         `json:"jobId"`
	Peer      bridge.PeerRef `json:"peer"`
}
type sessionFlowAdmissionRequest struct {
	sessionFlowOrigin
	SessionID    string `json:"sessionId,omitempty"`
	Agent        string `json:"agent"`
	Workspace    string `json:"workspace,omitempty"`
	WorkspaceRef string `json:"workspaceRef,omitempty"`
}

// claimConversation is ordered against standalone flow starts and worker
// rebinding/recycling by the same mutex as the existing flow runner.
func (s *Server) claimConversation(next *sessionFlowAdmission) (bool, error) {
	if !s.poolMode {
		return false, nil
	}
	if s.flowRunner == nil {
		return false, fmt.Errorf("flow runner unavailable")
	}
	s.flowRunner.mu.Lock()
	defer s.flowRunner.mu.Unlock()
	if s.poolDraining.Load() || s.poolBinding.Load() {
		return false, fmt.Errorf("worker is draining or rebinding")
	}
	if existing := s.sessionFlowAdmission.Load(); existing != nil {
		if existing.SessionID == next.SessionID && existing.JobID == next.JobID && existing.Peer.Channel == next.Peer.Channel && existing.Peer.Identity == next.Peer.Identity && existing.Peer.PeerID == next.Peer.PeerID {
			return false, nil
		}
		return false, fmt.Errorf("worker owns another conversation")
	}
	if s.flowRunner.currentRun != nil && !isTerminalStatus(s.flowRunner.currentRun.Status) {
		return false, fmt.Errorf("worker owns a standalone flow")
	}
	s.sessionFlowAdmission.Store(next)
	return true, nil
}
func (s *Server) forgetConversation(admission *sessionFlowAdmission) {
	if s.flowRunner != nil {
		s.flowRunner.mu.Lock()
		defer s.flowRunner.mu.Unlock()
	}
	s.sessionFlowAdmission.CompareAndSwap(admission, nil)
}
func (s *Server) handleSessionFlowAdmit(w http.ResponseWriter, r *http.Request) {
	if s.sessionFlowBridge == nil {
		writeError(w, http.StatusServiceUnavailable, "bridge session admission unavailable")
		return
	}
	var req sessionFlowAdmissionRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.JobID) == "" || len(req.JobID) > 200 || req.Peer.Channel == "" || req.Peer.Identity == "" || req.Peer.PeerID == "" {
		writeError(w, http.StatusBadRequest, "jobId and a complete peer are required")
		return
	}
	peerUnlock := s.lockSessionFlowKey("peer", req.Peer.Channel+"\x00"+req.Peer.Identity+"\x00"+req.Peer.PeerID)
	defer peerUnlock()
	if s.poolMode && (s.poolBoundWorkspace == "" || normalizeWorkspaceURL(req.Workspace) != s.poolBoundWorkspace || req.WorkspaceRef != s.poolBoundRef) {
		writeError(w, http.StatusConflict, "worker workspace/ref does not match admission")
		return
	}
	existingConversation, resolveErr := s.sessionFlowBridge.ResolveSessionFlowConversation(r.Context(), req.Peer, req.JobID)
	if resolveErr != nil {
		writeError(w, http.StatusForbidden, resolveErr.Error())
		return
	}
	if existingConversation != "" {
		if req.SessionID != "" && req.SessionID != existingConversation {
			writeError(w, http.StatusConflict, "peer is bound to another conversation")
			return
		}
		req.SessionID = existingConversation
	}
	if req.SessionID == "" {
		// The binding, not a deterministic peer hash, is the retry identity.
		// Once /reset removes it, a new admission must not resurrect old history.
		req.SessionID = uuid.NewString()
	}
	unlock := s.lockSessionFlowOperation(req.SessionID)
	defer unlock()
	if req.Agent == "" {
		current, err := s.app.Sessions.Get(r.Context(), req.SessionID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeSessionFlowError(w, err)
			return
		}
		req.Agent = current.AgentName
		if req.Agent == "" {
			req.Agent = string(s.app.ActiveAgentName())
		}
	}
	info, ok := s.app.Registry.Get(req.Agent)
	if !ok || info.Disabled || info.Mode != config.AgentModeAgent {
		writeError(w, http.StatusBadRequest, "agent must name an enabled primary agent")
		return
	}

	next := &sessionFlowAdmission{SessionID: req.SessionID, JobID: req.JobID, Peer: req.Peer}
	claimed, err := s.claimConversation(next)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	success := false
	defer func() {
		if claimed && !success {
			s.forgetConversation(next)
		}
	}()
	ctx, cancel, err := admittedFlowContext(r.Context(), req.sessionFlowOrigin)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer cancel()
	sess, err := s.app.Sessions.Get(ctx, req.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		sess, err = s.app.Sessions.CreateWithID(ctx, req.SessionID, "Bridge conversation")
	}
	if err != nil {
		writeSessionFlowError(w, err)
		return
	}
	if sess.ParentSessionID != "" || (sess.RootSessionID != "" && sess.RootSessionID != sess.ID) {
		writeError(w, http.StatusForbidden, "cannot admit a flow-step or subagent session")
		return
	}
	if err = s.app.SessionFlowAccess(ctx, sess.ID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if agentpkg.SessionBusy(sess.ID) {
		writeError(w, http.StatusConflict, "conversation has an active turn")
		return
	}
	if sess.AgentName != "" && sess.AgentName != req.Agent {
		writeError(w, http.StatusConflict, "admission agent differs from conversation agent")
		return
	}
	if err = s.app.Sessions.SetAgentName(ctx, sess.ID, req.Agent); err != nil {
		writeSessionFlowError(w, err)
		return
	}
	releaseReservation := func() {}
	local, hasLocalCheck := s.sessionFlowBridge.(interface {
		IsSessionFlowAdmitted(string, bridge.PeerRef, string) bool
	})
	if !hasLocalCheck || !local.IsSessionFlowAdmitted(sess.ID, req.Peer, req.JobID) {
		var reservationErr error
		releaseReservation, reservationErr = s.app.ReserveFlowAdmission(ctx, sess.ID)
		if reservationErr != nil {
			writeSessionFlowError(w, reservationErr)
			return
		}
	}
	defer func() {
		if !success {
			releaseReservation()
		}
	}()
	if err = s.sessionFlowBridge.AdmitSessionFlow(ctx, sess.ID, req.Peer, req.JobID); err != nil {
		writeSessionFlowError(w, err)
		return
	}
	success = true
	writeJSON(w, http.StatusOK, map[string]any{"version": 1, "sessionId": sess.ID, "jobId": req.JobID, "admitted": true})
}
func (s *Server) handleSessionFlowRelease(w http.ResponseWriter, r *http.Request) {
	if s.sessionFlowBridge == nil {
		writeError(w, http.StatusServiceUnavailable, "bridge session admission unavailable")
		return
	}
	var req struct {
		SessionID string `json:"sessionId"`
		JobID     string `json:"jobId"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.SessionID == "" || req.JobID == "" {
		writeError(w, http.StatusBadRequest, "sessionId and jobId required")
		return
	}
	unlock := s.lockSessionFlowOperation(req.SessionID)
	defer unlock()
	current := s.sessionFlowAdmission.Load()
	if s.poolMode && (current == nil || current.SessionID != req.SessionID || current.JobID != req.JobID) {
		writeError(w, http.StatusConflict, "worker does not own this conversation/job")
		return
	}
	if s.app.FlowBusy(req.SessionID) || agentpkg.SessionBusy(req.SessionID) {
		writeError(w, http.StatusConflict, "conversation is still executing")
		return
	}
	if err := s.sessionFlowBridge.ReleaseSessionFlow(r.Context(), req.SessionID, req.JobID); err != nil {
		writeSessionFlowError(w, err)
		return
	}
	if s.poolMode {
		s.forgetConversation(current)
	}
	writeJSON(w, http.StatusOK, map[string]any{"released": true})
}

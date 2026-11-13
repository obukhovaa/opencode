package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// SessionFlowBridge is optional: plain API/TUI use does not require a bridge.
// Origin-bearing requests must resolve against an existing authorized binding.
type SessionFlowBridge interface {
	ResolveSessionFlowConversation(context.Context, bridge.PeerRef, string) (string, error)
	AdmitSessionFlow(context.Context, string, bridge.PeerRef, string) error
	ReleaseSessionFlow(context.Context, string, string) error
	ValidateSessionFlowOrigin(context.Context, string, bridge.PeerRef, string) error
	BridgeSessionFlowContext(context.Context, string, bridge.PeerRef, string) (context.Context, error)
}

type sessionFlowOrigin struct {
	Peer      bridge.PeerRef `json:"peer"`
	JobID     string         `json:"jobId"`
	Requester string         `json:"requester,omitempty"`
	Deadline  int64          `json:"deadline,omitempty"`
	JobArgs   map[string]any `json:"jobArgs,omitempty"`
}
type sessionFlowRequest struct {
	tools.FlowRequest
	Origin *sessionFlowOrigin `json:"origin,omitempty"`
}

func (s *Server) handleSessionFlowCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.app.FlowCapabilities())
}
func (s *Server) validateSessionFlowRoot(w http.ResponseWriter, r *http.Request) bool {
	id := r.PathValue("sessionID")
	if s.poolMode {
		admission := s.sessionFlowAdmission.Load()
		if admission == nil || admission.SessionID != id {
			writeError(w, http.StatusForbidden, "worker does not own this conversation")
			return false
		}
	}
	sess, err := s.app.Sessions.Get(r.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		writeError(w, status, "session not found")
		return false
	}
	if sess.ParentSessionID != "" || (sess.RootSessionID != "" && sess.RootSessionID != sess.ID) {
		writeError(w, http.StatusForbidden, "flow operations require an ordinary root conversation")
		return false
	}
	if err = s.app.SessionFlowAccess(r.Context(), id); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return false
	}
	return true
}
func (s *Server) sessionFlowOriginContext(r *http.Request, id string, origin *sessionFlowOrigin) (context.Context, context.CancelFunc, error) {
	ctx := r.Context()
	cancel := func() {}
	if origin == nil {
		return ctx, cancel, nil
	}
	if s.sessionFlowBridge == nil {
		return ctx, cancel, fmt.Errorf("bridge flow admission is unavailable")
	}
	if err := s.sessionFlowBridge.ValidateSessionFlowOrigin(ctx, id, origin.Peer, origin.JobID); err != nil {
		return ctx, cancel, err
	}
	ctx, err := s.sessionFlowBridge.BridgeSessionFlowContext(ctx, id, origin.Peer, origin.JobID)
	if err != nil {
		return ctx, cancel, err
	}
	return ctx, cancel, nil
}
func (s *Server) handleSessionFlowGet(w http.ResponseWriter, r *http.Request) {
	if !s.validateSessionFlowRoot(w, r) {
		return
	}
	snapshot, err := s.app.DynamicFlowSnapshot(r.Context(), r.PathValue("sessionID"))
	if err != nil {
		writeSessionFlowError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}
func (s *Server) handleSessionFlowPost(w http.ResponseWriter, r *http.Request) {
	if !s.validateSessionFlowRoot(w, r) {
		return
	}
	if s.poolMode && (s.poolDraining.Load() || s.poolBinding.Load()) {
		writeError(w, http.StatusConflict, "worker is draining or rebinding")
		return
	}
	var req sessionFlowRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch req.Operation {
	case "plan", "create", "run", "update", "list", "status", "control":
	default:
		writeError(w, http.StatusBadRequest, "unknown flow operation")
		return
	}
	id := r.PathValue("sessionID")
	unlock := s.lockSessionFlowOperation(id)
	defer unlock()
	ctx, cancel, err := s.sessionFlowOriginContext(r, id, req.Origin)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	defer cancel()
	if s.poolMode {
		admission := s.sessionFlowAdmission.Load()
		if admission == nil || admission.SessionID != id {
			writeError(w, http.StatusConflict, "conversation is not admitted on this worker")
			return
		}
		if req.Origin == nil || req.Origin.JobID != admission.JobID {
			writeError(w, http.StatusForbidden, "admitted job origin is required")
			return
		}
	}
	name, _ := s.app.ResolveSessionAgent(ctx, id)
	ctx = context.WithValue(ctx, tools.AgentIDContextKey, name)
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) > 200 {
		writeError(w, http.StatusBadRequest, "Idempotency-Key exceeds 200 characters")
		return
	}
	if key == "" {
		key = uuid.NewString()
	}
	req.InvocationID = sessionFlowInvocationID(id, key)
	result, err := s.app.SubmitFlow(ctx, id, req.FlowRequest)
	if err != nil {
		writeSessionFlowError(w, err)
		return
	}
	code := http.StatusOK
	if req.Operation == "plan" || req.Operation == "create" || req.Operation == "run" {
		code = http.StatusAccepted
	}
	w.Header().Set("Idempotency-Key", key)
	writeJSON(w, code, result)
}
func (s *Server) handleSessionFlowEvents(w http.ResponseWriter, r *http.Request) {
	if !s.validateSessionFlowRoot(w, r) {
		return
	}
	after := r.URL.Query().Get("after")
	if after != "" {
		split := strings.LastIndexByte(after, ':')
		if split <= 0 || split == len(after)-1 {
			writeError(w, http.StatusBadRequest, "after must be a runId:sequence cursor")
			return
		}
		n, err := strconv.ParseInt(after[split+1:], 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid event cursor sequence")
			return
		}
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 500")
			return
		}
		limit = n
	}
	events, err := s.app.ReplayFlowEvents(r.Context(), r.PathValue("sessionID"), after, limit)
	if err != nil {
		writeSessionFlowError(w, err)
		return
	}
	cursor := after
	if len(events) > 0 {
		cursor = events[len(events)-1].RunID + ":" + strconv.FormatInt(events[len(events)-1].Sequence, 10)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "cursor": cursor})
}
func writeSessionFlowError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	if errors.Is(err, app.ErrFlowPermission) {
		status = http.StatusForbidden
	}
	if errors.Is(err, app.ErrFlowInput) {
		status = http.StatusBadRequest
	}
	if errors.Is(err, sql.ErrNoRows) {
		status = http.StatusNotFound
	}
	var classified interface{ HTTPStatus() int }
	if errors.As(err, &classified) && classified.HTTPStatus() >= 400 && classified.HTTPStatus() <= 599 {
		status = classified.HTTPStatus()
	}
	writeError(w, status, err.Error())
}

func admittedFlowContext(ctx context.Context, origin sessionFlowOrigin) (context.Context, context.CancelFunc, error) {
	ctx = tools.WithPeer(ctx, tools.Peer{Channel: origin.Peer.Channel, Identity: origin.Peer.Identity, PeerID: origin.Peer.PeerID})
	if origin.Requester != "" {
		ctx = tools.WithRequester(ctx, origin.Requester)
	}
	if origin.JobArgs != nil {
		ctx = tools.WithJobArgs(ctx, origin.JobArgs)
	}
	if origin.Deadline > 0 {
		deadline := time.UnixMilli(origin.Deadline)
		if !deadline.After(time.Now()) {
			return ctx, func() {}, fmt.Errorf("job deadline has expired")
		}
		next, cancel := context.WithDeadline(ctx, deadline)
		return next, cancel, nil
	}
	return ctx, func() {}, nil
}

type sessionFlowOperationKey struct{ scope, id string }

func (s *Server) lockSessionFlowOperation(conversation string) func() {
	return s.lockSessionFlowKey("conversation", conversation)
}
func (s *Server) lockSessionFlowKey(scope, id string) func() {
	entry, _ := s.sessionFlowOps.LoadOrStore(sessionFlowOperationKey{scope, id}, &sync.Mutex{})
	lock := entry.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

func sessionFlowInvocationID(conversation, key string) string {
	sum := sha256.Sum256([]byte(conversation + "\x00" + key))
	return "api-" + hex.EncodeToString(sum[:])
}

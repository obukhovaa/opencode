package service

import (
	"errors"
	"net/http"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// bindRequest is the body shape POST /router/bind accepts.
type bindRequest struct {
	SessionID string           `json:"sessionId"`
	Peers     []bridge.PeerRef `json:"peers"`
}

// bindResponse mirrors the bridge.Service.Bind return shape per peer.
type bindResponse struct {
	Results []bindResult `json:"results"`
}

type bindResult struct {
	Channel        string `json:"channel"`
	Identity       string `json:"identity"`
	PeerID         string `json:"peerId"`
	ResolvedPeerID string `json:"resolvedPeerId,omitempty"`
	SessionID      string `json:"sessionId,omitempty"`
	Err            string `json:"error,omitempty"`
}

func (s *Service) handleBind(w http.ResponseWriter, r *http.Request) {
	var req bindRequest
	if err := readJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.SessionID == "" {
		writeAPIError(w, http.StatusBadRequest, "sessionId is required")
		return
	}
	if len(req.Peers) == 0 {
		writeAPIError(w, http.StatusBadRequest, "peers must contain at least one entry")
		return
	}

	results, err := s.Bind(r.Context(), req.SessionID, req.Peers)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := bindResponse{Results: make([]bindResult, len(results))}
	for i, r := range results {
		row := bindResult{
			Channel:        r.Requested.Channel,
			Identity:       r.Requested.Identity,
			PeerID:         r.Requested.PeerID,
			ResolvedPeerID: r.ResolvedPeerID,
		}
		if r.Err != nil {
			row.Err = r.Err.Error()
		} else {
			row.SessionID = r.Binding.SessionID
		}
		resp.Results[i] = row
	}
	writeJSON(w, http.StatusOK, resp)
}

// unbindRequest accepts either { sessionId } (unbind all) or
// { sessionId, peers: [...] } (partial unbind).
type unbindRequest struct {
	SessionID string           `json:"sessionId"`
	Peers     []bridge.PeerRef `json:"peers,omitempty"`
}

func (s *Service) handleUnbind(w http.ResponseWriter, r *http.Request) {
	var req unbindRequest
	if err := readJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.SessionID == "" {
		writeAPIError(w, http.StatusBadRequest, "sessionId is required")
		return
	}
	var err error
	if len(req.Peers) == 0 {
		err = s.Unbind(r.Context(), req.SessionID)
	} else {
		err = s.Unbind(r.Context(), req.SessionID, req.Peers...)
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ensure errors compiles cleanly when no helpers reference it directly
var _ = errors.New

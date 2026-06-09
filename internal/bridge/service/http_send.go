package service

import (
	"net/http"
	"strings"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// sendRequest is the body shape POST /router/send accepts. Matches the TS
// bridge's /send contract minus the explicit `autoBind` field — the
// chat-bridge-http-api spec drops autoBind in favor of explicit
// /router/bind.
type sendRequest struct {
	Channel  string `json:"channel"`
	Identity string `json:"identity"`
	PeerID   string `json:"peerId"`
	Text     string `json:"text"`
	Mention  string `json:"mention,omitempty"`
	// Files lists local paths to attach. Each path MUST be inside the
	// bridge media store; the handler rejects paths outside it.
	Files []string `json:"files,omitempty"`
	// AutoBind is explicitly rejected per the chat-bridge-http-api spec
	// ("autoBind field rejected").
	AutoBind *bool `json:"autoBind,omitempty"`
}

type sendResponse struct {
	Delivered []sendDeliveryRow `json:"delivered"`
	Errors    []sendDeliveryRow `json:"errors,omitempty"`
}

type sendDeliveryRow struct {
	Channel      string `json:"channel"`
	Identity     string `json:"identity"`
	PeerID       string `json:"peerId"`
	ResolvedPeer string `json:"resolvedPeerId,omitempty"`
	Err          string `json:"error,omitempty"`
}

func (s *Service) handleSend(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if err := readJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.AutoBind != nil {
		writeAPIError(w, http.StatusBadRequest,
			"autoBind is not supported; call POST /router/bind explicitly before /router/send")
		return
	}
	if req.Channel == "" || req.Identity == "" || req.PeerID == "" {
		writeAPIError(w, http.StatusBadRequest, "channel, identity, and peerId are required")
		return
	}
	if strings.TrimSpace(req.Text) == "" && len(req.Files) == 0 {
		writeAPIError(w, http.StatusBadRequest, "text or files required")
		return
	}

	mediaRoot, _ := s.MediaDir()
	atts := make([]bridge.Attachment, 0, len(req.Files))
	for _, path := range req.Files {
		att, err := loadMediaAttachment(path, mediaRoot)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "files: "+err.Error())
			return
		}
		atts = append(atts, att)
	}

	peer := bridge.PeerRef{
		Channel:  req.Channel,
		Identity: req.Identity,
		PeerID:   req.PeerID,
	}
	result, err := s.Send(r.Context(), peer, req.Text, req.Mention, atts)
	resp := sendResponse{}
	if err != nil {
		resp.Errors = append(resp.Errors, sendDeliveryRow{
			Channel: peer.Channel, Identity: peer.Identity, PeerID: peer.PeerID,
			Err: err.Error(),
		})
		writeJSON(w, http.StatusBadGateway, resp)
		return
	}
	row := sendDeliveryRow{
		Channel: peer.Channel, Identity: peer.Identity, PeerID: peer.PeerID,
		ResolvedPeer: result.ResolvedPeer,
	}
	if result.Delivered {
		resp.Delivered = append(resp.Delivered, row)
	} else {
		if result.Err != nil {
			row.Err = result.Err.Error()
		}
		resp.Errors = append(resp.Errors, row)
	}
	writeJSON(w, http.StatusOK, resp)
}

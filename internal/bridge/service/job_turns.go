package service

import (
	"sync"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// jobTurns records which orchestrator job each session and reviewer peer was
// bound for, so a free-text bridge turn can be proven to belong to the job
// whose args the process holds (tools.ProcessJobArgsFor) instead of being
// stamped with them merely because a job is running.
type jobTurns struct {
	mu       sync.Mutex
	sessions map[string]string // sessionID -> jobID
	peers    map[string]string // channel|identity|peerID -> jobID
}

func jobPeerKey(p bridge.PeerRef) string {
	return p.Channel + "|" + p.Identity + "|" + p.PeerID
}

// markJobTurns records that sessionID and peers were bound by the job jobID.
// An empty jobID records nothing.
func (s *Service) markJobTurns(jobID, sessionID string, peers ...bridge.PeerRef) {
	if jobID == "" {
		return
	}
	s.jobTurns.mu.Lock()
	defer s.jobTurns.mu.Unlock()
	if s.jobTurns.sessions == nil {
		s.jobTurns.sessions = map[string]string{}
		s.jobTurns.peers = map[string]string{}
	}
	s.jobTurns.sessions[sessionID] = jobID
	for _, p := range peers {
		s.jobTurns.peers[jobPeerKey(p)] = jobID
	}
}

// pruneJobTurns forgets every record that does not belong to jobID. Called
// whenever the pod's job identity changes, so a late turn of a previous job
// can no longer be tied to anything.
func (s *Service) pruneJobTurns(jobID string) {
	s.jobTurns.mu.Lock()
	defer s.jobTurns.mu.Unlock()
	for k, v := range s.jobTurns.sessions {
		if v != jobID {
			delete(s.jobTurns.sessions, k)
		}
	}
	for k, v := range s.jobTurns.peers {
		if v != jobID {
			delete(s.jobTurns.peers, k)
		}
	}
}

// turnJobID is the job a turn of sessionID from peer was bound for, or "".
func (s *Service) turnJobID(sessionID string, peer bridge.PeerRef) string {
	s.jobTurns.mu.Lock()
	defer s.jobTurns.mu.Unlock()
	if id := s.jobTurns.sessions[sessionID]; id != "" {
		return id
	}
	return s.jobTurns.peers[jobPeerKey(peer)]
}

// jobArgsForTurn returns the job args a bridge turn may carry as MCP
// flowArgHeaders. A daemon turn carries none (the design rule: a daemon
// serves many conversations and none of them is the claim of a job), and any
// other turn carries them only when its session or peer was bound by the very
// job whose args the process holds.
func (s *Service) jobArgsForTurn(sessionID string, peer bridge.PeerRef) map[string]any {
	if s.HeartbeatsEnabled() {
		return nil
	}
	return tools.ProcessJobArgsFor(s.turnJobID(sessionID, peer))
}

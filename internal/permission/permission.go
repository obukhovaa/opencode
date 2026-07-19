package permission

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

var ErrorPermissionDenied = errors.New("permission denied")

type CreatePermissionRequest struct {
	SessionID   string `json:"session_id"`
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Params      any    `json:"params"`
	Path        string `json:"path"`
}

type PermissionRequest struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Params      any    `json:"params"`
	Path        string `json:"path"`
}

type Service interface {
	pubsub.Suscriber[PermissionRequest]
	GrantPersistant(permission PermissionRequest)
	Grant(permission PermissionRequest)
	Deny(permission PermissionRequest)
	Request(ctx context.Context, opts CreatePermissionRequest) bool
	AutoApproveSession(sessionID string)
	RemoveAutoApproveSession(sessionID string)
	IsAutoApproveSession(sessionID string) bool

	// LinkSession records parentSessionID as the permission ancestor of
	// sessionID (task-tool subagent sessions link to their caller).
	// IsAutoApproveSession and persisted grants resolve through the chain
	// live: enabling auto-approve on the parent applies to subagents that
	// are already running (and to resumed tasks), and disabling it revokes
	// them at the same moment. A point-in-time copy at spawn cannot do
	// either.
	LinkSession(sessionID, parentSessionID string)

	// MarkInteractiveSession flags a session as interactively bound
	// to a human (via the chat-bridge for `interactive: true` flow
	// steps). When set, the question tool will NOT short-circuit on
	// auto-approve — it'll still defer to the bridge so the human
	// actually picks the answer. Cleared by RemoveInteractiveSession
	// (typically called from the InteractiveHook's unbind path).
	MarkInteractiveSession(sessionID string)
	RemoveInteractiveSession(sessionID string)
	IsInteractiveSession(sessionID string) bool
}

type permissionService struct {
	*pubsub.Broker[PermissionRequest]

	sessionPermissions   []PermissionRequest
	pendingRequests      sync.Map
	autoApproveSessions  sync.Map
	interactiveSessions  sync.Map
	sessionParents       sync.Map // child session ID -> parent session ID
	serializePermissions sync.Mutex
}

func (s *permissionService) GrantPersistant(permission PermissionRequest) {
	respCh, ok := s.pendingRequests.Load(permission.ID)
	if ok {
		respCh.(chan bool) <- true
	}
	s.sessionPermissions = append(s.sessionPermissions, permission)
}

func (s *permissionService) Grant(permission PermissionRequest) {
	respCh, ok := s.pendingRequests.Load(permission.ID)
	if ok {
		respCh.(chan bool) <- true
	}
}

func (s *permissionService) Deny(permission PermissionRequest) {
	respCh, ok := s.pendingRequests.Load(permission.ID)
	if ok {
		respCh.(chan bool) <- false
	}
}

// hookAllowKey is a context key set by the agent loop when a PreToolUse
// hook returned permissionDecision: "allow". The permission service
// honors it as a per-call auto-approve, mirroring D8 in the
// flow-runtime-resume-adjacent claude-code-hooks-plugin-system spec.
type hookAllowKeyType struct{}

// HookAllowKey is the context key used by the agent loop to signal that
// a PreToolUse hook explicitly allowed this tool invocation, bypassing
// the standard permission gate for this call only.
var HookAllowKey = hookAllowKeyType{}

func (s *permissionService) Request(ctx context.Context, opts CreatePermissionRequest) bool {
	if v, ok := ctx.Value(HookAllowKey).(bool); ok && v {
		return true
	}
	if s.IsAutoApproveSession(opts.SessionID) {
		return true
	}
	dir := filepath.Dir(opts.Path)
	if dir == "." {
		dir = config.WorkingDirectory()
	}
	permission := PermissionRequest{
		ID:          uuid.New().String(),
		Path:        dir,
		SessionID:   opts.SessionID,
		ToolName:    opts.ToolName,
		Description: opts.Description,
		Action:      opts.Action,
		Params:      opts.Params,
	}

	// NOTE: serialise permission dialog, permissions requests are interactive
	defer s.serializePermissions.Unlock()
	s.serializePermissions.Lock()

	for _, p := range s.sessionPermissions {
		if p.ToolName != permission.ToolName || p.Action != permission.Action || p.Path != permission.Path {
			continue
		}
		// A persistent grant covers the session it was issued on and every
		// descendant session linked below it, so "allow always" on the main
		// conversation also covers subagents it spawns later.
		if s.walkSessionChain(permission.SessionID, func(id string) bool { return p.SessionID == id }) {
			return true
		}
	}

	respCh := make(chan bool, 1)

	s.pendingRequests.Store(permission.ID, respCh)
	defer s.pendingRequests.Delete(permission.ID)

	s.Publish(pubsub.CreatedEvent, permission)

	select {
	case resp := <-respCh:
		return resp
	case <-ctx.Done():
		return false
	}
}

func (s *permissionService) AutoApproveSession(sessionID string) {
	s.autoApproveSessions.Store(sessionID, true)
}

func (s *permissionService) RemoveAutoApproveSession(sessionID string) {
	s.autoApproveSessions.Delete(sessionID)
}

func (s *permissionService) IsAutoApproveSession(sessionID string) bool {
	return s.walkSessionChain(sessionID, func(id string) bool {
		_, ok := s.autoApproveSessions.Load(id)
		return ok
	})
}

func (s *permissionService) LinkSession(sessionID, parentSessionID string) {
	if sessionID == "" || parentSessionID == "" || sessionID == parentSessionID {
		return
	}
	s.sessionParents.Store(sessionID, parentSessionID)
}

// walkSessionChain calls visit for sessionID and each linked ancestor in
// turn, returning true at the first match. It stops on a missing link or a
// repeated ID, so a malformed link cycle (e.g. an agent resuming its own
// ancestor as a task) terminates instead of spinning.
func (s *permissionService) walkSessionChain(sessionID string, visit func(string) bool) bool {
	seen := make(map[string]struct{})
	for id := sessionID; id != ""; {
		if visit(id) {
			return true
		}
		if _, dup := seen[id]; dup {
			return false
		}
		seen[id] = struct{}{}
		parent, ok := s.sessionParents.Load(id)
		if !ok {
			return false
		}
		id, _ = parent.(string)
	}
	return false
}

func (s *permissionService) MarkInteractiveSession(sessionID string) {
	s.interactiveSessions.Store(sessionID, true)
}

func (s *permissionService) RemoveInteractiveSession(sessionID string) {
	s.interactiveSessions.Delete(sessionID)
}

func (s *permissionService) IsInteractiveSession(sessionID string) bool {
	_, ok := s.interactiveSessions.Load(sessionID)
	return ok
}

func NewPermissionService() Service {
	return &permissionService{
		Broker:             pubsub.NewBroker[PermissionRequest](),
		sessionPermissions: make([]PermissionRequest, 0),
	}
}

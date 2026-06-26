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
		if p.ToolName == permission.ToolName && p.Action == permission.Action && p.SessionID == permission.SessionID && p.Path == permission.Path {
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
	_, ok := s.autoApproveSessions.Load(sessionID)
	return ok
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

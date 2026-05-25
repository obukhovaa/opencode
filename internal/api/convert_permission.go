package api

import (
	"github.com/opencode-ai/opencode/internal/permission"
)

// ConvertPermissionRequest converts an internal permission.PermissionRequest
// to the API representation.
func ConvertPermissionRequest(p permission.PermissionRequest) APIPermissionRequest {
	return APIPermissionRequest{
		ID:          p.ID,
		SessionID:   p.SessionID,
		ToolName:    p.ToolName,
		Description: p.Description,
		Action:      p.Action,
		Params:      p.Params,
		Path:        p.Path,
	}
}

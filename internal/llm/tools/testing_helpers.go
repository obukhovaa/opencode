package tools

import (
	"context"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/permission"
)

// NewBashToolForTest returns a bash-tool handle that exposes the
// background path without going through the synchronous
// permission/safe-readonly checks. Intended for cmd/background-e2e.
//
// The handle is NOT a BaseTool — it has no Info / Run that exercises
// the synchronous bash path. Use the real bash tool for any test that
// needs the synchronous behavior.
func NewBashToolForTest() *BashTestHelper { return &BashTestHelper{} }

type BashTestHelper struct{}

func (h *BashTestHelper) RunBackgroundForTest(ctx context.Context, call ToolCall, params BashParams, workdir, sessionID string) (ToolResponse, error) {
	b := &bashTool{}
	return b.runBackground(ctx, call, params, workdir, sessionID)
}

// NewMonitorToolForTest returns a monitor tool wired with the supplied
// permission and registry services. The caller is responsible for setting
// up an auto-approve session so the permission gate passes.
func NewMonitorToolForTest(perm permission.Service, reg agentregistry.Registry) BaseTool {
	return &monitorTool{permissions: perm, registry: reg}
}

// NewTaskStopToolForTest returns a taskstop tool wired with the supplied
// permission and registry services.
func NewTaskStopToolForTest(perm permission.Service, reg agentregistry.Registry) BaseTool {
	return &taskstopTool{permissions: perm, registry: reg}
}

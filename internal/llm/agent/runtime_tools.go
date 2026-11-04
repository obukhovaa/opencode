package agent

import (
	"fmt"
	"sync"

	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// ReplaceToolset retains the agent's provider, prompts, subscriptions and session
// identity. The caller must exclude concurrent runs with WithIdleToolPolicy.
func ReplaceToolset(svc Service, stream <-chan tools.BaseTool) error {
	a, ok := svc.(*agent)
	if !ok {
		return fmt.Errorf("agent does not support live tool updates")
	}
	// Finish startup before replacing its once-only stream.
	a.resolveTools()
	a.toolsMu.Lock()
	a.toolsCh = stream
	a.toolsOnce = sync.Once{}
	a.toolsResolved.Store(false)
	a.toolsMu.Unlock()
	a.resolveTools()
	return nil
}

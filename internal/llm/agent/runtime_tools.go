package agent

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// ReplaceToolset retains the agent's provider, prompts and equivalent tool
// instances. The caller must exclude concurrent runs with WithIdleToolPolicy.
func ReplaceToolset(svc Service, stream <-chan tools.BaseTool) error {
	return replaceToolset(svc, stream, "")
}

// ReplaceMCPToolset refreshes one server without rebuilding unrelated dynamic
// tool descriptions or losing deferred discoveries. A changed server catalog or
// deferral policy still changes the provider prefix; we must not hide that by
// advertising tools the user disabled.
func ReplaceMCPToolset(svc Service, stream <-chan tools.BaseTool, server string) error {
	return replaceToolset(svc, stream, server+"_")
}

func replaceToolset(svc Service, stream <-chan tools.BaseTool, mcpPrefix string) error {
	a, ok := svc.(*agent)
	if !ok {
		return fmt.Errorf("agent does not support live tool updates")
	}
	previous := a.resolveTools()
	var incoming []tools.BaseTool
	for t := range stream {
		incoming = append(incoming, t)
	}
	next := reconcileTools(previous, incoming, mcpPrefix)
	ready := make(chan tools.BaseTool, len(next))
	for _, t := range next {
		ready <- t
	}
	close(ready)
	a.toolsMu.Lock()
	a.toolsCh = ready
	a.toolsOnce = sync.Once{}
	a.toolsResolved.Store(false)
	a.toolsMu.Unlock()
	a.resolveTools()
	return nil
}

func reconcileTools(previous, incoming []tools.BaseTool, mcpPrefix string) []tools.BaseTool {
	prior := make(map[string]tools.BaseTool, len(previous))
	for _, t := range previous {
		if _, exists := prior[t.Info().Name]; !exists {
			prior[t.Info().Name] = t
		}
	}
	for i, t := range incoming {
		old, exists := prior[t.Info().Name]
		if !exists {
			continue
		}
		_, oldDeferred := old.(*tools.DeferredWrapper)
		_, newDeferred := t.(*tools.DeferredWrapper)
		// MCP UI changes are scoped to that server. In particular router_send
		// embeds live peers in its description: a reconnect must not rewrite that
		// unrelated prefix. toolsearch itself may be newly added/removed below.
		unrelated := mcpPrefix != "" && !strings.HasPrefix(t.Info().Name, mcpPrefix) && t.Info().Name != tools.ToolSearchToolName
		equivalent := oldDeferred == newDeferred && old.IsBaseline() == t.IsBaseline() && reflect.DeepEqual(old.Info(), t.Info())
		if unrelated || equivalent {
			incoming[i] = old
		}
	}
	tools.RebindDeferredActivationSequence(incoming)
	return incoming
}

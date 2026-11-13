package agent

import (
	"context"
	"sort"
	"sync"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
)

type primaryFlowPrefixKey struct{}
type primaryFlowPrefix struct {
	mu                          sync.Mutex
	agentID, base, system, root string
	tools                       []tools.BaseTool
	activated                   []*tools.DeferredWrapper
}

// WithPrimaryFlowPrefix pins a generated run to its initiating primary's actual
// resolved request prefix. It runs off the UI thread after the planning turn has
// released its session. Per-step schemas remain message-tail payloads; a chosen
// specialist subagent receives its own normal prompt and toolset.
func WithPrimaryFlowPrefix(ctx context.Context, svc Service, root string) context.Context {
	a, ok := svc.(*agent)
	if !ok || a == nil {
		return ctx
	}
	p, ok := a.provider.(interface{ SystemMessage() string })
	if !ok {
		return ctx
	}
	prefix := &primaryFlowPrefix{root: root, agentID: string(a.AgentID()), base: a.basePrompt, system: p.SystemMessage(), tools: append([]tools.BaseTool(nil), a.resolveTools()...)}
	prefix.captureActivations(prefix.tools, root)
	return context.WithValue(ctx, primaryFlowPrefixKey{}, prefix)
}
func primaryFlowPrefixFrom(ctx context.Context, id string) *primaryFlowPrefix {
	p, _ := ctx.Value(primaryFlowPrefixKey{}).(*primaryFlowPrefix)
	if p != nil && p.agentID == id {
		return p
	}
	return nil
}
func withPrimaryFlowPrefix(p *primaryFlowPrefix) providerOption {
	return func(o *providerOptions) { o.primaryFlowPrefix = p }
}
func (p *primaryFlowPrefix) toolsFor(info *agentregistry.AgentInfo, model models.ModelID, allowStructured bool) <-chan tools.BaseTool {
	ch := make(chan tools.BaseTool, len(p.tools)+1)
	found := false
	for _, t := range p.tools {
		// These tools bind callbacks/tool slices to an agent instance. Their
		// wire definitions stay identical, but sharing the object would let a
		// step overwrite the ordinary agent's binding.
		switch t.(type) {
		case *tools.ToolSearchTool:
			t = tools.NewToolSearchTool()
		case *tools.CompactSessionTool:
			t = tools.NewCompactSessionTool(selfCompactSettings(info).MinTokens)
		}
		if t.Info().Name == tools.StructOutputToolName {
			found = true
			if schema, ok := ResolveOutputSchema(info); ok {
				t = tools.NewStructOutputToolWithDelivery(schema, ResolveSchemaDelivery(info, model))
			} else {
				t = tools.NewDormantStructOutputTool()
			}
		}
		ch <- t
	}
	if !found && allowStructured {
		if schema, ok := ResolveOutputSchema(info); ok {
			ch <- tools.NewStructOutputToolWithDelivery(schema, ResolveSchemaDelivery(info, model))
		}
	}
	close(ch)
	return ch
}

func (p *primaryFlowPrefix) activate(sessionID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	active := append([]*tools.DeferredWrapper(nil), p.activated...)
	p.mu.Unlock()
	for _, w := range active {
		w.Activate(sessionID)
	}
}

// Carry discoveries forward only within this run, never from unrelated sessions.
// The shared ordering prevents a subsequent fallback request from dropping a
// tool schema that an earlier step had already activated.
func (p *primaryFlowPrefix) captureActivations(toolSet []tools.BaseTool, sessionID string) {
	if p == nil {
		return
	}
	type discovered struct {
		w   *tools.DeferredWrapper
		seq int64
	}
	var found []discovered
	for _, t := range toolSet {
		if w, ok := t.(*tools.DeferredWrapper); ok {
			if seq, on := w.ActivatedAt(sessionID); on {
				found = append(found, discovered{w, seq})
			}
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].seq < found[j].seq })
	p.mu.Lock()
	seen := map[*tools.DeferredWrapper]bool{}
	for _, w := range p.activated {
		seen[w] = true
	}
	var added []*tools.DeferredWrapper
	for _, entry := range found {
		if !seen[entry.w] {
			p.activated = append(p.activated, entry.w)
			added = append(added, entry.w)
			seen[entry.w] = true
		}
	}
	p.mu.Unlock()
	// Preserve the learned tool tail when the owning ordinary conversation
	// resumes after the generated run. No other root is affected.
	for _, w := range added {
		w.Activate(p.root)
	}
}

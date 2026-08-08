package tools

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
)

// DeferredWrapper marks a tool's schema as deferred: providers omit it from
// (or flag it in) the request payload until it is discovered via tool search.
// It wraps any BaseTool and delegates everything; providers and the
// toolsearch tool recognize it by type assertion, so the BaseTool interface
// and its many implementations stay untouched.
//
// Activation state is PER SESSION. Primary agent instances live for the
// whole process and serve every session, so a process-global flag would
// leak activations across sessions and suppress announcements after the
// first session. The activation sequence number (from a counter shared by
// all wrappers of one toolset) records the order of activations within a
// session — the fallback path appends activated tools to the serialized
// list in exactly this order so previously sent tool positions never shift.
type DeferredWrapper struct {
	inner BaseTool
	seq   *atomic.Int64

	mu        sync.RWMutex
	activated map[string]int64 // sessionID -> activation sequence
}

// WrapDeferred wraps a tool as deferred. All wrappers of one toolset must
// share the same seq counter so activation order is globally consistent
// within the agent.
func WrapDeferred(inner BaseTool, seq *atomic.Int64) *DeferredWrapper {
	return &DeferredWrapper{
		inner:     inner,
		seq:       seq,
		activated: make(map[string]int64),
	}
}

// Activate marks the tool loaded for the session. Idempotent: repeated
// activation keeps the original sequence number.
func (w *DeferredWrapper) Activate(sessionID string) {
	if sessionID == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.activated[sessionID]; !ok {
		w.activated[sessionID] = w.seq.Add(1)
	}
}

// ActivatedAt returns the session's activation sequence for this tool, and
// whether it has been activated for that session at all.
func (w *DeferredWrapper) ActivatedAt(sessionID string) (int64, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	seq, ok := w.activated[sessionID]
	return seq, ok
}

// SerializableFor computes the fallback-path serialization order for a
// session: non-deferred tools in their given (OrderTools) order, then
// session-activated deferred tools appended in activation order.
// Non-activated deferred tools are omitted — fallback APIs reject calls to
// undeclared tools, and omitting is what saves the schema tokens. Appending
// (never inserting) keeps every previously serialized tool position stable
// so implicit prefix caches survive everything except the activation itself.
func SerializableFor(sessionID string, all []BaseTool) []BaseTool {
	type activated struct {
		tool BaseTool
		seq  int64
	}
	out := make([]BaseTool, 0, len(all))
	var tail []activated
	for _, t := range all {
		w, ok := t.(*DeferredWrapper)
		if !ok {
			out = append(out, t)
			continue
		}
		if seq, on := w.ActivatedAt(sessionID); on {
			tail = append(tail, activated{t, seq})
		}
	}
	sort.Slice(tail, func(i, j int) bool { return tail[i].seq < tail[j].seq })
	for _, a := range tail {
		out = append(out, a.tool)
	}
	return out
}

func (w *DeferredWrapper) Info() ToolInfo { return w.inner.Info() }

func (w *DeferredWrapper) Run(ctx context.Context, params ToolCall) (ToolResponse, error) {
	return w.inner.Run(ctx, params)
}

func (w *DeferredWrapper) AllowParallelism(call ToolCall, allCalls []ToolCall) bool {
	return w.inner.AllowParallelism(call, allCalls)
}

func (w *DeferredWrapper) IsBaseline() bool { return w.inner.IsBaseline() }

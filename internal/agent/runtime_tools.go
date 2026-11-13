package agent

import "maps"

// SetRuntimeTools publishes an immutable policy snapshot for future toolsets.
func SetRuntimeTools(reg Registry, id string, enabled, deferred map[string]bool) bool {
	r, ok := reg.(*registry)
	if !ok {
		return false
	}
	info, ok := r.Get(id)
	if !ok {
		return false
	}
	info.AllowTools = nil
	info.Tools = maps.Clone(enabled)
	info.DeferredTools = maps.Clone(deferred)
	r.runtimeTools.Store(id, info)
	return true
}

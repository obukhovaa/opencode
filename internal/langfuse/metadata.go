package langfuse

// NamespaceMetadata returns a copy of metadata with all keys prefixed by
// "namespace." when namespace is non-empty. When namespace is empty the
// original map is returned unchanged (default behaviour — flat keys).
//
// This allows custom (non-Langfuse-standard) metadata like flow_id,
// agent_id, flow_arg_* to be visually grouped under a common prefix
// in the Langfuse UI while remaining independently filterable as
// top-level metadata keys.
func NamespaceMetadata(metadata map[string]any, namespace string) map[string]any {
	if namespace == "" || len(metadata) == 0 {
		return metadata
	}
	prefix := namespace + "."
	out := make(map[string]any, len(metadata))
	for k, v := range metadata {
		out[prefix+k] = v
	}
	return out
}

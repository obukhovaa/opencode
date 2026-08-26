package agent

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/runidentity"
)

// TestResolveProviderAPIKey pins the rule that decides WHICH providers a
// run's key override replaces.
//
// The override must not be applied blindly: a config may hold a real
// vendor key next to the shared endpoint key, and re-keying the vendor
// one would ship a per-team LiteLLM token to an API that has never heard
// of it. The set is anchored on $LOCAL_ENDPOINT_API_KEY because that is
// exactly the set the per-Job pod's entrypoint substitution re-keys —
// which is the functional-parity property the pool path must preserve.
func TestResolveProviderAPIKey(t *testing.T) {
	const (
		sharedKey = "shared-endpoint-key"
		vendorKey = "sk-real-vendor-key"
		runKey    = "per-team-litellm-key"
	)
	tests := []struct {
		name       string
		sharedEnv  string
		override   string
		configured string
		want       string
	}{
		{
			name:       "no override leaves the configured key alone",
			sharedEnv:  sharedKey,
			configured: sharedKey,
			want:       sharedKey,
		},
		{
			name:       "override replaces a provider on the shared endpoint",
			sharedEnv:  sharedKey,
			override:   runKey,
			configured: sharedKey,
			want:       runKey,
		},
		{
			// The reason the rule is not "replace everything".
			name:       "override leaves an independently-keyed provider alone",
			sharedEnv:  sharedKey,
			override:   runKey,
			configured: vendorKey,
			want:       vendorKey,
		},
		{
			// Any deployment that is not endpoint-shared: the set is empty
			// and the override is inert rather than dangerous.
			name:       "no shared-endpoint env makes the override inert",
			sharedEnv:  "",
			override:   runKey,
			configured: vendorKey,
			want:       vendorKey,
		},
		{
			name:       "an unkeyed provider stays unkeyed",
			sharedEnv:  sharedKey,
			override:   runKey,
			configured: "",
			want:       "",
		},
		{
			// Guards the degenerate case where the env is set but empty:
			// an unkeyed provider must not match "" and get re-keyed.
			name:       "empty shared env does not match an unkeyed provider",
			sharedEnv:  "",
			override:   runKey,
			configured: "",
			want:       "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(sharedEndpointKeyEnv, tt.sharedEnv)
			if tt.override != "" {
				runidentity.Set(&runidentity.Identity{APIKey: tt.override})
				t.Cleanup(func() { runidentity.Set(nil) })
			} else {
				runidentity.Set(nil)
			}
			if got := resolveProviderAPIKey(tt.configured); got != tt.want {
				t.Errorf("resolveProviderAPIKey(%q) = %q, want %q", tt.configured, got, tt.want)
			}
		})
	}
}

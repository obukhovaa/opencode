package provider

import (
	"testing"

	"google.golang.org/genai"
)

// TestGeminiUsage pins the cache split: PromptTokenCount already includes
// CachedContentTokenCount, so the TokenUsage parts must add up to the
// request's real size. Counting the cached prefix in both InputTokens and
// CacheReadTokens billed it twice and doubled it in the session's reported
// usage, which the auto-compaction floor reads.
func TestGeminiUsage(t *testing.T) {
	cases := []struct {
		name      string
		meta      *genai.GenerateContentResponseUsageMetadata
		wantInput int64
		wantCache int64
	}{
		{"no cache", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 500, CandidatesTokenCount: 20}, 500, 0},
		{"mostly cached", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 500, CachedContentTokenCount: 480, CandidatesTokenCount: 20}, 20, 480},
		{"fully cached", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 500, CachedContentTokenCount: 500, CandidatesTokenCount: 20}, 0, 500},
		{"cache over prompt never goes negative", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CachedContentTokenCount: 30, CandidatesTokenCount: 20}, 0, 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := (&geminiClient{}).usage(&genai.GenerateContentResponse{UsageMetadata: tc.meta})
			if got.InputTokens != tc.wantInput || got.CacheReadTokens != tc.wantCache {
				t.Errorf("usage = %+v, want InputTokens %d, CacheReadTokens %d", got, tc.wantInput, tc.wantCache)
			}
			if tc.meta.CachedContentTokenCount > tc.meta.PromptTokenCount {
				return
			}
			sum := got.InputTokens + got.CacheCreationTokens + got.CacheReadTokens + got.OutputTokens
			if want := int64(tc.meta.PromptTokenCount + tc.meta.CandidatesTokenCount); sum != want {
				t.Errorf("Input+CacheCreation+CacheRead+Output = %d, want PromptTokenCount+CandidatesTokenCount = %d", sum, want)
			}
		})
	}
}

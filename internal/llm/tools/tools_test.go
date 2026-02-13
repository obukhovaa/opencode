package tools

import (
	"context"
	"strings"
	"testing"
)

func TestValidateAndTruncate(t *testing.T) {
	t.Run("small response not truncated", func(t *testing.T) {
		response := NewTextResponse("Hello, world!")
		if len(response.Content) != 13 {
			t.Errorf("expected length 13, got %d", len(response.Content))
		}
		if strings.Contains(response.Content, "[Output truncated") {
			t.Error("small response should not be truncated")
		}
	})

	t.Run("large response truncated", func(t *testing.T) {
		// Create content larger than MaxToolResponseTokens
		// 1.5M chars = ~375k tokens, which exceeds 300k limit
		largeContent := strings.Repeat("A", 1_500_000)
		response := NewTextResponse(largeContent)

		expectedMaxChars := MaxToolResponseTokens * 4
		if len(response.Content) <= expectedMaxChars {
			t.Errorf("expected content to be truncated to ~%d chars, got %d", expectedMaxChars, len(response.Content))
		}
		if !strings.Contains(response.Content, "[Output truncated") {
			t.Error("large response should contain truncation message")
		}
	})

	t.Run("error response also truncated", func(t *testing.T) {
		largeContent := strings.Repeat("Error: ", 200_000)
		response := NewTextErrorResponse(largeContent)

		if !response.IsError {
			t.Error("response should be marked as error")
		}
		if !strings.Contains(response.Content, "[Output truncated") {
			t.Error("large error response should be truncated")
		}
	})

	t.Run("empty response not truncated", func(t *testing.T) {
		response := NewEmptyResponse()
		if response.Content != "" {
			t.Errorf("expected empty content, got %q", response.Content)
		}
		if response.Type != ToolResponseTypeText {
			t.Errorf("expected text type, got %v", response.Type)
		}
	})

	t.Run("image response truncated not truncated (will be corrupted)", func(t *testing.T) {
		largeContent := strings.Repeat("base64data", 200_000)
		response := NewImageResponse(largeContent)

		if response.Type != ToolResponseTypeImage {
			t.Errorf("expected image type, got %v", response.Type)
		}
		if strings.Contains(response.Content, "[Output truncated") {
			t.Error("large image response should not be truncated")
		}
	})

	t.Run("response at exact limit not truncated", func(t *testing.T) {
		// Create content exactly at the limit
		exactContent := strings.Repeat("A", MaxToolResponseTokens*4)
		response := NewTextResponse(exactContent)

		if strings.Contains(response.Content, "[Output truncated") {
			t.Error("response at exact limit should not be truncated")
		}
	})

	t.Run("response just over limit truncated", func(t *testing.T) {
		// Create content just over the limit (need more than 4 chars to exceed 1 token)
		overContent := strings.Repeat("A", MaxToolResponseTokens*4+4)
		response := NewTextResponse(overContent)

		if !strings.Contains(response.Content, "[Output truncated") {
			t.Error("response just over limit should be truncated")
		}
	})
}

func TestIsTaskAgent(t *testing.T) {
	t.Run("returns false for empty context", func(t *testing.T) {
		ctx := context.Background()
		if IsTaskAgent(ctx) {
			t.Error("expected false for empty context")
		}
	})

	t.Run("returns true when context has task agent flag", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), IsTaskAgentContextKey, true)
		if !IsTaskAgent(ctx) {
			t.Error("expected true when context has task agent flag")
		}
	})

	t.Run("returns false when context has false flag", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), IsTaskAgentContextKey, false)
		if IsTaskAgent(ctx) {
			t.Error("expected false when context has false flag")
		}
	})

	t.Run("returns false when context has wrong type", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), IsTaskAgentContextKey, "not a bool")
		if IsTaskAgent(ctx) {
			t.Error("expected false when context has wrong type")
		}
	})
}

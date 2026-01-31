package db

import (
	"testing"
)

func TestNormalizeGitURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "HTTPS URL with .git suffix",
			input:    "https://github.com/opencode-ai/opencode.git",
			expected: "github.com/opencode-ai/opencode",
		},
		{
			name:     "HTTPS URL without .git suffix",
			input:    "https://gitlab.com/myteam/myproject",
			expected: "gitlab.com/myteam/myproject",
		},
		{
			name:     "SSH URL with .git suffix",
			input:    "git@github.com:opencode-ai/opencode.git",
			expected: "github.com/opencode-ai/opencode",
		},
		{
			name:     "SSH URL without .git suffix",
			input:    "git@gitlab.com:myteam/myproject",
			expected: "gitlab.com/myteam/myproject",
		},
		{
			name:     "HTTP URL with .git suffix",
			input:    "http://github.com/opencode-ai/opencode.git",
			expected: "github.com/opencode-ai/opencode",
		},
		{
			name:     "URL with trailing slash",
			input:    "https://github.com/opencode-ai/opencode/",
			expected: "github.com/opencode-ai/opencode",
		},
		{
			name:     "URL with trailing slash and .git",
			input:    "https://github.com/opencode-ai/opencode.git/",
			expected: "github.com/opencode-ai/opencode",
		},
		{
			name:     "Plain URL without protocol",
			input:    "github.com/opencode-ai/opencode",
			expected: "github.com/opencode-ai/opencode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeGitURL(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeGitURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGetProjectIDFromDirectory(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Unix path",
			input:    "/Users/john/projects/my-app",
			expected: "my-app",
		},
		{
			name:     "Unix path with trailing slash",
			input:    "/Users/john/projects/my-app/",
			expected: "my-app",
		},
		{
			name:     "Relative path",
			input:    "./my-app",
			expected: "my-app",
		},
		{
			name:     "Single directory",
			input:    "my-app",
			expected: "my-app",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getProjectIDFromDirectory(tt.input)
			if result != tt.expected {
				t.Errorf("getProjectIDFromDirectory(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

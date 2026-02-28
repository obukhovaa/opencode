package dialog

import (
	"regexp"
	"testing"
)

func TestNamedArgPattern(t *testing.T) {
	testCases := []struct {
		input    string
		expected []string
	}{
		{
			input:    "This is a test with $ARGUMENTS placeholder",
			expected: []string{"ARGUMENTS"},
		},
		{
			input:    "This is a test with $FOO and $BAR placeholders",
			expected: []string{"FOO", "BAR"},
		},
		{
			input:    "This is a test with $FOO_BAR and $BAZ123 placeholders",
			expected: []string{"FOO_BAR", "BAZ123"},
		},
		{
			input:    "This is a test with no placeholders",
			expected: []string{},
		},
		{
			input:    "This is a test with $FOO appearing twice: $FOO",
			expected: []string{"FOO"},
		},
		{
			input:    "This is a test with $1INVALID placeholder",
			expected: []string{},
		},
	}

	for _, tc := range testCases {
		matches := namedArgPattern.FindAllStringSubmatch(tc.input, -1)

		// Extract unique argument names
		argNames := make([]string, 0)
		argMap := make(map[string]bool)

		for _, match := range matches {
			argName := match[1] // Group 1 is the name without $
			if !argMap[argName] {
				argMap[argName] = true
				argNames = append(argNames, argName)
			}
		}

		// Check if we got the expected number of arguments
		if len(argNames) != len(tc.expected) {
			t.Errorf("Expected %d arguments, got %d for input: %s", len(tc.expected), len(argNames), tc.input)
			continue
		}

		// Check if we got the expected argument names
		for _, expectedArg := range tc.expected {
			found := false
			for _, actualArg := range argNames {
				if actualArg == expectedArg {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Expected argument %s not found in %v for input: %s", expectedArg, argNames, tc.input)
			}
		}
	}
}

func TestRegexPattern(t *testing.T) {
	pattern := regexp.MustCompile(`\$([A-Z][A-Z0-9_]*)`)

	validMatches := []string{
		"$FOO",
		"$BAR",
		"$FOO_BAR",
		"$BAZ123",
		"$ARGUMENTS",
	}

	invalidMatches := []string{
		"$foo",
		"$1BAR",
		"$_FOO",
		"FOO",
		"$",
	}

	for _, valid := range validMatches {
		if !pattern.MatchString(valid) {
			t.Errorf("Expected %s to match, but it didn't", valid)
		}
	}

	for _, invalid := range invalidMatches {
		if pattern.MatchString(invalid) {
			t.Errorf("Expected %s not to match, but it did", invalid)
		}
	}
}

func TestParseCommandMarkdown(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantTitle string
		wantDesc  string
		wantBody  string
	}{
		{
			name:      "no frontmatter",
			input:     "Just a plain command body",
			wantTitle: "",
			wantDesc:  "",
			wantBody:  "Just a plain command body",
		},
		{
			name:      "with frontmatter",
			input:     "---\ntitle: My Command\ndescription: Does cool stuff\n---\nThe body content",
			wantTitle: "My Command",
			wantDesc:  "Does cool stuff",
			wantBody:  "The body content",
		},
		{
			name:      "frontmatter title only",
			input:     "---\ntitle: Title Only\n---\nBody here",
			wantTitle: "Title Only",
			wantDesc:  "",
			wantBody:  "Body here",
		},
		{
			name:      "frontmatter description only",
			input:     "---\ndescription: A description\n---\nBody here",
			wantTitle: "",
			wantDesc:  "A description",
			wantBody:  "Body here",
		},
		{
			name:      "empty body after frontmatter",
			input:     "---\ntitle: No Body\n---\n",
			wantTitle: "No Body",
			wantDesc:  "",
			wantBody:  "",
		},
		{
			name:      "incomplete frontmatter treated as body",
			input:     "---\ntitle: Broken\nno closing delimiter",
			wantTitle: "",
			wantDesc:  "",
			wantBody:  "---\ntitle: Broken\nno closing delimiter",
		},
		{
			name:      "body with newlines after frontmatter",
			input:     "---\ntitle: Test\n---\n\n\nBody after blank lines",
			wantTitle: "Test",
			wantDesc:  "",
			wantBody:  "Body after blank lines",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, body := parseCommandMarkdown([]byte(tt.input))
			if fm.Title != tt.wantTitle {
				t.Errorf("title = %q, want %q", fm.Title, tt.wantTitle)
			}
			if fm.Description != tt.wantDesc {
				t.Errorf("description = %q, want %q", fm.Description, tt.wantDesc)
			}
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

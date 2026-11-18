package tools

import (
	"context"
	"slices"
	"testing"
)

func TestWithDeniedTools(t *testing.T) {
	base := context.Background()
	if ctx := WithDeniedTools(base, []string{" ", ""}); ctx != base {
		t.Fatal("an all-blank list must leave ctx unchanged")
	}
	ctx := WithDeniedTools(base, []string{" gitlab_* ", "", "bash"})
	if got := DeniedTools(ctx); !slices.Equal(got, []string{"gitlab_*", "bash"}) {
		t.Fatalf("DeniedTools = %q", got)
	}
	if DeniedTools(base) != nil {
		t.Fatal("a bare ctx must carry no denied tools")
	}
}

func TestIsToolDeniedBy(t *testing.T) {
	globs := []string{"gitlab_create_*", "Router_Send", "jarvis-memory_*"}
	tests := []struct {
		name string
		want bool
	}{
		{"gitlab_create_merge_request_note", true},
		{"GITLAB_CREATE_NOTE", true},
		{"router_send", true},
		{"jarvis-memory_append", true},
		{"gitlab_get_merge_request", false},
		{"read", false},
		{StructOutputToolName, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsToolDeniedBy(globs, tt.name); got != tt.want {
				t.Fatalf("IsToolDeniedBy(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
	if IsToolDeniedBy([]string{"*"}, StructOutputToolName) {
		t.Fatal("a catch-all must not deny struct_output")
	}
	if !IsToolDenied(WithDeniedTools(context.Background(), []string{"*"}), "bash") {
		t.Fatal("IsToolDenied ignored the ctx globs")
	}
}

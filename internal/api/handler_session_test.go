package api

import "testing"

func TestShouldAutoApprove(t *testing.T) {
	tests := []struct {
		name  string
		rules []APIPermissionRule
		want  bool
	}{
		{"nil", nil, false},
		{"empty", []APIPermissionRule{}, false},
		{
			"wildcard allow",
			[]APIPermissionRule{{Permission: "*", Pattern: "*", Action: "allow"}},
			true,
		},
		{
			"wildcard deny",
			[]APIPermissionRule{{Permission: "*", Pattern: "*", Action: "deny"}},
			false,
		},
		{
			"wildcard ask",
			[]APIPermissionRule{{Permission: "*", Pattern: "*", Action: "ask"}},
			false,
		},
		{
			"specific allow not honored",
			[]APIPermissionRule{{Permission: "bash", Pattern: "git *", Action: "allow"}},
			false,
		},
		{
			"wildcard allow among other rules",
			[]APIPermissionRule{
				{Permission: "bash", Pattern: "rm -rf *", Action: "deny"},
				{Permission: "*", Pattern: "*", Action: "allow"},
			},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldAutoApprove(tt.rules); got != tt.want {
				t.Fatalf("shouldAutoApprove() = %v, want %v", got, tt.want)
			}
		})
	}
}

package app

import "testing"

// externalJobID prefers the general name so any supervisor can supply a job id
// without depending on the bridge subsystem, while still honouring the
// bridge-scoped variable for deployments that already set it.
func TestExternalJobIDPrefersGeneralName(t *testing.T) {
	tests := []struct {
		name   string
		jobID  string
		bridge string
		want   string
	}{
		{"neither set — standalone run, no fabricated id", "", "", ""},
		{"only the bridge-scoped name — honoured for compatibility", "", "bridge-1", "bridge-1"},
		{"only the general name", "job-1", "", "job-1"},
		{"both set — the general name wins", "job-1", "bridge-1", "job-1"},
		{"general name empty falls through", "", "bridge-1", "bridge-1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENCODE_JOB_ID", tc.jobID)
			t.Setenv("OPENCODE_BRIDGE_JOB_ID", tc.bridge)
			if got := externalJobID(); got != tc.want {
				t.Errorf("externalJobID() = %q, want %q", got, tc.want)
			}
		})
	}
}

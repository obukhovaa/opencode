package service

import (
	"context"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/cron"
)

// TestCronOutputRouterSkipsEmptyAndUnbound verifies that handleUpdate
// is a no-op for the two early-exit conditions: an UpdatedEvent with
// no fresh run (LastRunAt==0 OR LastResult=="") and a session that has
// no bridge bindings. Both must return without touching the adapter
// path — there's no adapter wired in the test harness, so any forward
// attempt would log "no adapter" but not crash. We just confirm the
// method doesn't panic or error on the skip paths.
func TestCronOutputRouterSkipsEmptyAndUnbound(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	r := &CronOutputRouter{svc: svc}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cases := []struct {
		name string
		job  cron.CronJob
	}{
		{
			name: "zero LastRunAt skipped",
			job:  cron.CronJob{ID: "c1", SessionID: "S1", LastResult: "ok"},
		},
		{
			name: "empty LastResult skipped",
			job:  cron.CronJob{ID: "c2", SessionID: "S1", LastRunAt: time.Now().Unix()},
		},
		{
			name: "unbound session skipped",
			job: cron.CronJob{
				ID: "c3", SessionID: "not-bound",
				LastRunAt: time.Now().Unix(), LastResult: "ok",
			},
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			// Must not panic; nothing else to assert without a
			// recording adapter — the forwarded-message path is
			// covered by integration runs.
			r.handleUpdate(ctx, c.job)
		})
	}
}

func TestStripTaskTrailer(t *testing.T) {
	t.Parallel()
	trailer := "\n\n<task_id>toolu_abc</task_id>\n<task_resume_hint>To continue this same subagent session later, call the task tool again with task_id=\"toolu_abc\". Mention this task_id (alongside a short description of what was done) in your reply so the user can reference or resume it.</task_resume_hint>"
	cases := []struct{ name, in, want string }{
		{"trailer stripped", "Nothing new, 3 pipelines green." + trailer, "Nothing new, 3 pipelines green."},
		{"multi-line body kept", "**Waiting on you**\n- item one\n- item two" + trailer, "**Waiting on you**\n- item one\n- item two"},
		{"no trailer unchanged", "plain result", "plain result"},
		{"task_id alone is not a trailer", "see <task_id>x</task_id> above", "see <task_id>x</task_id> above"},
	}
	for _, tc := range cases {
		if got := stripTaskTrailer(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

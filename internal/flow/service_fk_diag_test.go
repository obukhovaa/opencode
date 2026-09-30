package flow

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/opencode-ai/opencode/internal/db"
)

// fkFailingQuerier fails every CreateFlowState with the MySQL FK error a
// concurrent run's fresh-start wipe produces when it deletes the step session
// between resolveSession and the entry-time flow_states write.
type fkFailingQuerier struct {
	*stubQuerier
}

func (q *fkFailingQuerier) CreateFlowState(_ context.Context, _ db.CreateFlowStateParams) (db.FlowState, error) {
	return db.FlowState{}, &mysql.MySQLError{
		Number:  1452,
		Message: "Cannot add or update a child row: a foreign key constraint fails (`opencode`.`flow_states`, CONSTRAINT `fk_flow_states_session` FOREIGN KEY (`session_id`) REFERENCES `sessions` (`id`) ON DELETE CASCADE)",
	}
}

func (q *fkFailingQuerier) WithTx(_ *sql.Tx) db.QuerierWithTx { return q }

func TestRunStepNamesMissingSessionOnFlowStateFK(t *testing.T) {
	registerTestFlow(t, Flow{
		ID:   "test-fk-diag",
		Name: "Test FK Diag",
		Spec: FlowSpec{Steps: []Step{{ID: "step-one", Prompt: "do something"}}},
	})

	q := &fkFailingQuerier{stubQuerier: &stubQuerier{}}
	agent := newStubAgent()
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", "test-fk-diag", map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	go func() {
		for range agentEvents {
		}
	}()

	var failed *FlowState
	for s := range flowStates {
		if s.Status == FlowStatusFailed {
			failed = s
		}
	}
	if failed == nil {
		t.Fatal("expected a failed flow state")
	}
	for _, want := range []string{"persisting flow state", "session prefix-test-fk-diag-step-one no longer exists", "concurrent run", "fk_flow_states_session"} {
		if !strings.Contains(failed.Output, want) {
			t.Errorf("failed output %q missing %q", failed.Output, want)
		}
	}
	if agent.callCount() != 0 {
		t.Errorf("agent ran %d times, want 0 (step must fail before running, not re-create and retry)", agent.callCount())
	}
}

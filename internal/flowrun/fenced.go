package flowrun

import (
	"context"
	"database/sql"

	"github.com/opencode-ai/opencode/internal/db"
)

type fencedQuerier struct {
	db.QuerierWithTx
	store *Store
	lease Lease
	tx    *sql.Tx
}

// FencedQuerier keeps the ownership check and each flow-state mutation in the
// SAME transaction, locking the lease row until commit. A preflight ownership
// query followed by an unrelated state write would permit stale-owner writes.
func (s *Store) FencedQuerier(l Lease) db.QuerierWithTx {
	return &fencedQuerier{QuerierWithTx: s.queries, store: s, lease: l}
}
func (q *fencedQuerier) WithTx(tx *sql.Tx) db.QuerierWithTx {
	return &fencedQuerier{QuerierWithTx: q.store.queries.WithTx(tx), store: q.store, lease: q.lease, tx: tx}
}
func (q *fencedQuerier) write(ctx context.Context, fn func(db.QuerierWithTx) error) error {
	guarded := func(tx *sql.Tx) error {
		if err := q.store.fence(ctx, tx, q.lease); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRowContext(ctx, "SELECT status FROM conversation_flow_runs WHERE project_id=? AND id=?", q.store.projectID, q.lease.RunID).Scan(&status); err != nil {
			return err
		}
		if terminal(status) {
			return ErrLeaseLost
		}
		return fn(q.store.queries.WithTx(tx))
	}
	if q.tx != nil {
		return guarded(q.tx)
	}
	return q.store.transaction(ctx, guarded)
}
func (q *fencedQuerier) CreateFlowState(ctx context.Context, arg db.CreateFlowStateParams) (db.FlowState, error) {
	if arg.RootSessionID != q.lease.RunID {
		return db.FlowState{}, ErrConflict
	}
	var out db.FlowState
	err := q.write(ctx, func(txq db.QuerierWithTx) error { var err error; out, err = txq.CreateFlowState(ctx, arg); return err })
	return out, err
}
func (q *fencedQuerier) UpdateFlowState(ctx context.Context, arg db.UpdateFlowStateParams) (db.FlowState, error) {
	var out db.FlowState
	err := q.write(ctx, func(txq db.QuerierWithTx) error {
		old, err := txq.GetFlowState(ctx, arg.SessionID)
		if err != nil {
			return err
		}
		if old.RootSessionID != q.lease.RunID {
			return ErrConflict
		}
		out, err = txq.UpdateFlowState(ctx, arg)
		return err
	})
	return out, err
}
func (q *fencedQuerier) DeleteFlowStatesByRootSession(ctx context.Context, root string) error {
	if root != q.lease.RunID {
		return ErrConflict
	}
	return q.write(ctx, func(txq db.QuerierWithTx) error { return txq.DeleteFlowStatesByRootSession(ctx, root) })
}

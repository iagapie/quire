package app

import (
	"context"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/iagapie/quire/internal/query"
)

// EventConfirmQuit asks the frontend to confirm quitting with open transactions.
const EventConfirmQuit = "app:confirm-quit"

func init() {
	application.RegisterEvent[[]string](EventConfirmQuit)
}

// Queries runs SQL for editor tabs.
type Queries struct {
	conns  *Connections
	engine *query.Engine
	force  atomic.Bool
}

// NewQueries creates the service.
func NewQueries(conns *Connections, engine *query.Engine) *Queries {
	q := &Queries{conns: conns, engine: engine}
	engine.OnRetry = func(opID string, attempt int, err error) {
		emit(EventRetry, RetryEvent{OpID: opID, Attempt: attempt, Error: err.Error()})
	}
	return q
}

// Run executes a script (or part of it) in a tab. opID identifies the operation so it
// can be cancelled with Cancel.
func (q *Queries) Run(ctx context.Context, sessionID, tabID, opID string, req query.RunRequest) (query.RunResult, error) {
	s, err := q.conns.get(sessionID)
	if err != nil {
		return query.RunResult{}, err
	}
	return q.engine.Run(ctx, s, tabID, opID, req), nil
}

// Cancel aborts a running operation.
func (q *Queries) Cancel(sessionID, opID string) bool {
	s, err := q.conns.get(sessionID)
	if err != nil {
		return false
	}
	return s.Cancel(opID)
}

// FetchPage returns a page of a result, without running the script again.
func (q *Queries) FetchPage(ctx context.Context, sessionID, tabID, resultID, opID string, offset, size int64) (query.Page, error) {
	s, err := q.conns.get(sessionID)
	if err != nil {
		return query.Page{}, err
	}
	return q.engine.FetchPage(ctx, s, tabID, resultID, opID, offset, size)
}

// Count counts the rows of a result.
func (q *Queries) Count(ctx context.Context, sessionID, tabID, resultID, opID string) (int64, error) {
	s, err := q.conns.get(sessionID)
	if err != nil {
		return 0, err
	}
	return q.engine.Count(ctx, s, tabID, resultID, opID)
}

// Requery filters, sorts and limits a single-table result again.
func (q *Queries) Requery(ctx context.Context, sessionID, tabID, resultID, opID, where, orderBy string, limit, pageSize int64) (query.Result, error) {
	s, err := q.conns.get(sessionID)
	if err != nil {
		return query.Result{}, err
	}
	return q.engine.Requery(ctx, s, tabID, resultID, opID, where, orderBy, limit, pageSize)
}

// PlanEdits returns the statements an edit would run, for the preview.
func (q *Queries) PlanEdits(sessionID string, req query.EditRequest) ([]query.EditStatement, error) {
	s, err := q.conns.get(sessionID)
	if err != nil {
		return nil, err
	}
	return query.PlanEdits(s.Driver, req)
}

// ApplyEdits applies row changes in one transaction and returns how many ran.
func (q *Queries) ApplyEdits(ctx context.Context, sessionID, tabID string, req query.EditRequest) (int, error) {
	s, err := q.conns.get(sessionID)
	if err != nil {
		return 0, err
	}
	return q.engine.ApplyEdits(ctx, s, tabID, req)
}

// SetManualTransaction switches a tab between auto-commit and a manual transaction.
func (q *Queries) SetManualTransaction(sessionID, tabID string, manual bool) (query.TxState, error) {
	s, err := q.conns.get(sessionID)
	if err != nil {
		return query.TxState{}, err
	}
	return q.engine.SetManual(s, tabID, manual)
}

// Commit commits the transaction of a tab.
func (q *Queries) Commit(ctx context.Context, sessionID, tabID string) (query.TxState, error) {
	s, err := q.conns.get(sessionID)
	if err != nil {
		return query.TxState{}, err
	}
	return q.engine.Commit(ctx, s, tabID)
}

// Rollback rolls back the transaction of a tab.
func (q *Queries) Rollback(ctx context.Context, sessionID, tabID string) (query.TxState, error) {
	s, err := q.conns.get(sessionID)
	if err != nil {
		return query.TxState{}, err
	}
	return q.engine.Rollback(ctx, s, tabID)
}

// TransactionState returns the transaction state of a tab.
func (q *Queries) TransactionState(tabID string) query.TxState {
	return q.engine.State(tabID)
}

// CloseTab releases the connection of a tab (rolling back an open transaction).
func (q *Queries) CloseTab(tabID string) {
	q.engine.CloseTab(tabID)
}

// OpenTransactions lists the tabs with a transaction in progress.
func (q *Queries) OpenTransactions() []string {
	return q.engine.OpenTransactions()
}

// ShouldQuit lets the application quit unless a transaction is open; then the
// frontend is asked to confirm.
func (q *Queries) ShouldQuit() bool {
	if q.force.Load() {
		return true
	}
	if open := q.engine.OpenTransactions(); len(open) > 0 {
		emit(EventConfirmQuit, open)
		return false
	}
	return true
}

// Quit quits the application even with open transactions (rolled back on exit).
func (q *Queries) Quit() {
	q.force.Store(true)
	if a := application.Get(); a != nil {
		a.Quit()
	}
}

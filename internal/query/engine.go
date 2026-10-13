// Package query runs SQL for editor tabs.
//
// Every tab owns a dedicated connection, taken from the pool on first use, so session
// state (SET, temporary tables, the current schema, a transaction) carries over from
// one run to the next, as in a terminal. A result whose rows do not fit a page keeps
// its cursor open on that connection and later pages are read from it: paging never
// re-runs the script. The connection is released when the tab closes, when the result
// is replaced, or after a period of inactivity; if the cursor is gone (or the network
// dropped it) a page is rebuilt by running only that SELECT, wrapped with the
// dialect's LIMIT/OFFSET.
package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/session"
)

// Tunables, variables for tests.
var (
	// CursorIdle closes an open cursor (releasing the rows it holds) after this long.
	CursorIdle = 10 * time.Minute
	// ConnIdle releases the connection of an idle tab without a transaction.
	ConnIdle = 30 * time.Minute
	// MaxCachedRows bounds the rows a result keeps in memory.
	MaxCachedRows = 50_000
	// MaxCachedBytes bounds the memory of the rows a result keeps.
	MaxCachedBytes = 128 << 20
	// MaxReadRows bounds the rows read from a statement that cannot be paged.
	MaxReadRows = 100_000
	// preflightAfter checks a tab connection that has been idle this long before use.
	preflightAfter = 20 * time.Second
	janitorEvery   = 30 * time.Second
	// interruptGrace is how long an interrupted statement may take to stop before its
	// connection is closed.
	interruptGrace = 5 * time.Second
	// drainTimeout bounds the reading of what is left of a cursor that is closed.
	drainTimeout = time.Second
	// interruptWait is how long the interruption itself may take.
	interruptWait = 1500 * time.Millisecond
)

// Engine holds the tabs of every session.
type Engine struct {
	// OnRetry is called when an operation is retried after a transient error.
	OnRetry func(opID string, attempt int, err error)

	mu      sync.Mutex
	tabs    map[string]*Tab
	results map[string]*result
	stop    chan struct{}
	once    sync.Once
}

// New creates an engine and starts its janitor.
func New() *Engine {
	e := &Engine{tabs: map[string]*Tab{}, results: map[string]*result{}, stop: make(chan struct{})}
	go e.janitor()
	return e
}

// Close releases every tab.
func (e *Engine) Close() {
	e.once.Do(func() { close(e.stop) })
	e.mu.Lock()
	tabs := make([]*Tab, 0, len(e.tabs))
	for _, t := range e.tabs {
		tabs = append(tabs, t)
	}
	e.tabs = map[string]*Tab{}
	e.results = map[string]*result{}
	e.mu.Unlock()
	for _, t := range tabs {
		t.release(context.Background())
	}
}

// Tab is the server-side state of an editor tab.
type Tab struct {
	ID string

	// mu serializes everything done on the tab connection.
	mu   sync.Mutex
	sess *session.Session
	conn *sql.Conn
	// sid is the server id of conn, for drivers that interrupt statements by id.
	sid string
	// pool is the database the connection belongs to (per-database drivers).
	pool string
	// database and schema are what the connection currently uses.
	database string
	schema   string
	used     time.Time
	// manual is set when the tab runs in a manual transaction; tx is the open one.
	manual bool
	tx     *sql.Tx
	// txStatements counts the statements run in the open transaction.
	txStatements int
	// implicitTx is set when a script left the connection inside a transaction.
	implicitTx bool
	// stateful is set once a statement changed session state (SET, temporary table...).
	stateful bool
	// lost explains why session state was lost, until the next run reports it.
	lost string
	// partition is the partition key the connection's queries are limited to.
	partition string
	// cursor is the open result holding the connection, if any.
	cursor *result
}

// TxState is the transaction state of a tab, for the UI.
type TxState struct {
	// Manual is the tab's transaction mode.
	Manual bool `json:"manual"`
	// Open tells a transaction is in progress (manual, or started by a statement).
	Open       bool `json:"open"`
	Statements int  `json:"statements"`
}

func (t *Tab) txState() TxState {
	return TxState{Manual: t.manual, Open: t.tx != nil || t.implicitTx, Statements: t.txStatements}
}

// tab returns the tab, creating it for s when needed. A tab whose session was
// replaced (reconnected) starts over.
func (e *Engine) tab(id string, s *session.Session) *Tab {
	e.mu.Lock()
	t, ok := e.tabs[id]
	if !ok {
		t = &Tab{ID: id, sess: s}
		e.tabs[id] = t
	}
	e.mu.Unlock()
	t.mu.Lock()
	if t.sess != s {
		t.dropConnLocked("the connection was closed and opened again")
		t.sess = s
		t.tx, t.implicitTx, t.txStatements = nil, false, 0
	}
	t.mu.Unlock()
	return t
}

func (e *Engine) existing(id string) (*Tab, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t, ok := e.tabs[id]
	return t, ok
}

// CloseTab releases the tab connection, rolling back an open transaction.
func (e *Engine) CloseTab(id string) {
	e.mu.Lock()
	t, ok := e.tabs[id]
	delete(e.tabs, id)
	for rid, r := range e.results {
		if r.tabID == id {
			delete(e.results, rid)
		}
	}
	e.mu.Unlock()
	if ok {
		t.release(context.Background())
	}
}

// DropSession releases the tabs of a closed session.
func (e *Engine) DropSession(s *session.Session) {
	e.mu.Lock()
	var tabs []*Tab
	for _, t := range e.tabs {
		if t.sess == s {
			tabs = append(tabs, t)
		}
	}
	e.mu.Unlock()
	for _, t := range tabs {
		t.mu.Lock()
		t.closeCursorLocked()
		if t.tx != nil {
			_ = t.tx.Rollback()
			t.tx = nil
		}
		t.dropConnLocked("")
		t.mu.Unlock()
	}
}

// OpenTransactions lists tabs with a transaction in progress.
func (e *Engine) OpenTransactions() []string {
	e.mu.Lock()
	tabs := make([]*Tab, 0, len(e.tabs))
	for _, t := range e.tabs {
		tabs = append(tabs, t)
	}
	e.mu.Unlock()
	var out []string
	for _, t := range tabs {
		t.mu.Lock()
		if t.tx != nil || t.implicitTx {
			out = append(out, t.ID)
		}
		t.mu.Unlock()
	}
	return out
}

func (t *Tab) release(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeCursorLocked()
	if t.tx != nil {
		_ = t.tx.Rollback()
		t.tx = nil
	}
	if t.conn != nil && t.implicitTx {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, _ = t.conn.ExecContext(rctx, "ROLLBACK")
		cancel()
	}
	t.dropConnLocked("")
}

// dropConnLocked closes the connection; reason, when set and the session had state,
// is reported to the user on the next run.
func (t *Tab) dropConnLocked(reason string) {
	if t.conn == nil {
		return
	}
	t.closeCursorLocked()
	if reason != "" && (t.stateful || t.tx != nil || t.implicitTx) {
		t.lost = reason
	}
	_ = t.conn.Close()
	t.conn, t.sid = nil, ""
	t.database, t.schema, t.pool, t.partition = "", "", "", ""
	t.stateful, t.implicitTx = false, false
	if t.tx != nil {
		t.tx = nil
		t.txStatements = 0
	}
}

// janitor closes idle cursors and connections.
func (e *Engine) janitor() {
	tick := time.NewTicker(janitorEvery)
	defer tick.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-tick.C:
			e.sweep(time.Now())
		}
	}
}

func (e *Engine) sweep(now time.Time) {
	e.mu.Lock()
	tabs := make([]*Tab, 0, len(e.tabs))
	for _, t := range e.tabs {
		tabs = append(tabs, t)
	}
	e.mu.Unlock()
	for _, t := range tabs {
		if !t.mu.TryLock() {
			continue // busy
		}
		if t.cursor != nil && now.Sub(t.cursor.used) > CursorIdle {
			t.closeCursorLocked()
		}
		if t.conn != nil && t.tx == nil && !t.implicitTx && now.Sub(t.used) > ConnIdle {
			t.dropConnLocked("the connection was released after " + ConnIdle.String() + " of inactivity")
		}
		t.mu.Unlock()
	}
}

// acquire makes sure the tab has a live connection on database/schema. It returns a
// notice when session state was lost on the way.
func (t *Tab) acquire(ctx context.Context, database, schema string) (string, error) {
	s := t.sess
	pool := ""
	if pd, ok := s.Driver.(interface{ PerDatabasePools() bool }); ok && pd.PerDatabasePools() && database != "" && database != s.Database {
		pool = database
	}
	if t.conn != nil && pool != t.pool {
		if t.tx != nil || t.implicitTx {
			return "", errors.New("commit or roll back the transaction before switching to another database")
		}
		t.dropConnLocked("")
	}
	// A connection idle for a while may have died with the network: check it before
	// running anything, so a write is not sent into a dead socket.
	if t.conn != nil && time.Since(t.used) > preflightAfter && t.cursor == nil && !dialect.IsStateless(s.Driver) {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := t.conn.PingContext(pctx)
		cancel()
		if err != nil {
			t.dropConnLocked("the connection to the server was lost")
		}
	}
	if t.conn == nil {
		var conn *sql.Conn
		_, err := session.RetryRead(ctx, s, nil, func(ctx context.Context) (struct{}, error) {
			db, err := s.DB(ctx, pool)
			if err != nil {
				return struct{}{}, err
			}
			conn, err = db.Conn(ctx)
			if err == nil {
				err = conn.PingContext(ctx)
				if err != nil {
					_ = conn.Close()
				}
			}
			return struct{}{}, err
		})
		if err != nil {
			return "", err
		}
		t.conn, t.pool = conn, pool
		t.database, t.schema = "", ""
		if ir, ok := s.Driver.(dialect.Interrupter); ok {
			if id, err := ir.SessionID(ctx, conn); err == nil {
				t.sid = id
			}
		}
		if so, ok := s.Driver.(dialect.ServerOutput); ok {
			_ = so.EnableOutput(ctx, conn)
		}
	}
	t.used = time.Now()
	notice := ""
	if t.lost != "" {
		notice = "Session state was reset: " + t.lost + ". SET values, temporary tables and any open transaction were lost."
		t.lost = ""
	}
	if database != t.database || schema != t.schema {
		if t.database == "" && t.schema == "" && (database == "" || database == s.Database) && (schema == "" || schema == s.Schema) {
			t.database, t.schema = database, schema
			return notice, nil
		}
		for _, stmt := range s.Driver.Use(database, schema) {
			if _, err := t.querier().ExecContext(ctx, stmt); err != nil {
				return notice, err
			}
		}
		t.database, t.schema = database, schema
	}
	return notice, nil
}

// usePartition limits the connection's queries to a partition key (drivers that
// have partitions); an empty key removes the limit.
func (t *Tab) usePartition(ctx context.Context, key string) error {
	pd, ok := t.sess.Driver.(dialect.Partitioned)
	if !ok || strings.TrimSpace(key) == t.partition {
		return nil
	}
	for _, stmt := range pd.UsePartition(strings.TrimSpace(key)) {
		if _, err := t.conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("partition key: %w", err)
		}
	}
	t.partition = strings.TrimSpace(key)
	return nil
}

// stopper returns what stops the statement running on the tab connection with the
// given cancel. Drivers that close the connection when a statement is cancelled get
// it interrupted from another connection first, so the session survives; cancel then
// runs only when that fails or the statement does not stop in time.
func (t *Tab) stopper(cancel context.CancelFunc) func() {
	ir, ok := t.sess.Driver.(dialect.Interrupter)
	if !ok || t.sid == "" {
		return cancel
	}
	s, pool, sid := t.sess, t.pool, t.sid
	return func() {
		ok := make(chan bool, 1)
		go func() {
			ctx, done := context.WithTimeout(context.Background(), interruptGrace)
			defer done()
			db, err := s.DB(ctx, pool)
			if err == nil {
				err = ir.Interrupt(ctx, db, sid)
			}
			ok <- err == nil
		}()
		select {
		case interrupted := <-ok:
			if !interrupted {
				cancel()
				return
			}
			time.AfterFunc(interruptGrace, cancel)
		case <-time.After(interruptWait):
			// The server does not answer: give up on the connection.
			cancel()
		}
	}
}

// guard returns the context to run a statement bounded by ctx with, and the function
// to call when the statement is over.
func (t *Tab) guard(ctx context.Context) (context.Context, func()) {
	if _, ok := t.sess.Driver.(dialect.Interrupter); !ok || t.sid == "" {
		return ctx, func() {}
	}
	dctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, t.stopper(cancel))
	return dctx, func() {
		stop()
		cancel()
	}
}

// querier is the open transaction, or the connection.
func (t *Tab) querier() querier {
	if t.tx != nil {
		return t.tx
	}
	return t.conn
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SetManual switches the tab between auto-commit and a manual transaction. Leaving
// manual mode with a transaction open is refused.
func (e *Engine) SetManual(s *session.Session, tabID string, manual bool) (TxState, error) {
	t := e.tab(tabID, s)
	t.mu.Lock()
	defer t.mu.Unlock()
	if !manual && t.tx != nil {
		return t.txState(), errors.New("commit or roll back the open transaction first")
	}
	t.manual = manual
	return t.txState(), nil
}

// Commit commits the tab transaction.
func (e *Engine) Commit(ctx context.Context, s *session.Session, tabID string) (TxState, error) {
	return e.endTx(ctx, s, tabID, true)
}

// Rollback rolls back the tab transaction.
func (e *Engine) Rollback(ctx context.Context, s *session.Session, tabID string) (TxState, error) {
	return e.endTx(ctx, s, tabID, false)
}

func (e *Engine) endTx(ctx context.Context, s *session.Session, tabID string, commit bool) (TxState, error) {
	t := e.tab(tabID, s)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeCursorLocked()
	var err error
	switch {
	case t.tx != nil:
		if commit {
			err = t.tx.Commit()
		} else {
			err = t.tx.Rollback()
		}
		t.tx, t.txStatements = nil, 0
	case t.implicitTx && t.conn != nil:
		stmt := "ROLLBACK"
		if commit {
			stmt = "COMMIT"
		}
		_, err = t.conn.ExecContext(ctx, stmt)
		t.implicitTx = false
	}
	if err != nil && errors.Is(err, sql.ErrTxDone) {
		err = errors.New("the transaction is no longer open: the connection was lost and the server rolled it back")
	}
	if err != nil && s.Driver.Transient(err) {
		t.dropConnLocked("")
		err = errors.New("the connection was lost before the transaction ended; the server rolls back unfinished transactions")
	}
	return t.txState(), err
}

// State returns the transaction state of a tab.
func (e *Engine) State(tabID string) TxState {
	t, ok := e.existing(tabID)
	if !ok {
		return TxState{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.txState()
}

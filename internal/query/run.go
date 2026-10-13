package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/session"
	"github.com/iagapie/quire/internal/sqlsplit"
)

// Mode selects which part of a script runs.
type Mode string

const (
	ModeAll       Mode = "all"
	ModeCurrent   Mode = "current"
	ModeSelection Mode = "selection"
)

// RunRequest describes a script execution. Offsets are UTF-16 code units, as used by
// the editor.
type RunRequest struct {
	Script   string `json:"script"`
	Mode     Mode   `json:"mode"`
	Cursor   int    `json:"cursor"`
	SelStart int    `json:"selStart"`
	SelEnd   int    `json:"selEnd"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	// Partition is the partition key (JSON) queries are limited to (Cosmos DB).
	Partition string `json:"partition"`
	PageSize  int64  `json:"pageSize"`
	// Explain shows the plan of each statement instead of running it.
	Explain bool `json:"explain"`
}

// TableInfo is set on results read from a single table: the grid can filter, sort and
// (with a primary key) edit them.
type TableInfo struct {
	Database string   `json:"database"`
	Schema   string   `json:"schema"`
	Table    string   `json:"table"`
	Where    string   `json:"where"`
	OrderBy  string   `json:"orderBy"`
	Limit    int64    `json:"limit"`
	Columns  []string `json:"columns"`
	// Key lists the primary key columns; empty when the rows cannot be edited.
	Key []string `json:"key"`
	// Writable lists the result columns that can be changed.
	Writable []string `json:"writable"`
	// Types maps column names to their declared types, for inserting new rows.
	Types map[string]string `json:"types"`
	// Nullable lists the columns accepting NULL.
	Nullable []string `json:"nullable"`
	// Defaults lists columns with a default value.
	Defaults []string `json:"defaults"`
}

// Result is the outcome of one statement.
type Result struct {
	ID string `json:"id"`
	// Kind is rows, message or error.
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	Line      int      `json:"line"`
	Statement string   `json:"statement"`
	Columns   []Column `json:"columns,omitempty"`
	Page      *Page    `json:"page,omitempty"`
	Message   string   `json:"message,omitempty"`
	// RowsAffected is -1 when unknown.
	RowsAffected int64      `json:"rowsAffected"`
	Error        string     `json:"error,omitempty"`
	Table        *TableInfo `json:"table,omitempty"`
	DurationMs   int64      `json:"durationMs"`
}

// RunResult is the outcome of a script execution.
type RunResult struct {
	Results []Result `json:"results"`
	// Output lists messages: affected rows, notices, session resets.
	Output     []string `json:"output"`
	Database   string   `json:"database"`
	Schema     string   `json:"schema"`
	DurationMs int64    `json:"durationMs"`
	Tx         TxState  `json:"tx"`
}

var seq atomic.Int64

func newID(prefix string) string {
	return prefix + strconv.FormatInt(seq.Add(1), 36)
}

// SelectStatements picks the statements of req to run.
func SelectStatements(req RunRequest, o sqlsplit.Options) ([]sqlsplit.Statement, error) {
	script := req.Script
	switch {
	case req.Mode == ModeSelection && req.SelEnd > req.SelStart:
		return sqlsplit.Within(script, byteOffset(script, req.SelStart), byteOffset(script, req.SelEnd), o), nil
	case req.Mode == ModeCurrent:
		all := sqlsplit.Split(script, o)
		st, ok := sqlsplit.At(script, all, byteOffset(script, req.Cursor))
		if !ok {
			return nil, errors.New("there is no statement at the cursor")
		}
		// Inside a large GO batch, run only the paragraph at the cursor.
		if o.GoBatches {
			inner := o
			inner.GoBatches, inner.BlankLines = false, true
			parts := sqlsplit.Within(script, st.Start, st.End, inner)
			if p, ok := sqlsplit.At(script, parts, byteOffset(script, req.Cursor)); ok {
				return []sqlsplit.Statement{p}, nil
			}
		}
		return []sqlsplit.Statement{st}, nil
	}
	return sqlsplit.Split(script, o), nil
}

// Run executes the requested part of the script on the tab connection.
func (e *Engine) Run(ctx context.Context, s *session.Session, tabID, opID string, req RunRequest) (res RunResult) {
	start := time.Now()
	if req.PageSize <= 0 {
		req.PageSize = 100
	}
	res = RunResult{Results: []Result{}, Output: []string{}, Database: req.Database, Schema: req.Schema}
	stmts, err := SelectStatements(req, s.Driver.Split())
	t := e.tab(tabID, s)
	t.mu.Lock()
	defer t.mu.Unlock()
	defer func() {
		res.Tx = t.txState()
		res.DurationMs = time.Since(start).Milliseconds()
	}()
	if err != nil {
		res.Results = append(res.Results, Result{ID: newID("r"), Kind: "error", Title: "Nothing to run", Error: err.Error(), RowsAffected: -1})
		return res
	}
	if len(stmts) == 0 {
		return res
	}
	// The previous results of the tab are replaced; their cursor is released.
	t.closeCursorLocked()
	e.forget(tabID, nil)

	ctx, done := s.BeginWithTimeout(ctx, opID, 0)
	defer done()
	msgs := &dialect.Messages{}
	ctx = dialect.WithMessages(ctx, msgs)
	timeout := s.Timeout()

	// Getting (and checking) the connection is bounded like a statement, so a frozen
	// server fails the run instead of hanging it.
	actx, acancel := ctx, context.CancelFunc(func() {})
	if timeout > 0 {
		actx, acancel = context.WithTimeout(ctx, timeout)
	}
	notice, err := t.acquire(actx, req.Database, req.Schema)
	if err == nil {
		err = t.usePartition(actx, req.Partition)
	}
	if err != nil && errors.Is(actx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("operation timed out: the server did not answer within %s", timeout)
	}
	acancel()
	if notice != "" {
		res.Output = append(res.Output, notice)
	}
	if err != nil {
		s.Failed(err)
		res.Results = append(res.Results, Result{ID: newID("r"), Kind: "error", Title: "Could not connect", Error: errorText(ctx, err), RowsAffected: -1})
		return res
	}
	syn := s.Driver
	for i, st := range stmts {
		last := i == len(stmts)-1
		text := st.Text
		a := dialect.Analyze(text, syn.Split())
		title := fmt.Sprintf("%s (line %d)", titleVerb(a.Verb), st.Line)
		var after []string
		if req.Explain {
			plan := syn.Explain(text)
			if plan.Query == "" {
				res.Results = append(res.Results, Result{ID: newID("r"), Kind: "error", Title: title, Line: st.Line, Statement: text, Error: "EXPLAIN is not available for this database", RowsAffected: -1})
				break
			}
			if err := t.execAll(ctx, plan.Before, timeout); err != nil {
				_ = t.execAll(context.WithoutCancel(ctx), plan.After, 10*time.Second)
				res.Results = append(res.Results, Result{ID: newID("r"), Kind: "error", Title: title, Line: st.Line, Statement: text, Error: errorText(ctx, err), RowsAffected: -1})
				break
			}
			after = plan.After
			text, title = plan.Query, fmt.Sprintf("Plan (line %d)", st.Line)
			a = dialect.Analyze(text, syn.Split())
			a.Kind = dialect.KindQuery
		}
		if s.Profile.ReadOnly {
			if err := dialect.CheckReadOnly(a); err != nil {
				res.Results = append(res.Results, Result{ID: newID("r"), Kind: "error", Title: title, Line: st.Line, Statement: text, Error: err.Error(), RowsAffected: -1})
				break
			}
		}
		stmtCtx, cancel := ctx, context.CancelFunc(func() {})
		if timeout > 0 {
			stmtCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		r, out := e.exec(stmtCtx, ctx, t, st, text, a, title, last, req, opID)
		cancel()
		if len(after) > 0 {
			_ = t.execAll(context.WithoutCancel(ctx), after, 10*time.Second)
		}
		failed := len(r) > 0 && r[len(r)-1].Kind == "error"
		lines := msgs.Take()
		if so, ok := syn.(dialect.ServerOutput); ok && t.conn != nil && t.cursor == nil {
			octx, ocancel := context.WithTimeout(ctx, 10*time.Second)
			if more, err := so.ReadOutput(octx, t.conn); err == nil {
				lines = append(lines, more...)
			}
			ocancel()
		}
		if len(lines) > 0 {
			out = append(lines, out...)
			if n := len(r); n > 0 && r[n-1].Kind == "message" && r[n-1].Message == statementExecuted {
				r[n-1].Message = lines[len(lines)-1]
			}
		}
		res.Results = append(res.Results, r...)
		res.Output = append(res.Output, out...)
		// Without a way to ask the server, transactions are followed by statement.
		if _, ok := syn.(dialect.TxState); !ok && !t.manual && !failed {
			switch a.Transaction {
			case "BEGIN":
				t.implicitTx = true
			case "COMMIT", "ROLLBACK":
				t.implicitTx = false
			}
		}
		if failed || ctx.Err() != nil {
			break
		}
	}
	res.Database, res.Schema = t.database, t.schema
	if t.tx == nil && t.conn != nil && t.cursor == nil {
		if ts, ok := s.Driver.(dialect.TxState); ok {
			if in, err := ts.InTransaction(ctx, t.conn); err == nil {
				t.implicitTx = in
			}
		}
	}
	return res
}

func titleVerb(v string) string {
	switch v {
	case "":
		return "Statement"
	case "SELECT", "WITH", "VALUES", "TABLE":
		return "Query"
	}
	return strings.ToUpper(v[:1]) + strings.ToLower(v[1:])
}

// exec runs one statement. runCtx outlives the statement and carries the cursor of
// the last result.
func (e *Engine) exec(ctx, runCtx context.Context, t *Tab, st sqlsplit.Statement, text string, a dialect.Analysis, title string, last bool, req RunRequest, opID string) ([]Result, []string) {
	s := t.sess
	started := time.Now()
	base := Result{Title: title, Line: st.Line, Statement: text, RowsAffected: -1}
	fail := func(err error) ([]Result, []string) {
		r := base
		r.ID, r.Kind, r.Error = newID("r"), "error", errorText(ctx, err)
		r.DurationMs = time.Since(started).Milliseconds()
		return []Result{r}, nil
	}
	var out []string

	// Transaction statements in a manual transaction drive the tab transaction.
	if t.manual {
		switch a.Transaction {
		case "BEGIN":
			return []Result{{ID: newID("r"), Kind: "message", Title: title, Line: st.Line, Statement: text, Message: "A transaction is already open in this tab.", RowsAffected: -1}}, nil
		case "COMMIT", "ROLLBACK":
			if t.tx != nil {
				var err error
				if a.Transaction == "COMMIT" {
					err = t.tx.Commit()
				} else {
					err = t.tx.Rollback()
				}
				t.tx, t.txStatements = nil, 0
				if err != nil {
					return fail(err)
				}
			}
			msg := "Transaction committed."
			if a.Transaction == "ROLLBACK" {
				msg = "Transaction rolled back."
			}
			return []Result{{ID: newID("r"), Kind: "message", Title: title, Line: st.Line, Statement: text, Message: msg, RowsAffected: -1, DurationMs: time.Since(started).Milliseconds()}}, []string{msg}
		}
		if t.tx == nil {
			// The transaction outlives this run: it ends with Commit or Rollback.
			tx, err := t.conn.BeginTx(context.WithoutCancel(runCtx), &sql.TxOptions{ReadOnly: s.Profile.ReadOnly})
			if err != nil {
				return fail(err)
			}
			t.tx = tx
		}
		// Only statements that change something wait for Commit.
		if !a.ReadOnly {
			t.txStatements++
		}
	}
	if changesSession(a) {
		t.stateful = true
	}

	retryable := a.ReadOnly && t.tx == nil && !t.implicitTx
	if !req.Explain && isBatch(s.Driver.Split(), text, a) {
		return e.batch(ctx, t, base, text, req.PageSize)
	}
	if a.Kind == dialect.KindExec {
		ectx, release := t.guard(ctx)
		res, err := t.querier().ExecContext(ectx, text)
		release()
		if err != nil {
			s.Failed(err)
			if s.Driver.Transient(err) && t.tx == nil {
				t.dropConnLocked("the connection to the server was lost")
			}
			return fail(err)
		}
		r := base
		r.ID, r.Kind = newID("r"), "message"
		if n, err := res.RowsAffected(); err == nil && n >= 0 && affects(a.Verb) {
			r.RowsAffected = n
			r.Message = strconv.FormatInt(n, 10) + " rows affected"
			if n == 1 {
				r.Message = "1 row affected"
			}
		} else {
			r.Message = statementExecuted
		}
		r.DurationMs = time.Since(started).Milliseconds()
		out = append(out, fmt.Sprintf("Line %d: %s (%s)", st.Line, r.Message, formatMs(r.DurationMs)))
		return []Result{r}, out
	}

	// Queries: a statement that is not the last one is read in full (paged with
	// LIMIT when possible), so the connection is free for the next statement. The
	// last one keeps its cursor open for further pages.
	keepCursor := last && a.ReadOnly && !req.Explain
	q := text
	wrapped := false
	if !last && a.Wrappable && a.ReadOnly {
		if lq := s.Driver.Limit(text, req.PageSize+1, 0); lq != "" {
			q, wrapped = lq, true
		}
	}
	var (
		rows      *sql.Rows
		cancelCur context.CancelFunc
		stopCur   func()
		unwatch   = func() bool { return false }
	)
	run := func(ctx context.Context) error {
		// The cursor lives on after the run (for more pages); while the statement runs
		// and its first page is read, Stop and the timeout cancel it.
		cursorCtx, cancel := context.WithCancel(context.WithoutCancel(runCtx))
		cancelCur, stopCur = cancel, t.stopper(cancel)
		unwatch = context.AfterFunc(ctx, stopCur)
		var err error
		rows, err = t.querier().QueryContext(cursorCtx, q) //nolint:rowserrcheck,sqlclosecheck // the rows become the open cursor; readCursor checks Err and the tab closes them
		if err != nil {
			unwatch()
			cancel()
		}
		return err
	}
	err := run(ctx)
	if err != nil && retryable && s.Driver.Transient(err) && ctx.Err() == nil {
		// Run the read again on a new connection.
		for attempt := 1; attempt < session.MaxAttempts && err != nil && s.Driver.Transient(err) && ctx.Err() == nil; attempt++ {
			s.Failed(err)
			e.retry(opID)(attempt, err)
			t.dropConnLocked("the connection to the server was lost")
			select {
			case <-ctx.Done():
			case <-time.After(time.Duration(attempt) * session.RetryBackoff):
			}
			if ctx.Err() != nil {
				break
			}
			notice, aerr := t.acquire(ctx, req.Database, req.Schema)
			if notice != "" {
				out = append(out, notice)
			}
			if aerr != nil {
				err = aerr
				continue
			}
			err = run(ctx)
		}
	}
	if err != nil {
		s.Failed(err)
		if s.Driver.Transient(err) && t.tx == nil {
			t.dropConnLocked("the connection to the server was lost")
		}
		return fail(err)
	}
	defer unwatch()
	cts, err := rows.ColumnTypes()
	if err != nil {
		_ = rows.Close()
		cancelCur()
		return fail(err)
	}
	if len(cts) == 0 {
		// A statement without a result set (a CALL, a DDL run as a query...).
		_ = rows.Close()
		cancelCur()
		if err := rows.Err(); err != nil {
			return fail(err)
		}
		r := base
		r.ID, r.Kind, r.Message = newID("r"), "message", statementExecuted
		r.DurationMs = time.Since(started).Milliseconds()
		return []Result{r}, []string{fmt.Sprintf("Line %d: %s (%s)", st.Line, r.Message, formatMs(r.DurationMs))}
	}

	res := &result{
		id: newID("r"), tabID: t.ID, stmt: text, database: req.Database, schema: req.Schema, partition: t.partition,
		wrappable: a.Wrappable, readOnly: a.ReadOnly, cols: columns(cts), total: -1, used: time.Now(),
		cursor: rows, cancel: cancelCur, stop: stopCur,
	}
	if t.database != "" || t.schema != "" {
		res.database, res.schema = t.database, t.schema
	}
	// Results that change data, or that cannot be re-run, are read in full.
	upto := req.PageSize + 1
	if !a.ReadOnly {
		upto = int64(MaxReadRows)
	}
	t.cursor = res
	if err := t.readCursor(res, upto); err != nil {
		t.closeCursorLocked()
		if s.Driver.Transient(err) && t.tx == nil {
			t.dropConnLocked("the connection to the server was lost")
		}
		return fail(err)
	}
	if t.cursor == res && (!keepCursor || wrapped) {
		if wrapped {
			// The LIMIT stopped the read: more rows exist when one more came back.
			t.finishCursorLocked(res)
			if int64(len(res.rows)) > req.PageSize {
				res.done, res.total = false, -1
			}
		} else {
			t.closeCursorLocked()
		}
	}
	if !a.ReadOnly && !res.done {
		out = append(out, fmt.Sprintf("Line %d: only the first %d rows were kept.", st.Line, MaxReadRows))
		res.done = true
	}
	e.keep(res)
	page, _ := res.window(0, req.PageSize)
	r := base
	r.ID, r.Kind, r.Columns, r.Page = res.id, "rows", res.cols, &page
	r.DurationMs = time.Since(started).Milliseconds()
	page.DurationMs = r.DurationMs
	if !req.Explain && a.Verb == "SELECT" {
		if ti := e.tableInfo(ctx, t, text, res); ti != nil {
			res.table, r.Table = ti, ti
		}
	} else if a.Kind == dialect.KindQuery && !a.ReadOnly {
		// INSERT … RETURNING and friends: say how many rows came back.
		out = append(out, fmt.Sprintf("Line %d: %d rows returned", st.Line, res.end()))
	}
	return []Result{r}, out
}

const statementExecuted = "Statement executed."

// execAll runs statements on the tab connection, each bounded by timeout.
func (t *Tab) execAll(ctx context.Context, stmts []string, timeout time.Duration) error {
	for _, stmt := range stmts {
		sctx, cancel := ctx, context.CancelFunc(func() {})
		if timeout > 0 {
			sctx, cancel = context.WithTimeout(ctx, timeout)
		}
		gctx, release := t.guard(sctx)
		_, err := t.querier().ExecContext(gctx, stmt)
		release()
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

// isBatch reports a statement of a batch dialect (SQL Server) that is more than one
// plain query: several statements, a procedure call, variables. It may return several
// result sets and print messages, so it runs as a batch.
func isBatch(o sqlsplit.Options, text string, a dialect.Analysis) bool {
	if !o.GoBatches {
		return false
	}
	if a.Kind != dialect.KindQuery || !a.Wrappable {
		return true
	}
	inner := o
	inner.GoBatches = false
	return len(sqlsplit.Split(text, inner)) > 1
}

// batch runs a batch that may hold several statements, return several result sets
// and print messages. Every result set is read in full (up to MaxCachedRows) and
// becomes a result; no cursor stays open.
func (e *Engine) batch(ctx context.Context, t *Tab, base Result, text string, pageSize int64) ([]Result, []string) {
	s := t.sess
	started := time.Now()
	fail := func(err error) Result {
		r := base
		r.ID, r.Kind, r.Error = newID("r"), "error", errorText(ctx, err)
		r.DurationMs = time.Since(started).Milliseconds()
		return r
	}
	qctx, release := t.guard(ctx)
	defer release()
	rows, err := t.querier().QueryContext(qctx, text)
	if err != nil {
		s.Failed(err)
		if s.Driver.Transient(err) && t.tx == nil {
			t.dropConnLocked("the connection to the server was lost")
		}
		return []Result{fail(err)}, nil
	}
	defer func() { _ = rows.Close() }()
	f, _ := s.Driver.(ValueFormatter)
	var (
		results []Result
		out     []string
	)
	for {
		if cts, err := rows.ColumnTypes(); err == nil && len(cts) > 0 {
			res := &result{
				id: newID("r"), tabID: t.ID, stmt: text, database: t.database, schema: t.schema,
				cols: columns(cts), total: -1, used: time.Now(),
			}
			var batch [][]*string
			for rows.Next() {
				if len(batch) >= MaxCachedRows {
					out = append(out, fmt.Sprintf("Line %d: only the first %d rows of a result were kept.", base.Line, MaxCachedRows))
					break
				}
				row, err := scanRow(rows, res.cols, f)
				if err != nil {
					return append(results, fail(err)), out
				}
				batch = append(batch, row)
			}
			res.add(batch)
			res.done, res.total = true, res.end()
			e.keep(res)
			page, _ := res.window(0, pageSize)
			r := base
			r.ID, r.Kind, r.Columns, r.Page = res.id, "rows", res.cols, &page
			r.DurationMs = time.Since(started).Milliseconds()
			page.DurationMs = r.DurationMs
			results = append(results, r)
		}
		if !rows.NextResultSet() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		s.Failed(err)
		if s.Driver.Transient(err) && t.tx == nil {
			t.dropConnLocked("the connection to the server was lost")
		}
		return append(results, fail(err)), out
	}
	if len(results) > 1 {
		for i := range results {
			results[i].Title = fmt.Sprintf("Result %d (line %d)", i+1, base.Line)
		}
	}
	if len(results) == 0 {
		r := base
		r.ID, r.Kind, r.Message = newID("r"), "message", statementExecuted
		r.DurationMs = time.Since(started).Milliseconds()
		results = append(results, r)
	}
	return results, out
}

func affects(verb string) bool {
	switch verb {
	case "INSERT", "UPDATE", "DELETE", "MERGE", "UPSERT", "REPLACE", "COPY":
		return true
	}
	return false
}

// changesSession reports statements that leave state on the connection.
func changesSession(a dialect.Analysis) bool {
	switch a.Verb {
	case "SET", "USE", "DECLARE", "PREPARE", "LISTEN", "ATTACH", "CREATE":
		return true
	}
	return a.Transaction == "BEGIN"
}

func formatMs(ms int64) string {
	if ms < 1000 {
		return strconv.FormatInt(ms, 10) + " ms"
	}
	return strconv.FormatFloat(float64(ms)/1000, 'f', 2, 64) + " s"
}

// byteOffset converts a UTF-16 offset into a byte offset in s.
func byteOffset(s string, u16 int) int {
	if u16 <= 0 {
		return 0
	}
	n := 0
	for i, r := range s {
		if n >= u16 {
			return i
		}
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return len(s)
}

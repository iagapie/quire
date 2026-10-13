package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/session"
)

// Page is a window of a result.
type Page struct {
	Rows    [][]*string `json:"rows"`
	Offset  int64       `json:"offset"`
	HasMore bool        `json:"hasMore"`
	// Total is the number of rows when known (all rows were read), else -1.
	Total      int64 `json:"total"`
	DurationMs int64 `json:"durationMs"`
}

// result is a statement result kept for paging.
type result struct {
	id    string
	tabID string
	// stmt is the statement re-run to rebuild a page.
	stmt      string
	database  string
	schema    string
	partition string
	wrappable bool
	// readOnly results may be re-run; others keep all their rows.
	readOnly bool
	cols     []Column
	rows     [][]*string
	start    int64 // offset of rows[0]
	bytes    int
	done     bool  // every row up to the end is in rows (or was read past)
	total    int64 // row count when known, else -1
	used     time.Time
	table    *TableInfo

	// The open cursor: rows of the tab connection, the cancel of its context, and stop,
	// which ends the statement (see Tab.stopper).
	cursor *sql.Rows
	cancel context.CancelFunc
	stop   func()
}

func (r *result) end() int64 { return r.start + int64(len(r.rows)) }

// add appends rows to the cache, dropping the oldest when it grows too big.
func (r *result) add(rows [][]*string) {
	r.rows = append(r.rows, rows...)
	r.bytes += rowsSize(rows)
	for len(r.rows) > MaxCachedRows || (r.bytes > MaxCachedBytes && len(r.rows) > 1000) {
		drop := max(len(r.rows)/4, len(r.rows)-MaxCachedRows)
		r.bytes -= rowsSize(r.rows[:drop])
		r.rows = append([][]*string(nil), r.rows[drop:]...)
		r.start += int64(drop)
	}
}

// window returns rows [offset, offset+size) from the cache, when they are there.
func (r *result) window(offset, size int64) (Page, bool) {
	if offset < r.start {
		return Page{}, false
	}
	end := offset + size
	switch {
	case end < r.end(): // one more row is cached, so we know there are more
	case r.done && offset <= r.end():
	default:
		return Page{}, false
	}
	from := offset - r.start
	to := min(end, r.end()) - r.start
	p := Page{Rows: r.rows[from:to], Offset: offset, Total: r.total}
	p.HasMore = end < r.end() || (!r.done && end >= r.end())
	if p.Rows == nil {
		p.Rows = [][]*string{}
	}
	return p, true
}

// readCursor reads from the open cursor until upto rows are cached or the rows end.
func (t *Tab) readCursor(r *result, upto int64) error {
	if r.cursor == nil {
		return errors.New("cursor closed")
	}
	// Reading a page normally takes milliseconds: bound it so a dead link fails fast
	// and the page is rebuilt on a new connection.
	timer := time.AfterFunc(max(t.sess.Timeout(), 30*time.Second), r.stopFunc())
	defer timer.Stop()
	f, _ := t.sess.Driver.(ValueFormatter)
	var batch [][]*string
	for r.end()+int64(len(batch)) < upto {
		if !r.cursor.Next() {
			if err := r.cursor.Err(); err != nil {
				r.add(batch)
				return err
			}
			r.add(batch)
			r.done = true
			if r.start == 0 {
				r.total = r.end()
			} else {
				r.total = r.end()
			}
			t.finishCursorLocked(r)
			return nil
		}
		row, err := scanRow(r.cursor, r.cols, f)
		if err != nil {
			r.add(batch)
			return err
		}
		batch = append(batch, row)
	}
	r.add(batch)
	return nil
}

// stopFunc returns what ends the statement of the cursor.
func (r *result) stopFunc() func() {
	if r.stop != nil {
		return r.stop
	}
	if r.cancel != nil {
		return r.cancel
	}
	return func() {}
}

// finishCursorLocked closes a cursor whose rows were all read.
func (t *Tab) finishCursorLocked(r *result) {
	if r.cursor != nil {
		_ = r.cursor.Close()
		r.cursor = nil
	}
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.stop = nil
	if t.cursor == r {
		t.cursor = nil
	}
}

// closeCursorLocked releases the open cursor. A short drain keeps small remainders
// (and the connection); otherwise the query is cancelled. If cancelling broke the
// connection, it is dropped.
func (t *Tab) closeCursorLocked() {
	r := t.cursor
	if r == nil {
		return
	}
	t.cursor = nil
	if r.cursor != nil {
		f, _ := t.sess.Driver.(ValueFormatter)
		// Reading a row may wait for the network (the next page of a cursor): stop the
		// statement if the drain takes longer than planned.
		watchdog := time.AfterFunc(drainTimeout, r.stopFunc())
		defer watchdog.Stop()
		deadline := time.Now().Add(150 * time.Millisecond)
		var batch [][]*string
		drained := false
		for n := 0; n < 2000 && time.Now().Before(deadline); n++ {
			if !r.cursor.Next() {
				drained = r.cursor.Err() == nil
				break
			}
			row, err := scanRow(r.cursor, r.cols, f)
			if err != nil {
				break
			}
			batch = append(batch, row)
		}
		r.add(batch)
		if drained {
			r.done = true
			r.total = r.end()
		}
	}
	if !r.done {
		if stop := r.stopFunc(); stop != nil {
			stop()
		}
	}
	if r.cursor != nil {
		_ = r.cursor.Close()
		r.cursor = nil
	}
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.stop = nil
	if !r.done && t.conn != nil && !dialect.IsStateless(t.sess.Driver) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := t.conn.PingContext(ctx)
		cancel()
		if err != nil {
			t.dropConnLocked("stopping the previous query closed the connection")
		}
	}
}

func (e *Engine) result(id string) (*result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.results[id]
	if !ok {
		return nil, errors.New("this result is no longer available; run the statement again")
	}
	return r, nil
}

func (e *Engine) keep(r *result) {
	e.mu.Lock()
	e.results[r.id] = r
	e.mu.Unlock()
}

func (e *Engine) forget(tabID string, keep map[string]bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, r := range e.results {
		if r.tabID == tabID && !keep[id] {
			delete(e.results, id)
		}
	}
}

// FetchPage returns rows [offset, offset+size) of a result: from memory, from the
// open cursor, or by running the statement again for that window only.
func (e *Engine) FetchPage(ctx context.Context, s *session.Session, tabID, resultID, opID string, offset, size int64) (Page, error) {
	start := time.Now()
	if size <= 0 {
		size = 100
	}
	r, err := e.result(resultID)
	if err != nil {
		return Page{}, err
	}
	t := e.tab(tabID, s)
	t.mu.Lock()
	defer t.mu.Unlock()
	r.used = time.Now()
	t.used = time.Now()
	if p, ok := r.window(offset, size); ok {
		p.DurationMs = time.Since(start).Milliseconds()
		return p, nil
	}
	if t.cursor == r && offset >= r.start {
		err := t.readCursor(r, offset+size+1)
		if err == nil {
			if p, ok := r.window(offset, size); ok {
				p.DurationMs = time.Since(start).Milliseconds()
				return p, nil
			}
		}
		// The cursor broke (network, timeout): rebuild the page below.
		t.closeCursorLocked()
		if err != nil && s.Driver.Transient(err) {
			t.dropConnLocked("the connection to the server was lost")
		}
	}
	if !r.readOnly {
		return Page{}, errors.New("this page is no longer in memory and the statement changes data, so it is not run again; run it yourself to see the rows")
	}
	ctx, done := s.Begin(ctx, opID)
	defer done()
	p, err := session.RetryRead(ctx, s, e.retry(opID), func(ctx context.Context) (Page, error) {
		p, err := t.rerun(ctx, r, offset, size)
		if err != nil && s.Driver.Transient(err) {
			t.dropConnLocked("the connection to the server was lost")
		}
		return p, err
	})
	if err != nil {
		return Page{}, friendly(ctx, err)
	}
	p.DurationMs = time.Since(start).Milliseconds()
	return p, nil
}

// rerun runs the result statement again for one window.
func (t *Tab) rerun(ctx context.Context, r *result, offset, size int64) (Page, error) {
	notice, err := t.acquire(ctx, r.database, r.schema)
	if err != nil {
		return Page{}, err
	}
	_ = notice
	if err := t.usePartition(ctx, r.partition); err != nil {
		return Page{}, err
	}
	q := r.stmt
	skip := offset
	if r.wrappable {
		if lq := t.sess.Driver.Limit(r.stmt, size+1, offset); lq != "" {
			q, skip = lq, 0
		}
	}
	qctx, release := t.guard(ctx)
	defer release()
	rows, err := t.querier().QueryContext(qctx, q)
	if err != nil {
		return Page{}, err
	}
	defer func() { _ = rows.Close() }()
	f, _ := t.sess.Driver.(ValueFormatter)
	var out [][]*string
	var n int64
	for rows.Next() {
		if n < skip {
			n++
			continue
		}
		row, err := scanRow(rows, r.cols, f)
		if err != nil {
			return Page{}, err
		}
		out = append(out, row)
		if int64(len(out)) > size {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	more := int64(len(out)) > size
	if more {
		out = out[:size]
	}
	// The window becomes the cache, so moving around it does not run it again.
	r.rows, r.start, r.bytes = out, offset, rowsSize(out)
	r.done = !more
	if !more {
		r.total = offset + int64(len(out))
	}
	if out == nil {
		out = [][]*string{}
	}
	return Page{Rows: out, Offset: offset, HasMore: more, Total: r.total}, nil
}

// Count counts the rows of a result.
func (e *Engine) Count(ctx context.Context, s *session.Session, tabID, resultID, opID string) (int64, error) {
	r, err := e.result(resultID)
	if err != nil {
		return 0, err
	}
	if r.total >= 0 {
		return r.total, nil
	}
	if !r.wrappable || !r.readOnly {
		return 0, errors.New("this statement cannot be counted")
	}
	ctx, done := s.Begin(ctx, opID)
	defer done()
	n, err := session.RetryRead(ctx, s, e.retry(opID), func(ctx context.Context) (int64, error) {
		// A pool connection, so the tab keeps its open cursor.
		db, err := s.DB(ctx, poolFor(s, r.database))
		if err != nil {
			return 0, err
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			return 0, err
		}
		defer func() { _ = conn.Close() }()
		for _, stmt := range s.Driver.Use(r.database, r.schema) {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return 0, err
			}
		}
		var n int64
		err = conn.QueryRowContext(ctx, dialect.Count(s.Driver, r.stmt)).Scan(&n)
		return n, err
	})
	if err != nil {
		return 0, friendly(ctx, err)
	}
	return n, nil
}

func poolFor(s *session.Session, database string) string {
	if pd, ok := s.Driver.(dialect.PerDatabase); ok && pd.PerDatabasePools() && database != s.Database {
		return database
	}
	return ""
}

func (e *Engine) retry(opID string) func(int, error) {
	return func(attempt int, err error) {
		if e.OnRetry != nil {
			e.OnRetry(opID, attempt, err)
		}
	}
}

// friendly turns context errors into messages.
func friendly(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	// The context says why the operation ended: a driver may report a timeout as a
	// cancellation, or the other way round.
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return errors.New("operation timed out")
	case errors.Is(ctx.Err(), context.Canceled):
		return errors.New("operation cancelled")
	case errors.Is(err, context.Canceled):
		return errors.New("operation cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("operation timed out")
	case errors.Is(err, sql.ErrConnDone):
		return errors.New("the connection to the server was lost; run the statement again")
	}
	return err
}

func errorText(ctx context.Context, err error) string {
	return fmt.Sprint(friendly(ctx, err))
}

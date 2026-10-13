package query

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/session"
)

// tableInfo recognizes results read from a single table and finds what can be edited.
func (e *Engine) tableInfo(ctx context.Context, t *Tab, stmt string, res *result) *TableInfo {
	s := t.sess
	if s.Info.QueryOnly {
		return nil
	}
	ref, ok := dialect.SingleTable(stmt, s.Driver.Split())
	if !ok {
		return nil
	}
	ti := &TableInfo{Database: res.database, Schema: res.schema, Table: dialect.Fold(s.Driver, ref.Table, ref.TableQuoted), Where: ref.Where, OrderBy: ref.OrderBy, Limit: ref.Limit}
	for _, c := range ref.Columns {
		ti.Columns = append(ti.Columns, dialect.Fold(s.Driver, c, false))
	}
	ref.Schema = dialect.Fold(s.Driver, ref.Schema, ref.SchemaQuoted)
	if ti.Database == "" {
		ti.Database = s.Database
	}
	switch {
	case ref.Schema != "" && !s.Info.Schemas:
		ti.Database = ref.Schema // MySQL databases, SQLite attached databases
	case ref.Schema != "":
		ti.Schema = ref.Schema
	case s.Info.Schemas && ti.Schema == "":
		ti.Schema = s.Schema
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := s.DB(cctx, poolFor(s, ti.Database))
	if err != nil {
		return nil
	}
	cols, err := s.Driver.Columns(cctx, db, ti.Database, ti.Schema, ti.Table)
	if err != nil || len(cols) == 0 {
		return nil
	}
	ti.Types = map[string]string{}
	byName := map[string]dialect.Column{}
	for _, c := range cols {
		byName[c.Name] = c
		ti.Types[c.Name] = c.Type
		if c.Nullable {
			ti.Nullable = append(ti.Nullable, c.Name)
		}
		if c.Default != "" || c.Generated {
			ti.Defaults = append(ti.Defaults, c.Name)
		}
	}
	if s.Profile.ReadOnly {
		return ti
	}
	// Result columns must be plain table columns, each once.
	seen := map[string]bool{}
	for _, c := range res.cols {
		if seen[c.Name] {
			return ti
		}
		seen[c.Name] = true
	}
	var key []dialect.Column
	for _, c := range cols {
		if c.PK > 0 {
			key = append(key, c)
		}
	}
	slices.SortFunc(key, func(a, b dialect.Column) int { return a.PK - b.PK })
	if len(key) == 0 {
		return ti
	}
	for _, k := range key {
		if !seen[k.Name] {
			return ti
		}
	}
	for _, k := range key {
		ti.Key = append(ti.Key, k.Name)
	}
	for _, c := range res.cols {
		if tc, ok := byName[c.Name]; ok && !tc.Generated && c.Kind != KindBinary {
			ti.Writable = append(ti.Writable, c.Name)
		}
	}
	return ti
}

// Requery reads a single-table result again with a new filter, order and row limit,
// on the tab connection.
func (e *Engine) Requery(ctx context.Context, s *session.Session, tabID, resultID, opID, where, orderBy string, limit, pageSize int64) (Result, error) {
	r, err := e.result(resultID)
	if err != nil {
		return Result{}, err
	}
	if r.table == nil {
		return Result{}, errors.New("this result does not come from a single table")
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	started := time.Now()
	ti := *r.table
	ti.Where, ti.OrderBy, ti.Limit = strings.TrimSpace(where), strings.TrimSpace(orderBy), max(limit, 0)
	q := dialect.Select(s.Driver, s.Driver.Qualify(ti.Database, ti.Schema, ti.Table), ti.Columns, ti.Where, ti.OrderBy)
	if ti.Limit > 0 {
		q = s.Driver.LimitRows(q, ti.Limit)
	}
	a := dialect.Analyze(q, s.Driver.Split())
	if !a.ReadOnly || a.Verb != "SELECT" {
		return Result{}, errors.New("the filter must not contain other statements")
	}
	t := e.tab(tabID, s)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cursor == r {
		t.closeCursorLocked()
	}
	ctx, done := s.Begin(ctx, opID)
	defer done()
	_, err = session.RetryRead(ctx, s, e.retry(opID), func(ctx context.Context) (struct{}, error) {
		if _, err := t.acquire(ctx, r.database, r.schema); err != nil {
			return struct{}{}, err
		}
		if t.cursor != nil {
			t.closeCursorLocked()
		}
		cursorCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		stop := t.stopper(cancel)
		unwatch := context.AfterFunc(ctx, stop)
		defer unwatch()
		rows, err := t.querier().QueryContext(cursorCtx, q) //nolint:rowserrcheck // the rows become the open cursor; readCursor checks Err and the tab closes them
		if err != nil {
			cancel()
			if s.Driver.Transient(err) && t.tx == nil {
				t.dropConnLocked("the connection to the server was lost")
			}
			return struct{}{}, err
		}
		r.stmt, r.wrappable, r.readOnly = q, true, true
		r.rows, r.start, r.bytes, r.done, r.total = nil, 0, 0, false, -1
		r.cursor, r.cancel, r.stop, r.used = rows, cancel, stop, time.Now()
		t.cursor = r
		if err := t.readCursor(r, pageSize+1); err != nil {
			t.closeCursorLocked()
			if s.Driver.Transient(err) && t.tx == nil {
				t.dropConnLocked("the connection to the server was lost")
			}
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	if err != nil {
		return Result{}, friendly(ctx, err)
	}
	r.table = &ti
	page, _ := r.window(0, pageSize)
	res := Result{
		ID: r.id, Kind: "rows", Title: "Query " + ti.Table, Statement: q, Columns: r.cols, Page: &page,
		RowsAffected: -1, Table: &ti, DurationMs: time.Since(started).Milliseconds(),
	}
	page.DurationMs = res.DurationMs
	return res, nil
}

// Edit is one change to a table row.
type Edit struct {
	// Op is update, insert or delete.
	Op string `json:"op"`
	// Key holds the primary key values of the row (update, delete).
	Key map[string]*string `json:"key"`
	// Values holds the new column values (update, insert); nil is NULL.
	Values map[string]*string `json:"values"`
}

// EditRequest is a set of changes to apply in one transaction.
type EditRequest struct {
	Table TableInfo `json:"table"`
	Edits []Edit    `json:"edits"`
	// Kinds maps column names to value kinds (number, bool...), to convert values.
	Kinds map[string]string `json:"kinds"`
}

// EditStatement is a generated statement, with its parameters and a readable form.
type EditStatement struct {
	SQL    string    `json:"sql"`
	Params []*string `json:"params"`
	// Text is the statement with the values written in, for the preview.
	Text string `json:"text"`
}

// PlanEdits generates the statements of an edit request.
func PlanEdits(syn dialect.Syntax, req EditRequest) ([]EditStatement, error) {
	ti := req.Table
	if ti.Table == "" {
		return nil, errors.New("no table")
	}
	table := syn.Qualify(ti.Database, ti.Schema, ti.Table)
	var out []EditStatement
	for _, ed := range req.Edits {
		var st EditStatement
		switch ed.Op {
		case "update":
			if len(ti.Key) == 0 {
				return nil, errors.New("the table has no primary key; rows cannot be identified")
			}
			cols := sortedKeys(ed.Values)
			if len(cols) == 0 {
				continue
			}
			for _, c := range cols {
				if !slices.Contains(ti.Writable, c) {
					return nil, fmt.Errorf("column %s cannot be changed", c)
				}
				st.Params = append(st.Params, ed.Values[c])
			}
			for _, k := range ti.Key {
				v, ok := ed.Key[k]
				if !ok || v == nil {
					return nil, fmt.Errorf("missing key value for %s", k)
				}
				st.Params = append(st.Params, v)
			}
			st.SQL = dialect.Update(syn, table, cols, ti.Key)
			st.Text = literalText(syn, dialect.Update(literalSyntax{syn}, table, cols, ti.Key), st.Params, append(slices.Clone(cols), ti.Key...), req.Kinds)
		case "insert":
			cols := sortedKeys(ed.Values)
			for _, c := range cols {
				st.Params = append(st.Params, ed.Values[c])
			}
			st.SQL = dialect.Insert(syn, table, cols)
			st.Text = literalText(syn, dialect.Insert(literalSyntax{syn}, table, cols), st.Params, cols, req.Kinds)
		case "delete":
			if len(ti.Key) == 0 {
				return nil, errors.New("the table has no primary key; rows cannot be identified")
			}
			for _, k := range ti.Key {
				v, ok := ed.Key[k]
				if !ok || v == nil {
					return nil, fmt.Errorf("missing key value for %s", k)
				}
				st.Params = append(st.Params, v)
			}
			st.SQL = dialect.Delete(syn, table, ti.Key)
			st.Text = literalText(syn, dialect.Delete(literalSyntax{syn}, table, ti.Key), st.Params, ti.Key, req.Kinds)
		default:
			return nil, fmt.Errorf("unknown edit %q", ed.Op)
		}
		out = append(out, st)
	}
	return out, nil
}

// ApplyEdits runs the statements of req in one transaction: the tab's own when it is
// in manual transaction mode (left open), otherwise a new one that is committed.
// Updates and deletes must each touch exactly one row.
func (e *Engine) ApplyEdits(ctx context.Context, s *session.Session, tabID string, req EditRequest) (int, error) {
	if s.Profile.ReadOnly {
		return 0, errors.New("this connection is read-only")
	}
	stmts, err := PlanEdits(s.Driver, req)
	if err != nil {
		return 0, err
	}
	args := func(st EditStatement) []any {
		out := make([]any, len(st.Params))
		for i, p := range st.Params {
			out[i] = paramValue(p)
		}
		return out
	}
	ctx, done := s.Begin(ctx, "")
	defer done()
	run := func(q querier) error {
		for i, st := range stmts {
			res, err := q.ExecContext(ctx, st.SQL, args(st)...)
			if err != nil {
				return fmt.Errorf("change %d: %w", i+1, err)
			}
			if req.Edits[i].Op != "insert" {
				if n, err := res.RowsAffected(); err == nil && n != 1 {
					return fmt.Errorf("change %d affected %d rows instead of 1: the row was changed or deleted meanwhile", i+1, n)
				}
			}
		}
		return nil
	}
	t := e.tab(tabID, s)
	t.mu.Lock()
	if t.manual {
		defer t.mu.Unlock()
		t.closeCursorLocked()
		if _, err := t.acquire(ctx, req.Table.Database, ""); err != nil {
			return 0, err
		}
		if t.tx == nil {
			tx, err := t.conn.BeginTx(context.Background(), nil)
			if err != nil {
				return 0, err
			}
			t.tx = tx
		}
		t.txStatements += len(stmts)
		return len(stmts), run(t.tx)
	}
	t.mu.Unlock()
	db, err := s.DB(ctx, poolFor(s, req.Table.Database))
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	if err := run(tx); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	return len(stmts), tx.Commit()
}

func paramValue(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func sortedKeys(m map[string]*string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// literalSyntax renders placeholders as markers replaced by literalText.
type literalSyntax struct{ dialect.Syntax }

func (literalSyntax) Placeholder(n int) string { return "\x00" + strconv.Itoa(n) + "\x00" }

func literalText(_ dialect.Syntax, q string, params []*string, cols []string, kinds map[string]string) string {
	for i := len(params); i >= 1; i-- {
		lit := "NULL"
		if p := params[i-1]; p != nil {
			kind := ""
			if i-1 < len(cols) {
				kind = kinds[cols[i-1]]
			}
			if (kind == KindNumber && isNumber(*p)) || (kind == KindBool && (*p == "true" || *p == "false")) {
				lit = *p
			} else {
				lit = "'" + strings.ReplaceAll(*p, "'", "''") + "'"
			}
		}
		q = strings.ReplaceAll(q, "\x00"+strconv.Itoa(i)+"\x00", lit)
	}
	return q
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return err == nil
}

package query

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/iagapie/quire/internal/dialect/sqlite"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/session"
)

func openSQLite(t *testing.T, readOnly bool) *session.Session {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	p := profile.Profile{ID: "t", Name: "t", Driver: "sqlite", Database: path, Options: map[string]string{"create": "true"}}
	p.Normalize()
	s, err := session.Open(t.Context(), "t", p, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if readOnly {
		e := New()
		defer e.Close()
		r := e.Run(t.Context(), s, "setup", "", RunRequest{Script: "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT);", Mode: ModeAll})
		if r.Results[0].Kind == "error" {
			t.Fatal(r.Results[0].Error)
		}
		s.Close()
		p.ReadOnly = true
		s, err = session.Open(t.Context(), "t", p, session.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
	}
	return s
}

func run(t *testing.T, e *Engine, s *session.Session, tab, script string, mode Mode, cursor int) RunResult {
	t.Helper()
	return e.Run(t.Context(), s, tab, "", RunRequest{Script: script, Mode: mode, Cursor: cursor, PageSize: 10})
}

func TestRunPagesAndSessionState(t *testing.T) {
	s := openSQLite(t, false)
	e := New()
	t.Cleanup(e.Close)

	var b strings.Builder
	b.WriteString("CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL, price REAL, data BLOB);\n")
	for i := 1; i <= 35; i++ {
		fmt.Fprintf(&b, "INSERT INTO items (name, price) VALUES ('item %d', %d.5);\n", i, i)
	}
	res := run(t, e, s, "tab1", b.String(), ModeAll, 0)
	if n := len(res.Results); n != 36 {
		t.Fatalf("results = %d", n)
	}
	if res.Results[1].RowsAffected != 1 || res.Results[1].Kind != "message" {
		t.Fatalf("insert result = %+v", res.Results[1])
	}

	res = run(t, e, s, "tab1", "SELECT id, name, price FROM items ORDER BY id", ModeAll, 0)
	r := res.Results[0]
	if r.Kind != "rows" || len(r.Page.Rows) != 10 || !r.Page.HasMore {
		t.Fatalf("first page = %+v", r)
	}
	if r.Columns[2].Kind != KindNumber || *r.Page.Rows[0][1] != "item 1" || *r.Page.Rows[0][2] != "1.5" {
		t.Fatalf("columns = %+v row = %v", r.Columns, *r.Page.Rows[0][1])
	}
	if r.Table == nil || len(r.Table.Key) != 1 || r.Table.Key[0] != "id" || r.Table.OrderBy != "id" {
		t.Fatalf("table info = %+v", r.Table)
	}
	// Next pages come from the open cursor, then from memory.
	p, err := e.FetchPage(t.Context(), s, "tab1", r.ID, "", 30, 10)
	if err != nil || len(p.Rows) != 5 || p.HasMore || *p.Rows[0][0] != "31" || p.Total != 35 {
		t.Fatalf("last page = %+v, %v", p, err)
	}
	p, err = e.FetchPage(t.Context(), s, "tab1", r.ID, "", 10, 10)
	if err != nil || len(p.Rows) != 10 || !p.HasMore || *p.Rows[0][0] != "11" {
		t.Fatalf("second page = %+v, %v", p, err)
	}
	if n, err := e.Count(t.Context(), s, "tab1", r.ID, ""); err != nil || n != 35 {
		t.Fatalf("count = %d, %v", n, err)
	}

	// A temporary table survives between runs on the same tab.
	res = run(t, e, s, "tab1", "CREATE TEMP TABLE scratch (x INT); INSERT INTO scratch VALUES (7);", ModeAll, 0)
	if res.Results[len(res.Results)-1].Kind == "error" {
		t.Fatal(res.Results[len(res.Results)-1].Error)
	}
	res = run(t, e, s, "tab1", "SELECT x FROM scratch", ModeAll, 0)
	if res.Results[0].Kind != "rows" || *res.Results[0].Page.Rows[0][0] != "7" {
		t.Fatalf("temp table lost: %+v", res.Results[0])
	}
	// ...but not in another tab.
	res = run(t, e, s, "tab2", "SELECT x FROM scratch", ModeAll, 0)
	if res.Results[0].Kind != "error" {
		t.Fatalf("other tab sees the temp table: %+v", res.Results[0])
	}
}

func TestRunCurrentStatementAndErrors(t *testing.T) {
	s := openSQLite(t, false)
	e := New()
	t.Cleanup(e.Close)
	script := "SELECT 1 AS a;\nSELECT 2 AS b;\nSELECT nope FROM missing;\nSELECT 4;"
	res := run(t, e, s, "t", script, ModeCurrent, len("SELECT 1 AS a;\nSEL"))
	if len(res.Results) != 1 || res.Results[0].Columns[0].Name != "b" || res.Results[0].Line != 2 {
		t.Fatalf("current = %+v", res.Results)
	}
	res = run(t, e, s, "t", script, ModeAll, 0)
	if len(res.Results) != 3 || res.Results[2].Kind != "error" || !strings.Contains(res.Results[2].Error, "missing") {
		t.Fatalf("all = %+v", res.Results)
	}
	// Selection runs only the selected text.
	res = e.Run(t.Context(), s, "t", "", RunRequest{Script: script, Mode: ModeSelection, SelStart: 15, SelEnd: 29, PageSize: 10})
	if len(res.Results) != 1 || res.Results[0].Columns[0].Name != "b" {
		t.Fatalf("selection = %+v", res.Results)
	}
}

func TestReadOnlyGuardAndEdits(t *testing.T) {
	ro := openSQLite(t, true)
	e := New()
	t.Cleanup(e.Close)
	res := run(t, e, ro, "t", "DELETE FROM t", ModeAll, 0)
	if res.Results[0].Kind != "error" || !strings.Contains(res.Results[0].Error, "read-only") {
		t.Fatalf("read-only guard: %+v", res.Results[0])
	}
	res = run(t, e, ro, "t", "SELECT * FROM t", ModeAll, 0)
	if res.Results[0].Kind != "rows" || len(res.Results[0].Table.Key) != 0 {
		t.Fatalf("read-only select: %+v", res.Results[0])
	}

	s := openSQLite(t, false)
	run(t, e, s, "w", "CREATE TABLE p (id INTEGER PRIMARY KEY, name TEXT, n INT); INSERT INTO p VALUES (1, 'a', 1), (2, 'b', 2);", ModeAll, 0)
	res = run(t, e, s, "w", "SELECT * FROM p", ModeAll, 0)
	ti := res.Results[0].Table
	if ti == nil || len(ti.Writable) != 3 {
		t.Fatalf("table = %+v", ti)
	}
	str := func(v string) *string { return &v }
	req := EditRequest{Table: *ti, Kinds: map[string]string{"n": KindNumber, "id": KindNumber}, Edits: []Edit{
		{Op: "update", Key: map[string]*string{"id": str("1")}, Values: map[string]*string{"name": str("it's"), "n": nil}},
		{Op: "insert", Values: map[string]*string{"id": str("3"), "name": str("c")}},
		{Op: "delete", Key: map[string]*string{"id": str("2")}},
	}}
	plan, err := PlanEdits(s.Driver, req)
	if err != nil {
		t.Fatal(err)
	}
	if plan[0].Text != `UPDATE "p" SET "n" = NULL, "name" = 'it''s' WHERE "id" = 1` {
		t.Errorf("preview = %s", plan[0].Text)
	}
	if n, err := e.ApplyEdits(t.Context(), s, "w", req); err != nil || n != 3 {
		t.Fatalf("apply = %d, %v", n, err)
	}
	res = run(t, e, s, "w", "SELECT id, name, n FROM p ORDER BY id", ModeAll, 0)
	rows := res.Results[0].Page.Rows
	if len(rows) != 2 || *rows[0][1] != "it's" || rows[0][2] != nil || *rows[1][0] != "3" {
		t.Fatalf("after edits: %v", rows)
	}
	// A stale key fails and changes nothing.
	bad := EditRequest{Table: *ti, Edits: []Edit{
		{Op: "update", Key: map[string]*string{"id": str("1")}, Values: map[string]*string{"name": str("x")}},
		{Op: "delete", Key: map[string]*string{"id": str("99")}},
	}}
	if _, err := e.ApplyEdits(t.Context(), s, "w", bad); err == nil {
		t.Fatal("expected an error for a missing row")
	}
	res = run(t, e, s, "w", "SELECT name FROM p WHERE id = 1", ModeAll, 0)
	if *res.Results[0].Page.Rows[0][0] != "it's" {
		t.Fatal("a failed edit batch was not rolled back")
	}

	// Requery with a filter.
	res = run(t, e, s, "w", "SELECT * FROM p", ModeAll, 0)
	rq, err := e.Requery(t.Context(), s, "w", res.Results[0].ID, "", "name = 'c'", "id DESC", 0, 10)
	if err != nil || len(rq.Page.Rows) != 1 || *rq.Page.Rows[0][1] != "c" || rq.Table.Where != "name = 'c'" {
		t.Fatalf("requery = %+v, %v", rq, err)
	}
	if _, err := e.Requery(t.Context(), s, "w", res.Results[0].ID, "", "1=1; DROP TABLE p", "", 0, 10); err == nil {
		t.Fatal("a filter with another statement must be rejected")
	}
}

func TestManualTransaction(t *testing.T) {
	s := openSQLite(t, false)
	e := New()
	t.Cleanup(e.Close)
	run(t, e, s, "a", "CREATE TABLE k (v INT)", ModeAll, 0)
	if _, err := e.SetManual(s, "a", true); err != nil {
		t.Fatal(err)
	}
	res := run(t, e, s, "a", "INSERT INTO k VALUES (1)", ModeAll, 0)
	if !res.Tx.Open || res.Tx.Statements != 1 {
		t.Fatalf("tx = %+v", res.Tx)
	}
	if len(e.OpenTransactions()) != 1 {
		t.Fatal("open transaction not reported")
	}
	if _, err := e.SetManual(s, "a", false); err == nil {
		t.Fatal("leaving manual mode with an open transaction must fail")
	}
	if st, err := e.Rollback(t.Context(), s, "a"); err != nil || st.Open {
		t.Fatalf("rollback = %+v, %v", st, err)
	}
	res = run(t, e, s, "a", "SELECT COUNT(*) FROM k", ModeAll, 0)
	if *res.Results[0].Page.Rows[0][0] != "0" {
		t.Fatal("rollback did not undo the insert")
	}
	run(t, e, s, "a", "INSERT INTO k VALUES (2); COMMIT;", ModeAll, 0)
	res = run(t, e, s, "b", "SELECT COUNT(*) FROM k", ModeAll, 0)
	if *res.Results[0].Page.Rows[0][0] != "1" {
		t.Fatal("COMMIT in the script did not commit")
	}
	e.CloseTab("a")
	if len(e.OpenTransactions()) != 0 {
		t.Fatal("closing the tab must end its transaction")
	}
}

func TestByteOffset(t *testing.T) {
	s := "a😀b"
	if byteOffset(s, 3) != 5 || byteOffset(s, 0) != 0 || byteOffset(s, 10) != len(s) {
		t.Error("byteOffset")
	}
}

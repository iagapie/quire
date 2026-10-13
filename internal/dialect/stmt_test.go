package dialect

import (
	"slices"
	"strings"
	"testing"

	"github.com/iagapie/quire/internal/sqlsplit"
)

var pg = sqlsplit.Options{DollarQuotes: true, NestedComments: true, EStrings: true}

func TestAnalyze(t *testing.T) {
	cases := []struct {
		stmt      string
		kind      Kind
		readOnly  bool
		wrappable bool
		tx        string
	}{
		{"SELECT * FROM t", KindQuery, true, true, ""},
		{"  select 1", KindQuery, true, true, ""},
		{"(SELECT 1) UNION (SELECT 2)", KindQuery, true, true, ""},
		{"WITH x AS (SELECT 1) SELECT * FROM x", KindQuery, true, true, ""},
		{"WITH x AS (DELETE FROM t RETURNING *) SELECT * FROM x", KindQuery, true, true, ""},
		{"WITH x AS (SELECT 1) DELETE FROM t", KindExec, false, false, ""},
		{"SELECT * INTO new_t FROM t", KindOther, false, false, ""},
		{"SELECT a FROM t WHERE b IN (SELECT c INTO x FROM y)", KindQuery, true, true, ""},
		{"VALUES (1), (2)", KindQuery, true, true, ""},
		{"SHOW search_path", KindQuery, true, false, ""},
		{"DESCRIBE t", KindQuery, true, false, ""},
		{"EXPLAIN SELECT 1", KindQuery, true, false, ""},
		{"EXPLAIN ANALYZE SELECT 1", KindQuery, true, false, ""},
		{"EXPLAIN ANALYZE DELETE FROM t", KindQuery, false, false, ""},
		{"EXPLAIN (ANALYZE, BUFFERS) UPDATE t SET a = 1", KindQuery, false, false, ""},
		{"PRAGMA table_info(t)", KindQuery, true, false, ""},
		{"PRAGMA foreign_keys = ON", KindQuery, false, false, ""},
		{"PRAGMA optimize", KindQuery, false, false, ""},
		{"INSERT INTO t VALUES (1)", KindExec, false, false, ""},
		{"INSERT INTO t VALUES (1) RETURNING id", KindQuery, false, false, ""},
		{"UPDATE t SET a = 1", KindExec, false, false, ""},
		{"DELETE FROM t OUTPUT deleted.id", KindQuery, false, false, ""},
		{"CREATE TABLE t (a int)", KindExec, false, false, ""},
		{"SET search_path TO x", KindExec, false, false, ""},
		{"BEGIN", KindExec, false, false, "BEGIN"},
		{"BEGIN TRANSACTION", KindExec, false, false, "BEGIN"},
		{"START TRANSACTION READ ONLY", KindExec, false, false, "BEGIN"},
		{"BEGIN\n  NULL;\nEND;", KindOther, false, false, ""},
		{"COMMIT", KindExec, true, false, "COMMIT"},
		{"ROLLBACK", KindExec, true, false, "ROLLBACK"},
		{"ROLLBACK TO SAVEPOINT a", KindExec, true, false, ""},
		{"CALL p()", KindOther, false, false, ""},
		{"-- only a comment", KindOther, false, false, ""},
	}
	for _, c := range cases {
		a := Analyze(c.stmt, pg)
		if a.Kind != c.kind || a.ReadOnly != c.readOnly || a.Wrappable != c.wrappable || a.Transaction != c.tx {
			t.Errorf("Analyze(%q) = %+v, want kind=%d ro=%v wrap=%v tx=%q", c.stmt, a, c.kind, c.readOnly, c.wrappable, c.tx)
		}
	}
}

func TestCheckReadOnly(t *testing.T) {
	for _, ok := range []string{"SELECT 1", "WITH a AS (SELECT 1) SELECT * FROM a", "EXPLAIN SELECT 1", "SHOW tables", "DESCRIBE t", "PRAGMA table_info(t)"} {
		if err := CheckReadOnly(Analyze(ok, pg)); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"DELETE FROM t", "SELECT 1 INTO x", "EXPLAIN ANALYZE DELETE FROM t", "PRAGMA journal_mode = WAL", "SET default_transaction_read_only = off", "BEGIN READ WRITE", "CALL p()"} {
		err := CheckReadOnly(Analyze(bad, pg))
		if err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Errorf("%q allowed", bad)
		}
	}
}

func TestSingleTable(t *testing.T) {
	cases := []struct {
		stmt string
		ok   bool
		want TableRef
	}{
		{"SELECT * FROM orders", true, TableRef{Table: "orders"}},
		{"select * from Sales.Orders o where o.id > 1 order by id desc limit 50", true, TableRef{Schema: "Sales", Table: "Orders", Where: "o.id > 1", OrderBy: "id desc", Limit: 50}},
		{`SELECT "Id", name FROM "My Schema"."My Table"`, true, TableRef{Schema: "My Schema", Table: "My Table", SchemaQuoted: true, TableQuoted: true, Columns: []string{"Id", "name"}}},
		{"SELECT TOP 10 * FROM dbo.t ORDER BY a", true, TableRef{Schema: "dbo", Table: "t", OrderBy: "a", Limit: 10}},
		{"SELECT * FROM t FETCH FIRST 5 ROWS ONLY", true, TableRef{Table: "t", Limit: 5}},
		{"SELECT t.* FROM t", true, TableRef{Table: "t"}},
		{"SELECT a + 1 FROM t", false, TableRef{}},
		{"SELECT count(*) FROM t", false, TableRef{}},
		{"SELECT * FROM a JOIN b ON a.id = b.id", false, TableRef{}},
		{"SELECT * FROM a, b", false, TableRef{}},
		{"SELECT DISTINCT a FROM t", false, TableRef{}},
		{"SELECT a FROM t GROUP BY a", false, TableRef{}},
		{"SELECT * FROM (SELECT 1) x", false, TableRef{}},
		{"SELECT * FROM a UNION SELECT * FROM b", false, TableRef{}},
		{"SELECT * FROM db.s.t", false, TableRef{}},
		{"INSERT INTO t VALUES (1)", false, TableRef{}},
		{"SELECT * FROM t WHERE id IN (SELECT id FROM u JOIN v USING (id))", true, TableRef{Table: "t", Where: "id IN (SELECT id FROM u JOIN v USING (id))"}},
	}
	for _, c := range cases {
		got, ok := SingleTable(c.stmt, pg)
		if ok != c.ok {
			t.Errorf("SingleTable(%q) ok = %v", c.stmt, ok)
			continue
		}
		if ok && (got.Schema != c.want.Schema || got.Table != c.want.Table || got.Where != c.want.Where || got.OrderBy != c.want.OrderBy ||
			got.Limit != c.want.Limit || !slices.Equal(got.Columns, c.want.Columns) || got.TableQuoted != c.want.TableQuoted) {
			t.Errorf("SingleTable(%q) = %+v, want %+v", c.stmt, got, c.want)
		}
	}
}

type testSyntax struct{}

func (testSyntax) Quote(s string) string                    { return QuoteDouble(s) }
func (testSyntax) Placeholder(n int) string                 { return PlaceholderDollar(n) }
func (testSyntax) Limit(q string, l, o int64) string        { return WrapLimitOffset(q, l, o) }
func (testSyntax) LimitRows(q string, l int64) string       { return AppendLimit(q, l) }
func (testSyntax) Split() sqlsplit.Options                  { return pg }
func (testSyntax) Use(string, string) []string              { return nil }
func (testSyntax) Explain(s string) Plan                    { return Plan{Query: "EXPLAIN " + s} }
func (testSyntax) Upsert(string, []string, []string) string { return "" }
func (testSyntax) Qualify(db, schema, name string) string   { return Join(QuoteDouble, schema, name) }

func TestSQLGeneration(t *testing.T) {
	s := testSyntax{}
	if got := Update(s, `"t"`, []string{"a", "b"}, []string{"id"}); got != `UPDATE "t" SET "a" = $1, "b" = $2 WHERE "id" = $3` {
		t.Error(got)
	}
	if got := Insert(s, `"t"`, []string{"a"}); got != `INSERT INTO "t" ("a") VALUES ($1)` {
		t.Error(got)
	}
	if got := Insert(s, `"t"`, nil); got != `INSERT INTO "t" DEFAULT VALUES` {
		t.Error(got)
	}
	if got := InsertRows(s, `"t"`, []string{"a", "b"}, 2); got != `INSERT INTO "t" ("a", "b") VALUES ($1, $2), ($3, $4)` {
		t.Error(got)
	}
	if got := Delete(s, `"t"`, []string{"a", "b"}); got != `DELETE FROM "t" WHERE "a" = $1 AND "b" = $2` {
		t.Error(got)
	}
	if got := Select(s, `"t"`, []string{"a"}, " x > 1 ", "a"); got != "SELECT \"a\" FROM \"t\"\nWHERE x > 1\nORDER BY a" {
		t.Error(got)
	}
	// A trailing comment must not swallow the closing parenthesis.
	if got := WrapLimitOffset("SELECT 1 -- note", 10, 20); got != "SELECT * FROM (\nSELECT 1 -- note\n) quire_q LIMIT 10 OFFSET 20" {
		t.Error(got)
	}
	if got := CountQuery("SELECT 1;"); got != "SELECT COUNT(*) FROM (\nSELECT 1\n) quire_q" {
		t.Error(got)
	}
	if got := WrapOffsetFetch("SELECT 1", 5, 0); got != "SELECT * FROM (\nSELECT 1\n) quire_q OFFSET 0 ROWS FETCH NEXT 5 ROWS ONLY" {
		t.Error(got)
	}
	if QuoteWith(`a"b`, '"', '"') != `"a""b"` || QuoteWith("a]b", '[', ']') != "[a]]b]" {
		t.Error("QuoteWith")
	}
}

func TestLexKeepsQuotedText(t *testing.T) {
	toks := Lex(`select 'a''b', "x""y", $tag$ ; $tag$ -- c`, pg)
	if len(toks) != 6 || toks[1].Value != "a'b" || toks[3].Value != `x"y` || toks[5].Kind != TokString {
		t.Fatalf("tokens = %+v", toks)
	}
}

func TestSingleTableBatchTerminator(t *testing.T) {
	o := sqlsplit.Options{Brackets: true, GoBatches: true}
	ref, ok := SingleTable("SELECT TOP (1000) * FROM dbo.orders;", o)
	if !ok || ref.Schema != "dbo" || ref.Table != "orders" || ref.Limit != 1000 {
		t.Errorf("ref = %+v, %v", ref, ok)
	}
}

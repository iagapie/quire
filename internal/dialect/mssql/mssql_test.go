package mssql

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/sqlsplit"
)

// testProfile returns the profile of the test server (QUIRE_TEST_SQLSERVER=127.0.0.1:51433
// and QUIRE_TEST_SQLSERVER_PASSWORD, see scripts/test-databases.sh), after creating
// the quire_test database. It skips the test when the server is not configured.
func testProfile(t *testing.T) profile.Profile {
	t.Helper()
	addr := os.Getenv("QUIRE_TEST_SQLSERVER")
	if addr == "" {
		t.Skip("QUIRE_TEST_SQLSERVER not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	p := profile.Profile{ID: "ms", Name: "ms", Driver: "sqlserver", Host: host, Port: n, User: "sa", Password: os.Getenv("QUIRE_TEST_SQLSERVER_PASSWORD"),
		Database: "master", TLS: profile.TLS{Mode: profile.TLSPrefer}}
	db, err := (&Driver{}).Open(t.Context(), p, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), "IF DB_ID('quire_test') IS NULL CREATE DATABASE quire_test"); err != nil {
		t.Fatal(err)
	}
	p.Database = "quire_test"
	return p
}

func TestCatalog(t *testing.T) {
	p := testProfile(t)
	d := &Driver{}
	db, err := d.Open(t.Context(), p, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := t.Context()
	exec := func(q string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	drop := func() {
		for _, q := range []string{"DROP VIEW IF EXISTS cat.v", "DROP TABLE IF EXISTS cat.[child table]", "DROP TABLE IF EXISTS cat.parent",
			"DROP FUNCTION IF EXISTS cat.f", "DROP PROCEDURE IF EXISTS cat.p", "DROP SEQUENCE IF EXISTS cat.seq", "DROP SCHEMA IF EXISTS cat"} {
			_, _ = db.ExecContext(context.Background(), q)
		}
	}
	drop()
	t.Cleanup(drop)
	exec("CREATE SCHEMA cat")
	exec("CREATE TABLE cat.parent (id int NOT NULL CONSTRAINT pk_parent PRIMARY KEY, code nvarchar(20) CONSTRAINT uq_code UNIQUE, uid uniqueidentifier NULL)")
	exec(`CREATE TABLE cat.[child table] (a int NOT NULL, b int NOT NULL, name nvarchar(50) NOT NULL CONSTRAINT df_name DEFAULT ('x'),
		total AS (a * 2) PERSISTED, n int IDENTITY(10, 5), price decimal(10, 2) NULL, parent_id int NULL,
		CONSTRAINT pk_child PRIMARY KEY (b, a), CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES cat.parent (id) ON DELETE CASCADE,
		CONSTRAINT ck_a CHECK (a > 0))`)
	exec("CREATE NONCLUSTERED INDEX child_name ON cat.[child table] (name DESC) INCLUDE (price) WHERE a > 10 WITH (FILLFACTOR = 80)")
	exec("CREATE VIEW cat.v AS SELECT id FROM cat.parent")
	exec("CREATE FUNCTION cat.f(@x int) RETURNS int AS BEGIN RETURN @x + 1 END")
	exec("CREATE PROCEDURE cat.p AS SELECT 1 AS one")
	exec("CREATE SEQUENCE cat.seq AS bigint START WITH 10 INCREMENT BY 5")
	exec("CREATE TRIGGER cat.trg ON cat.parent AFTER INSERT AS SET NOCOUNT ON")

	dbs, err := d.Databases(ctx, db)
	if err != nil || dbs[0] != "quire_test" {
		t.Errorf("databases = %v, %v", dbs, err)
	}
	schemas, err := d.Schemas(ctx, db, "")
	if err != nil || !strings.Contains(strings.Join(schemas, ","), "cat,dbo") || strings.Contains(strings.Join(schemas, ","), "db_owner") {
		t.Errorf("schemas = %v, %v", schemas, err)
	}
	objs, err := d.Objects(ctx, db, "", "cat")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, o := range objs {
		kinds[o.Name] = o.Kind
		if o.Name == "trg" && o.Table != "parent" {
			t.Errorf("trigger table = %q", o.Table)
		}
	}
	for name, kind := range map[string]string{"parent": "table", "child table": "table", "v": "view", "f": "function", "p": "procedure", "seq": "sequence", "trg": "trigger"} {
		if kinds[name] != kind {
			t.Errorf("object %s = %q, want %s", name, kinds[name], kind)
		}
	}
	cols, err := d.Columns(ctx, db, "", "cat", "child table")
	if err != nil || len(cols) != 7 {
		t.Fatalf("columns = %+v, %v", cols, err)
	}
	if cols[0].PK != 2 || cols[1].PK != 1 || cols[2].Default != "'x'" || cols[2].Type != "nvarchar(50)" || !cols[3].Generated || !cols[4].Generated ||
		cols[5].Type != "decimal(10, 2)" || !cols[5].Nullable {
		t.Errorf("columns = %+v", cols)
	}
	ixs, err := d.Indexes(ctx, db, "", "cat", "child table")
	if err != nil || len(ixs) != 2 {
		t.Fatalf("indexes = %+v, %v", ixs, err)
	}
	if !ixs[0].Primary || !ixs[0].Clustered || ixs[0].Constraint != "pk_child" {
		t.Errorf("primary = %+v", ixs[0])
	}
	if ix := ixs[1]; ix.Definition != "CREATE NONCLUSTERED INDEX [child_name] ON [cat].[child table] ([name] DESC) INCLUDE ([price]) WHERE [a]>(10) WITH (FILLFACTOR = 80)" {
		t.Errorf("index = %s", ix.Definition)
	}
	cons, err := d.Constraints(ctx, db, "", "cat", "child table")
	if err != nil || len(cons) != 3 {
		t.Fatalf("constraints = %+v, %v", cons, err)
	}
	if cons[0].Type != "PRIMARY KEY" || strings.Join(cons[0].Columns, ",") != "b,a" ||
		cons[1].Definition != "FOREIGN KEY ([parent_id]) REFERENCES [cat].[parent] ([id]) ON DELETE CASCADE" || cons[2].Definition != "CHECK ([a]>(0))" {
		t.Errorf("constraints = %+v", cons)
	}
	ddl, err := d.DDL(ctx, db, "", "cat", dialect.Object{Name: "child table", Kind: "table"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[total] AS ([a]*(2)) PERSISTED", "IDENTITY(10, 5)", "CONSTRAINT [df_name] DEFAULT ('x')", "CREATE NONCLUSTERED INDEX [child_name]"} {
		if !strings.Contains(ddl, want) {
			t.Errorf("table DDL lacks %q:\n%s", want, ddl)
		}
	}
	// The DDL runs again as it is, batch by batch.
	exec("DROP TABLE cat.[child table]")
	for _, st := range sqlsplit.Split(ddl, d.Split()) {
		exec(st.Text)
	}
	for _, o := range []dialect.Object{{Name: "v", Kind: "view"}, {Name: "p", Kind: "procedure"}, {Name: "seq", Kind: "sequence"}, {Name: "trg", Kind: "trigger"}} {
		ddl, err := d.DDL(ctx, db, "", "cat", o)
		if err != nil || !strings.Contains(ddl, "CREATE") || !strings.HasSuffix(ddl, "GO\n") {
			t.Errorf("ddl %s = %q, %v", o.Name, ddl, err)
		}
	}
	stats, err := d.TableStats(ctx, db, "", "cat")
	if err != nil || len(stats) != 3 || stats[0].Rows != 0 {
		t.Errorf("stats = %+v, %v", stats, err)
	}
	up := d.Upsert("[cat].[parent]", []string{"id", "code"}, []string{"id"})
	for _, code := range []string{"a", "b"} {
		if _, err := db.ExecContext(ctx, up, 7, code); err != nil {
			t.Fatalf("%s: %v", up, err)
		}
	}
	var code string
	if err := db.QueryRowContext(ctx, "SELECT code FROM cat.parent WHERE id = 7").Scan(&code); err != nil || code != "b" {
		t.Errorf("upsert left %q, %v", code, err)
	}
	if cur, schema, err := d.Current(ctx, db); err != nil || cur != "quire_test" || schema != "dbo" {
		t.Errorf("current = %q %q, %v", cur, schema, err)
	}
	if v, err := d.Version(ctx, db); err != nil || !strings.HasPrefix(v, "16.") {
		t.Errorf("version = %q, %v", v, err)
	}
	if v, ok := d.FormatValue("UNIQUEIDENTIFIER", mustGUID(t, db)); !ok || v != "6F9619FF-8B86-D011-B42D-00C04FC964FF" {
		t.Errorf("guid = %s", v)
	}
}

func mustGUID(t *testing.T, db *sql.DB) []byte {
	t.Helper()
	var b []byte
	if err := db.QueryRowContext(t.Context(), "SELECT CAST('6F9619FF-8B86-D011-B42D-00C04FC964FF' AS uniqueidentifier)").Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSyntax(t *testing.T) {
	d := &Driver{}
	for _, c := range []struct{ in, want string }{
		{"SELECT a FROM t ORDER BY a;", "SELECT a FROM t ORDER BY a\nOFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY"},
		{"SELECT a FROM t", "SELECT * FROM (\nSELECT a FROM t\n) quire_q ORDER BY (SELECT NULL) OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY"},
		{"SELECT TOP 5 a FROM t ORDER BY a", "SELECT * FROM (\nSELECT TOP 5 a FROM t ORDER BY a\n) quire_q ORDER BY (SELECT NULL) OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY"},
		{"SELECT a FROM (SELECT TOP 1 a FROM t ORDER BY a) x", "SELECT * FROM (\nSELECT a FROM (SELECT TOP 1 a FROM t ORDER BY a) x\n) quire_q ORDER BY (SELECT NULL) OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY"},
	} {
		if got := d.Limit(c.in, 10, 20); got != c.want {
			t.Errorf("Limit(%q) = %q", c.in, got)
		}
	}
	if got := d.Count("SELECT a FROM t ORDER BY a"); !strings.Contains(got, "ORDER BY a\nOFFSET 0 ROWS") {
		t.Errorf("count = %q", got)
	}
	if got := d.LimitRows("SELECT DISTINCT a FROM t", 5); got != "SELECT DISTINCT TOP (5) a FROM t" {
		t.Errorf("limit rows = %q", got)
	}
	if d.Quote("a]b") != "[a]]b]" || d.Placeholder(3) != "@p3" {
		t.Error("quoting")
	}
	cfg, err := config(profile.Profile{Host: "h", User: "u", Password: "p@ss", Database: "d", ReadOnly: true, Options: map[string]string{"params": "packet size=8192"}}, "")
	if err != nil || cfg.Port != 1433 || cfg.Database != "d" || !cfg.ReadOnlyIntent || cfg.AppName != "Quire" || cfg.PacketSize != 8192 || cfg.Password != "p@ss" {
		t.Errorf("config = %+v, %v", cfg, err)
	}
	if got := stripParens("(('a'')b'))"); got != "'a'')b'" {
		t.Errorf("strip = %q", got)
	}
}

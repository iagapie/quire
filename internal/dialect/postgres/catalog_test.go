package postgres

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
)

// QUIRE_TEST_POSTGRES=127.0.0.1:55432 go test ./internal/dialect/postgres
func testDB(t *testing.T) (*Driver, *sql.DB) {
	t.Helper()
	addr := os.Getenv("QUIRE_TEST_POSTGRES")
	if addr == "" {
		t.Skip("QUIRE_TEST_POSTGRES not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	d := &Driver{info: info()}
	p := profile.Profile{Driver: "postgres", Host: host, Port: n, User: "quire", Password: "quirepw", Database: "shop", TLS: profile.TLS{Mode: profile.TLSDisable}}
	db, err := d.Open(t.Context(), p, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	exec := func(q string) {
		if _, err := db.ExecContext(t.Context(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`DROP SCHEMA IF EXISTS quire_cat CASCADE`)
	exec(`CREATE SCHEMA quire_cat`)
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS quire_cat CASCADE`) })
	exec(`CREATE TABLE quire_cat.parent (id int PRIMARY KEY, code text UNIQUE)`)
	exec(`CREATE TABLE quire_cat."Child Table" (
		a int, b int, name text NOT NULL DEFAULT 'x', total int GENERATED ALWAYS AS (a * 2) STORED,
		parent_id int REFERENCES quire_cat.parent(id) ON DELETE CASCADE,
		PRIMARY KEY (b, a), CHECK (a > 0))`)
	exec(`COMMENT ON COLUMN quire_cat."Child Table".name IS 'the name'`)
	exec(`CREATE INDEX child_partial ON quire_cat."Child Table" (name DESC NULLS LAST, lower(name)) INCLUDE (parent_id) WITH (fillfactor = 70) WHERE a > 10`)
	exec(`CREATE VIEW quire_cat.v AS SELECT id FROM quire_cat.parent`)
	exec(`CREATE MATERIALIZED VIEW quire_cat.mv AS SELECT count(*) AS n FROM quire_cat.parent`)
	exec(`CREATE SEQUENCE quire_cat.seq START 10 INCREMENT 5`)
	exec(`CREATE FUNCTION quire_cat.f(x int) RETURNS int LANGUAGE sql AS $$ SELECT x + 1 $$`)
	exec(`CREATE PROCEDURE quire_cat.p() LANGUAGE sql AS $$ SELECT 1 $$`)
	exec(`CREATE FUNCTION quire_cat.tf() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`)
	exec(`CREATE TRIGGER trg BEFORE INSERT ON quire_cat.parent FOR EACH ROW EXECUTE FUNCTION quire_cat.tf()`)
	return d, db
}

func TestCatalog(t *testing.T) {
	d, db := testDB(t)
	ctx := t.Context()

	dbs, err := d.Databases(ctx, db)
	if err != nil || !contains(dbs, "shop") {
		t.Fatalf("databases = %v, %v", dbs, err)
	}
	schemas, err := d.Schemas(ctx, db, "")
	if err != nil || !contains(schemas, "quire_cat") || schemas[len(schemas)-1] != "pg_catalog" && schemas[len(schemas)-1] != "information_schema" {
		t.Fatalf("schemas = %v, %v", schemas, err)
	}
	objs, err := d.Objects(ctx, db, "", "quire_cat")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	var fn, trg dialect.Object
	for _, o := range objs {
		kinds[o.Name] = o.Kind
		switch o.Name {
		case "f":
			fn = o
		case "trg":
			trg = o
		}
	}
	want := map[string]string{"parent": "table", "Child Table": "table", "v": "view", "mv": "matview", "seq": "sequence", "f": "function", "p": "procedure", "tf": "function", "trg": "trigger"}
	for name, kind := range want {
		if kinds[name] != kind {
			t.Errorf("object %s kind = %q, want %q", name, kinds[name], kind)
		}
	}
	if fn.Detail != "x integer" || trg.Table != "parent" {
		t.Errorf("function detail %q, trigger table %q", fn.Detail, trg.Table)
	}

	cols, err := d.Columns(ctx, db, "", "quire_cat", "Child Table")
	if err != nil || len(cols) != 5 {
		t.Fatalf("columns = %+v, %v", cols, err)
	}
	byName := map[string]dialect.Column{}
	for _, c := range cols {
		byName[c.Name] = c
	}
	if byName["a"].PK != 2 || byName["b"].PK != 1 || byName["name"].Nullable || byName["name"].Default != "'x'::text" ||
		!byName["total"].Generated || byName["name"].Comment != "the name" || byName["a"].Type != "integer" {
		t.Errorf("columns = %+v", byName)
	}

	ixs, err := d.Indexes(ctx, db, "", "quire_cat", "Child Table")
	if err != nil || len(ixs) != 2 {
		t.Fatalf("indexes = %+v, %v", ixs, err)
	}
	if !ixs[0].Primary || ixs[0].Constraint == "" || len(ixs[0].Columns) != 2 || ixs[0].Columns[0].Name != "b" {
		t.Errorf("primary = %+v", ixs[0])
	}
	ix := ixs[1]
	if ix.Name != "child_partial" || ix.Where != "a > 10" || ix.FillFactor != 70 || len(ix.Include) != 1 || ix.Include[0] != "parent_id" ||
		len(ix.Columns) != 2 || !ix.Columns[0].Desc || ix.Columns[0].Nulls != "last" || ix.Columns[1].Expression != "lower(name)" {
		t.Errorf("partial index = %+v", ix)
	}
	// The generated statement recreates the same index.
	stmt, err := d.CreateIndex("", "quire_cat", ix, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, d.DropIndex("", "quire_cat", "", ix.Name, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
	again, _ := d.Indexes(ctx, db, "", "quire_cat", "Child Table")
	if len(again) != 2 || again[1].Definition != ix.Definition {
		t.Errorf("recreated index differs:\n%s\n%s", again[1].Definition, ix.Definition)
	}

	cons, err := d.Constraints(ctx, db, "", "quire_cat", "Child Table")
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, c := range cons {
		types = append(types, c.Type)
		if c.Type == "FOREIGN KEY" && (c.RefTable != "parent" || c.OnDelete != "CASCADE" || c.Columns[0] != "parent_id") {
			t.Errorf("fk = %+v", c)
		}
	}
	if strings.Join(types, ",") != "PRIMARY KEY,FOREIGN KEY,CHECK" {
		t.Errorf("constraint types = %v", types)
	}

	for _, o := range []dialect.Object{{Name: "Child Table", Kind: "table"}, {Name: "v", Kind: "view"}, {Name: "mv", Kind: "matview"}, fn, trg, {Name: "seq", Kind: "sequence"}} {
		ddl, err := d.DDL(ctx, db, "", "quire_cat", o)
		if err != nil || ddl == "" {
			t.Errorf("DDL %s: %v", o.Name, err)
			continue
		}
		if o.Kind == "table" && (!strings.Contains(ddl, `CREATE TABLE quire_cat."Child Table"`) && !strings.Contains(ddl, `CREATE TABLE "quire_cat"."Child Table"`) ||
			!strings.Contains(ddl, "child_partial") || !strings.Contains(ddl, "COMMENT ON COLUMN")) {
			t.Errorf("table DDL:\n%s", ddl)
		}
	}
	// The table DDL runs: drop and recreate the table from it.
	ddl, _ := d.DDL(ctx, db, "", "quire_cat", dialect.Object{Name: "Child Table", Kind: "table"})
	if _, err := db.ExecContext(ctx, `DROP TABLE quire_cat."Child Table"`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("table DDL does not run: %v\n%s", err, ddl)
	}

	stats, err := d.TableStats(ctx, db, "", "quire_cat")
	if err != nil || len(stats) != 4 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}

	// Upsert inserts, then updates on the key.
	up := d.Upsert(d.Qualify("", "quire_cat", "parent"), []string{"id", "code"}, []string{"id"})
	for _, code := range []string{"a", "b"} {
		if _, err := db.ExecContext(ctx, up, 1, code); err != nil {
			t.Fatalf("%s: %v", up, err)
		}
	}
	var code string
	if err := db.QueryRowContext(ctx, `SELECT code FROM quire_cat.parent WHERE id = 1`).Scan(&code); err != nil || code != "b" {
		t.Fatalf("upsert code = %q, %v", code, err)
	}

	if v, err := d.Version(ctx, db); err != nil || v == "" {
		t.Errorf("version = %q, %v", v, err)
	}
	if dbName, schema, err := d.Current(ctx, db); err != nil || dbName != "shop" || schema != "public" {
		t.Errorf("current = %q %q %v", dbName, schema, err)
	}
}

func TestReadOnlyServerSide(t *testing.T) {
	addr := os.Getenv("QUIRE_TEST_POSTGRES")
	if addr == "" {
		t.Skip("QUIRE_TEST_POSTGRES not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	d := &Driver{info: info()}
	p := profile.Profile{Driver: "postgres", Host: host, Port: n, User: "quire", Password: "quirepw", Database: "shop", TLS: profile.TLS{Mode: profile.TLSDisable}, ReadOnly: true}
	db, err := d.Open(t.Context(), p, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// Even a write hidden in a SELECT is refused by the server.
	_, err = db.ExecContext(t.Context(), `SELECT nextval('quire_ro_missing_seq')`)
	if err == nil {
		t.Fatal("expected an error")
	}
	_, err = db.ExecContext(t.Context(), `CREATE TABLE quire_ro_test (a int)`)
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("write on read-only connection: %v", err)
	}
}

func TestSyntax(t *testing.T) {
	d := &Driver{info: info()}
	if d.Qualify("db", "s", "T") != `"s"."T"` || d.Placeholder(3) != "$3" {
		t.Error("qualify/placeholder")
	}
	if got := d.Use("", "sales"); len(got) != 1 || got[0] != `SET search_path TO "sales", public` {
		t.Errorf("use = %v", got)
	}
	if got := d.Upsert(`"t"`, []string{"id"}, []string{"id"}); !strings.HasSuffix(got, `ON CONFLICT ("id") DO NOTHING`) {
		t.Errorf("upsert = %s", got)
	}
	if !d.Plain("orders") || d.Plain("Orders") || d.Plain("my table") || dialect.Readable(d, "user") != `"user"` {
		t.Error("plain identifiers")
	}
	if !d.Transient(&testNetError{}) || d.Transient(nil) {
		t.Error("transient")
	}
	if sslMode("") != "prefer" || quoteValue("a b") != `'a b'` || quoteValue(`it's`) != `'it\'s'` {
		t.Error("dsn helpers")
	}
	split, op := splitOpClass(`"My Col" text_pattern_ops`)
	if split != "My Col" || op != "text_pattern_ops" {
		t.Errorf("opclass = %q %q", split, op)
	}
}

type testNetError struct{}

func (testNetError) Error() string   { return "read tcp: connection reset by peer" }
func (testNetError) Timeout() bool   { return false }
func (testNetError) Temporary() bool { return true }

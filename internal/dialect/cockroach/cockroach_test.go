package cockroach

import (
	"context"
	"database/sql"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/dialect/postgres"
	"github.com/iagapie/quire/internal/profile"
)

// QUIRE_TEST_COCKROACH=127.0.0.1:56257 go test ./internal/dialect/cockroach
func testDB(t *testing.T) (*Driver, *sql.DB) {
	t.Helper()
	addr := os.Getenv("QUIRE_TEST_COCKROACH")
	if addr == "" {
		t.Skip("QUIRE_TEST_COCKROACH not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	d := &Driver{postgres.New(info())}
	p := profile.Profile{Driver: "cockroachdb", Host: host, Port: n, User: "root", Database: "defaultdb", TLS: profile.TLS{Mode: profile.TLSDisable}}
	db, err := d.Open(t.Context(), p, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	exec := func(q string) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`DROP SCHEMA IF EXISTS quire_cat CASCADE`)
	exec(`CREATE SCHEMA quire_cat`)
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS quire_cat CASCADE`) })
	exec(`CREATE TABLE quire_cat.parent (id int PRIMARY KEY, code text UNIQUE)`)
	exec(`CREATE TABLE quire_cat."Child Table" (
		a int, b int, name text NOT NULL DEFAULT 'x', total int AS (a * 2) STORED,
		parent_id int REFERENCES quire_cat.parent(id) ON DELETE CASCADE,
		PRIMARY KEY (b, a), CHECK (a > 0))`)
	exec(`COMMENT ON COLUMN quire_cat."Child Table".name IS 'the name'`)
	exec(`CREATE INDEX child_partial ON quire_cat."Child Table" (name DESC, lower(name)) STORING (parent_id) WHERE a > 10`)
	exec(`CREATE TABLE quire_cat.nokey (v text)`)
	exec(`CREATE VIEW quire_cat.v AS SELECT id FROM quire_cat.parent`)
	exec(`CREATE MATERIALIZED VIEW quire_cat.mv AS SELECT count(*) AS n FROM quire_cat.parent`)
	exec(`CREATE SEQUENCE quire_cat.seq START 10 INCREMENT 5`)
	exec(`CREATE FUNCTION quire_cat.f(x int) RETURNS int LANGUAGE sql AS $$ SELECT x + 1 $$`)
	exec(`CREATE FUNCTION quire_cat.tf() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`)
	exec(`CREATE TRIGGER trg BEFORE INSERT ON quire_cat.parent FOR EACH ROW EXECUTE FUNCTION quire_cat.tf()`)
	return d, db
}

func TestCatalog(t *testing.T) {
	d, db := testDB(t)
	ctx := t.Context()

	if v, err := d.Version(ctx, db); err != nil || !strings.HasPrefix(v, "v") {
		t.Errorf("version = %q, %v", v, err)
	}
	if dbName, schema, err := d.Current(ctx, db); err != nil || dbName != "defaultdb" || schema != "public" {
		t.Errorf("current = %q %q %v", dbName, schema, err)
	}
	dbs, err := d.Databases(ctx, db)
	if err != nil || !slices.Contains(dbs, "defaultdb") {
		t.Fatalf("databases = %v, %v", dbs, err)
	}
	schemas, err := d.Schemas(ctx, db, "defaultdb")
	if err != nil || schemas[0] != "public" && schemas[0] != "quire_cat" || !slices.Contains(schemas[len(schemas)-4:], "crdb_internal") {
		t.Errorf("schemas = %v, %v", schemas, err)
	}
	objs, err := d.Objects(ctx, db, "defaultdb", "quire_cat")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	var trg dialect.Object
	for _, o := range objs {
		kinds[o.Name] = o.Kind
		if o.Name == "trg" {
			trg = o
		}
	}
	for name, kind := range map[string]string{"parent": "table", "Child Table": "table", "v": "view", "mv": "matview", "seq": "sequence", "f": "function", "trg": "trigger"} {
		if kinds[name] != kind {
			t.Errorf("object %s kind = %q, want %q (%v)", name, kinds[name], kind, kinds)
		}
	}

	cols, err := d.Columns(ctx, db, "defaultdb", "quire_cat", "nokey")
	if err != nil || len(cols) != 1 || cols[0].Name != "v" {
		t.Errorf("rowid is not hidden: %+v, %v", cols, err)
	}
	cols, err = d.Columns(ctx, db, "defaultdb", "quire_cat", "Child Table")
	if err != nil || len(cols) != 5 || cols[1].PK != 1 || !cols[3].Generated || cols[2].Comment != "the name" {
		t.Fatalf("columns = %+v, %v", cols, err)
	}
	all, err := d.SchemaColumns(ctx, db, "defaultdb", "quire_cat")
	if err != nil || !slices.Equal(all["nokey"], []string{"v"}) || len(all["Child Table"]) != 5 {
		t.Errorf("schema columns = %v, %v", all, err)
	}

	ixs, err := d.Indexes(ctx, db, "defaultdb", "quire_cat", "Child Table")
	if err != nil || len(ixs) != 2 {
		t.Fatalf("indexes = %+v, %v", ixs, err)
	}
	ix := ixs[1]
	if ix.Name != "child_partial" || ix.Method != "btree" || ix.Where != "a > 10" || !slices.Equal(ix.Include, []string{"parent_id"}) ||
		len(ix.Columns) != 2 || !ix.Columns[0].Desc || ix.Columns[0].Nulls != "" || ix.Columns[1].Expression != "lower(name)" {
		t.Errorf("partial index = %+v", ix)
	}
	// The generated statement recreates the same index, invisible this time.
	ix.Invisible = true
	stmt, err := d.CreateIndex("defaultdb", "quire_cat", ix, false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{d.DropIndex("defaultdb", "quire_cat", "Child Table", ix.Name, false), stmt} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	again, _ := d.Indexes(ctx, db, "defaultdb", "quire_cat", "Child Table")
	if len(again) != 2 || !again[1].Invisible || !slices.Equal(again[1].Include, ix.Include) || again[1].Where != ix.Where {
		t.Errorf("recreated index = %+v", again)
	}

	cons, err := d.Constraints(ctx, db, "defaultdb", "quire_cat", "Child Table")
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, c := range cons {
		types = append(types, c.Type)
	}
	if strings.Join(types, ",") != "PRIMARY KEY,FOREIGN KEY,CHECK" {
		t.Errorf("constraint types = %v", types)
	}
	fks, err := d.ForeignKeys(ctx, db, "defaultdb", "quire_cat")
	if err != nil || len(fks) != 1 || fks[0].RefTable != "parent" {
		t.Errorf("foreign keys = %+v, %v", fks, err)
	}

	for _, o := range []dialect.Object{{Name: "Child Table", Kind: "table"}, {Name: "v", Kind: "view"}, {Name: "mv", Kind: "matview"}, {Name: "seq", Kind: "sequence"}, {Name: "f", Kind: "function", ID: idOf(objs, "f")}, trg} {
		ddl, err := d.DDL(ctx, db, "defaultdb", "quire_cat", o)
		if err != nil || !strings.Contains(ddl, "CREATE") {
			t.Errorf("DDL %s = %q, %v", o.Name, ddl, err)
		}
	}
	// The table DDL runs: drop and recreate the table from it.
	ddl, _ := d.DDL(ctx, db, "defaultdb", "quire_cat", dialect.Object{Name: "Child Table", Kind: "table"})
	if _, err := db.ExecContext(ctx, `DROP TABLE quire_cat."Child Table"`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("table DDL does not run: %v\n%s", err, ddl)
	}

	stats, err := d.TableStats(ctx, db, "defaultdb", "quire_cat")
	if err != nil || len(stats) != 5 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}

	up := d.Upsert(d.Qualify("", "quire_cat", "parent"), []string{"id", "code"}, []string{"id"})
	for _, code := range []string{"a", "b"} {
		if _, err := db.ExecContext(ctx, up, 1, code); err != nil {
			t.Fatalf("%s: %v", up, err)
		}
	}
}

func idOf(objs []dialect.Object, name string) string {
	for _, o := range objs {
		if o.Name == name {
			return o.ID
		}
	}
	return ""
}

func TestReadOnly(t *testing.T) {
	addr := os.Getenv("QUIRE_TEST_COCKROACH")
	if addr == "" {
		t.Skip("QUIRE_TEST_COCKROACH not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	d := &Driver{postgres.New(info())}
	db, err := d.Open(t.Context(), profile.Profile{Host: host, Port: n, User: "root", Database: "defaultdb", ReadOnly: true, TLS: profile.TLS{Mode: profile.TLSDisable}}, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE quire_ro (a int)"); err == nil {
		_, _ = db.ExecContext(context.Background(), "DROP TABLE quire_ro")
		t.Fatal("a read-only connection created a table")
	}
}

func TestVersionParsing(t *testing.T) {
	if m := versionRe.FindString("CockroachDB CCL v26.3.2 (aarch64-unknown-linux-gnu, built 2026/09/16)"); m != "v26.3.2" {
		t.Errorf("version = %q", m)
	}
	if trimParens("((lower(a)))") != "lower(a)" || trimParens("(a) + (b)") != "(a) + (b)" {
		t.Error("trimParens")
	}
}

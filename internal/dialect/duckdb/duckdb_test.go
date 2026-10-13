package duckdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	duck "github.com/duckdb/duckdb-go/v2"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/query"
)

func open(t *testing.T, p profile.Profile) *sql.DB {
	t.Helper()
	db, err := (&Driver{}).Open(t.Context(), p, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestHelpers(t *testing.T) {
	if got := parseList(`[id, '"name"', '(lower("name"))', 'concat(a, ''x,y'')']`); !slices.Equal(got, []string{"id", `"name"`, `(lower("name"))`, "concat(a, 'x,y')"}) {
		t.Errorf("parseList = %q", got)
	}
	if n, ok := quotedName(`"Na""me"`); !ok || n != `Na"me` {
		t.Errorf("quotedName = %q", n)
	}
	if _, ok := quotedName(`lower("name")`); ok {
		t.Error("an expression read as a name")
	}
	if got := interval(duck.Interval{Months: 14, Days: 3, Micros: 3723_500000}); got != "1 year 2 months 3 days 01:02:03.5" {
		t.Errorf("interval = %q", got)
	}
	if got := interval(duck.Interval{}); got != "00:00:00" {
		t.Errorf("zero interval = %q", got)
	}
	if _, err := dsn(profile.Profile{Database: filepath.Join(t.TempDir(), "missing.duckdb")}); err == nil {
		t.Error("a missing file was opened without the create option")
	}
}

func TestCatalog(t *testing.T) {
	d := &Driver{}
	dir := t.TempDir()
	path := filepath.Join(dir, "shop.duckdb")
	db := open(t, profile.Profile{Database: path, Options: map[string]string{"create": "true", "attach": "aux=" + filepath.Join(dir, "aux.duckdb")}})
	ctx := t.Context()
	for _, q := range []string{
		`CREATE SCHEMA sales`,
		`CREATE TABLE sales.parent (id INTEGER PRIMARY KEY, code VARCHAR UNIQUE)`,
		`CREATE TABLE sales.child (a INTEGER, b INTEGER, name VARCHAR NOT NULL DEFAULT 'x', parent_id INTEGER REFERENCES sales.parent(id),
			total INTEGER GENERATED ALWAYS AS (a * 2), PRIMARY KEY (b, a), CHECK (a > 0))`,
		`COMMENT ON COLUMN sales.child.name IS 'the name'`,
		`CREATE INDEX child_name ON sales.child (name, lower(name))`,
		`CREATE VIEW sales.v AS SELECT id FROM sales.parent`,
		`CREATE SEQUENCE sales.seq START 10 INCREMENT 5`,
		`CREATE MACRO sales.add1(x) AS x + 1`,
		`CREATE TABLE aux.main.t (x INTEGER)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if v, err := d.Version(ctx, db); err != nil || !strings.HasPrefix(v, "v") {
		t.Errorf("version = %q, %v", v, err)
	}
	if cur, schema, err := d.Current(ctx, db); err != nil || cur != "shop" || schema != "main" {
		t.Errorf("current = %q %q %v", cur, schema, err)
	}
	dbs, err := d.Databases(ctx, db)
	if err != nil || !slices.Equal(dbs, []string{"aux", "shop"}) {
		t.Errorf("databases = %v, %v", dbs, err)
	}
	schemas, err := d.Schemas(ctx, db, "shop")
	if err != nil || !slices.Contains(schemas, "sales") || !slices.Contains(schemas, "main") {
		t.Errorf("schemas = %v, %v", schemas, err)
	}
	objs, err := d.Objects(ctx, db, "shop", "sales")
	kinds := map[string]string{}
	byName := map[string]dialect.Object{}
	for _, o := range objs {
		kinds[o.Name], byName[o.Name] = o.Kind, o
	}
	if err != nil || kinds["parent"] != "table" || kinds["v"] != "view" || kinds["seq"] != "sequence" || kinds["add1"] != "function" || len(kinds) != 5 {
		t.Errorf("objects = %+v, %v", objs, err)
	}
	cols, err := d.Columns(ctx, db, "shop", "sales", "child")
	if err != nil || len(cols) != 5 || cols[0].PK != 2 || cols[1].PK != 1 || cols[2].Nullable || cols[2].Comment != "the name" || cols[2].Generated || !cols[4].Generated {
		t.Errorf("columns = %+v, %v", cols, err)
	}
	ixs, err := d.Indexes(ctx, db, "shop", "sales", "child")
	if err != nil || len(ixs) != 2 || !ixs[0].Primary || ixs[1].Name != "child_name" || ixs[1].Columns[0].Name != "name" || ixs[1].Columns[1].Expression != `lower("name")` {
		t.Fatalf("indexes = %+v, %v", ixs, err)
	}
	stmt, err := d.CreateIndex("shop", "sales", ixs[1], false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{d.DropIndex("shop", "sales", "child", "child_name", false), stmt} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	cons, err := d.Constraints(ctx, db, "shop", "sales", "child")
	var types []string
	for _, c := range cons {
		types = append(types, c.Type)
	}
	if err != nil || strings.Join(types, ",") != "PRIMARY KEY,FOREIGN KEY,CHECK" || !slices.Equal(cons[1].RefColumns, []string{"id"}) {
		t.Errorf("constraints = %+v, %v", cons, err)
	}
	fks, err := d.ForeignKeys(ctx, db, "shop", "sales")
	if err != nil || len(fks) != 1 || fks[0].RefTable != "parent" || !slices.Equal(fks[0].Columns, []string{"parent_id"}) {
		t.Errorf("foreign keys = %+v, %v", fks, err)
	}
	for _, o := range []dialect.Object{byName["child"], byName["v"], byName["seq"], byName["add1"]} {
		if ddl, err := d.DDL(ctx, db, "shop", "sales", o); err != nil || !strings.HasPrefix(ddl, "CREATE") {
			t.Errorf("DDL %s = %q, %v", o.Name, ddl, err)
		}
	}
	ddl, _ := d.DDL(ctx, db, "shop", "sales", byName["parent"])
	if _, err := db.ExecContext(ctx, `DROP VIEW sales.v; DROP TABLE sales.child; DROP TABLE sales.parent`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "USE shop.sales; "+ddl); err != nil {
		t.Fatalf("table DDL does not run: %v\n%s", err, ddl)
	}
	stats, err := d.TableStats(ctx, db, "shop", "sales")
	if err != nil || len(stats) != 1 || stats[0].Name != "parent" {
		t.Errorf("stats = %+v, %v", stats, err)
	}
	up := d.Upsert(d.Qualify("shop", "sales", "parent"), []string{"id", "code"}, []string{"id"})
	for _, code := range []string{"a", "b"} {
		if _, err := db.ExecContext(ctx, up, 1, code); err != nil {
			t.Fatalf("%s: %v", up, err)
		}
	}

	// A read-only connection to the same file (after this one closes) cannot write.
	_ = db.Close()
	ro := open(t, profile.Profile{Database: path, ReadOnly: true})
	if _, err := ro.ExecContext(ctx, "CREATE TABLE x (a INT)"); err == nil {
		t.Error("a read-only connection created a table")
	}
	var code string
	if err := ro.QueryRowContext(ctx, "SELECT code FROM sales.parent WHERE id = 1").Scan(&code); err != nil || code != "b" {
		t.Errorf("read-only read = %q, %v", code, err)
	}
}

func TestValues(t *testing.T) {
	d := &Driver{}
	db := open(t, profile.Profile{Database: Memory})
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `SELECT 12.50::DECIMAL(10,2), '4ad7411c-88bc-4c10-8e32-a3ede0b0a4e0'::UUID, INTERVAL 3 DAY,
		[1, 2], {'a': 1, 'b': 'z'}, MAP {'k': 1}, 170141183460469231731687303715884105727::HUGEINT, '\x01\x02'::BLOB,
		DATE '2026-10-01', NULL::INTEGER, 'y'::ENUM('x', 'y')`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols, _ := query.Columns(rows)
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	row, err := query.FullRow(rows, cols, d)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range row {
		if v == nil {
			got = append(got, "<NULL>")
		} else {
			got = append(got, *v)
		}
	}
	want := []string{"12.5", "4ad7411c-88bc-4c10-8e32-a3ede0b0a4e0", "3 days", "[1,2]", `{"a":1,"b":"z"}`, `{"k":1}`,
		"170141183460469231731687303715884105727", `\x0102`, "2026-10-01", "<NULL>", "y"}
	if !slices.Equal(got, want) {
		t.Errorf("values =\n%q\nwant\n%q", got, want)
	}
}

func TestInterrupt(t *testing.T) {
	db := open(t, profile.Profile{Database: Memory})
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(t.Context(), "CREATE TEMP TABLE kept (a INT)"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200_000_000)
	defer cancel()
	if _, err := conn.ExecContext(ctx, "SELECT count(*) FROM range(10000000000) a"); err == nil {
		t.Fatal("the query was not interrupted")
	}
	// The connection, and its temporary table, survive.
	if _, err := conn.ExecContext(t.Context(), "INSERT INTO kept VALUES (1)"); err != nil {
		t.Errorf("connection lost after the interrupt: %v", err)
	}
}

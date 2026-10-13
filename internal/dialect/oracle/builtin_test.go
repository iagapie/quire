package oracle

import (
	"context"
	"database/sql"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
)

// QUIRE_TEST_ORACLE=127.0.0.1:51521 (Oracle Free, user quire/quirepw, service FREEPDB1)
// runs these with the built-in client; the Instant Client is not needed.
func oracleProfile(t *testing.T) profile.Profile {
	t.Helper()
	addr := os.Getenv("QUIRE_TEST_ORACLE")
	if addr == "" {
		t.Skip("QUIRE_TEST_ORACLE not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	return profile.Profile{Driver: "oracle", Host: host, Port: n, User: "quire", Password: "quirepw", Database: "FREEPDB1",
		Options: map[string]string{"client": ClientBuiltin}}
}

func openOracle(t *testing.T) (*Driver, *sql.DB) {
	t.Helper()
	d := &Driver{}
	db, err := d.Open(t.Context(), oracleProfile(t), "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return d, db
}

func TestBuiltinCatalog(t *testing.T) {
	d, db := openOracle(t)
	ctx := t.Context()
	drop := []string{"DROP TRIGGER IF EXISTS trg", "DROP VIEW IF EXISTS v", `DROP TABLE IF EXISTS "Child Table"`, "DROP TABLE IF EXISTS parent",
		"DROP SEQUENCE IF EXISTS seq", "DROP FUNCTION IF EXISTS f", "DROP PROCEDURE IF EXISTS p", "DROP PACKAGE IF EXISTS pk"}
	for _, q := range drop {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		for _, q := range drop {
			_, _ = db.ExecContext(context.Background(), q)
		}
	})
	for _, q := range []string{
		`CREATE TABLE parent (id NUMBER PRIMARY KEY, code VARCHAR2(20) UNIQUE)`,
		`CREATE TABLE "Child Table" (a NUMBER, b NUMBER, name VARCHAR2(50) DEFAULT 'x' NOT NULL, total NUMBER GENERATED ALWAYS AS (a * 2) VIRTUAL,
			parent_id NUMBER REFERENCES parent(id) ON DELETE CASCADE, PRIMARY KEY (b, a), CHECK (a > 0))`,
		`COMMENT ON COLUMN "Child Table".name IS 'the name'`,
		`CREATE INDEX child_name ON "Child Table" (UPPER(name), parent_id DESC) INVISIBLE`,
		`CREATE VIEW v AS SELECT id FROM parent`,
		`CREATE SEQUENCE seq START WITH 10 INCREMENT BY 5`,
		`CREATE FUNCTION f(x NUMBER) RETURN NUMBER IS BEGIN RETURN x + 1; END;`,
		`CREATE PROCEDURE p IS BEGIN NULL; END;`,
		`CREATE PACKAGE pk AS FUNCTION g RETURN NUMBER; END;`,
		`CREATE TRIGGER trg BEFORE INSERT ON parent FOR EACH ROW BEGIN :NEW.code := UPPER(:NEW.code); END;`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if v, err := d.Version(ctx, db); err != nil || !strings.HasPrefix(v, "23.") {
		t.Errorf("version = %q, %v", v, err)
	}
	if _, schema, err := d.Current(ctx, db); err != nil || schema != "QUIRE" {
		t.Errorf("current schema = %q, %v", schema, err)
	}
	schemas, err := d.Schemas(ctx, db, "")
	if err != nil || !slices.Contains(schemas, "QUIRE") {
		t.Errorf("schemas = %v, %v", schemas, err)
	}
	objs, err := d.Objects(ctx, db, "", "QUIRE")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, o := range objs {
		kinds[o.Name] = o.Kind
	}
	own := map[string]string{"PARENT": "table", "Child Table": "table", "V": "view", "SEQ": "sequence", "F": "function", "P": "procedure", "PK": "package", "TRG": "trigger"}
	for name, kind := range own {
		if kinds[name] != kind {
			t.Errorf("object %s = %q, want %s (%v)", name, kinds[name], kind, kinds)
		}
	}
	cols, err := d.Columns(ctx, db, "", "QUIRE", "Child Table")
	if err != nil || len(cols) != 5 || cols[0].PK != 2 || cols[1].PK != 1 || cols[2].Nullable || cols[2].Comment != "the name" || !cols[3].Generated {
		t.Errorf("columns = %+v, %v", cols, err)
	}
	ixs, err := d.Indexes(ctx, db, "", "QUIRE", "Child Table")
	if err != nil {
		t.Fatal(err)
	}
	var ix dialect.Index
	for _, x := range ixs {
		if x.Name == "CHILD_NAME" {
			ix = x
		}
	}
	if !ix.Invisible || len(ix.Columns) != 2 || !strings.Contains(ix.Columns[0].Expression, "UPPER") || !ix.Columns[1].Desc {
		t.Errorf("index = %+v (all %+v)", ix, ixs)
	}
	stmt, err := d.CreateIndex("", "QUIRE", ix, false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{d.DropIndex("", "QUIRE", "Child Table", ix.Name, false), stmt} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	cons, err := d.Constraints(ctx, db, "", "QUIRE", "Child Table")
	if err != nil || len(cons) < 3 {
		t.Errorf("constraints = %+v, %v", cons, err)
	}
	fks, err := d.ForeignKeys(ctx, db, "", "QUIRE")
	if err != nil || len(fks) != 1 || fks[0].RefTable != "PARENT" {
		t.Errorf("foreign keys = %+v, %v", fks, err)
	}
	for _, o := range objs {
		if _, ours := own[o.Name]; !ours {
			continue // objects of other packages' tests, which run at the same time
		}
		if ddl, err := d.DDL(ctx, db, "", "QUIRE", o); err != nil || !strings.Contains(ddl, "CREATE") {
			t.Errorf("DDL %s %s = %q, %v", o.Kind, o.Name, ddl, err)
		}
	}
	if stats, err := d.TableStats(ctx, db, "", "QUIRE"); err != nil || len(stats) < 2 {
		t.Errorf("stats = %+v, %v", stats, err)
	}
	up := d.Upsert("PARENT", []string{"ID", "CODE"}, []string{"ID"})
	for _, code := range []string{"a", "b"} {
		if _, err := db.ExecContext(ctx, up, 1, code); err != nil {
			t.Fatalf("%s: %v", up, err)
		}
	}
	var code string
	if err := db.QueryRowContext(ctx, "SELECT code FROM parent WHERE id = 1").Scan(&code); err != nil || code != "b" { // an update: the BEFORE INSERT trigger does not run
		t.Errorf("upsert = %q, %v", code, err)
	}
}

func TestBuiltinServerOutput(t *testing.T) {
	d, db := openOracle(t)
	c, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if !isBuiltin(c) {
		t.Fatal("not a go-ora connection")
	}
	if err := d.EnableOutput(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecContext(t.Context(), `BEGIN DBMS_OUTPUT.PUT_LINE('hello'); DBMS_OUTPUT.PUT_LINE('ünicode ' || 42); END;`); err != nil {
		t.Fatal(err)
	}
	lines, err := d.ReadOutput(t.Context(), c)
	if err != nil || !slices.Equal(lines, []string{"hello", "ünicode 42"}) {
		t.Errorf("output = %q, %v", lines, err)
	}
	if lines, _ := d.ReadOutput(t.Context(), c); len(lines) != 0 {
		t.Errorf("output read twice = %q", lines)
	}
}

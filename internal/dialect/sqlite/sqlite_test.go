package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
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

func TestCatalog(t *testing.T) {
	dir := t.TempDir()
	aux := filepath.Join(dir, "aux.db")
	auxDB := open(t, profile.Profile{ID: "a", Database: aux, Options: map[string]string{"create": "true"}})
	if _, err := auxDB.ExecContext(t.Context(), "CREATE TABLE extra (x INT)"); err != nil {
		t.Fatal(err)
	}
	d := &Driver{}
	db := open(t, profile.Profile{ID: "m", Database: filepath.Join(dir, "main.db"), Options: map[string]string{"create": "true", "attach": "aux=" + aux}})
	ctx := t.Context()
	for _, q := range []string{
		`CREATE TABLE parent (id INTEGER PRIMARY KEY, code TEXT UNIQUE)`,
		`CREATE TABLE child (a INT, b INT, name TEXT NOT NULL DEFAULT 'x', total INT GENERATED ALWAYS AS (a * 2), parent_id INT REFERENCES parent(id) ON DELETE CASCADE, PRIMARY KEY (b, a))`,
		`CREATE INDEX child_expr ON child (lower(name) DESC, b) WHERE a > 10`,
		`CREATE VIEW v AS SELECT id FROM parent`,
		`CREATE TRIGGER trg AFTER INSERT ON parent BEGIN UPDATE parent SET code = 'c' WHERE id = NEW.id; END`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	dbs, err := d.Databases(ctx, db)
	if err != nil || strings.Join(dbs, ",") != "main,aux" {
		t.Fatalf("databases = %v, %v", dbs, err)
	}
	if objs, _ := d.Objects(ctx, db, "aux", ""); len(objs) != 1 || objs[0].Name != "extra" {
		t.Fatalf("aux objects = %+v", objs)
	}
	objs, err := d.Objects(ctx, db, "main", "")
	if err != nil || len(objs) != 4 {
		t.Fatalf("objects = %+v, %v", objs, err)
	}
	cols, err := d.Columns(ctx, db, "main", "", "child")
	if err != nil || len(cols) != 5 {
		t.Fatalf("columns = %+v, %v", cols, err)
	}
	if cols[0].PK != 2 || cols[1].PK != 1 || cols[2].Nullable || cols[2].Default != "'x'" || !cols[3].Generated {
		t.Errorf("columns = %+v", cols)
	}
	ixs, err := d.Indexes(ctx, db, "main", "", "child")
	if err != nil {
		t.Fatal(err)
	}
	var expr dialect.Index
	for _, ix := range ixs {
		if ix.Name == "child_expr" {
			expr = ix
		}
	}
	if expr.Where != "a > 10" || len(expr.Columns) != 2 || expr.Columns[0].Expression != "lower(name)" || !expr.Columns[0].Desc || expr.Columns[1].Name != "b" {
		t.Errorf("expression index = %+v", expr)
	}
	stmt, err := d.CreateIndex("main", "", expr, false, false)
	if err != nil || stmt != `CREATE INDEX "child_expr" ON "child" (lower(name) DESC, "b") WHERE a > 10` {
		t.Errorf("create index = %s, %v", stmt, err)
	}
	pix, _ := d.Indexes(ctx, db, "main", "", "parent")
	if len(pix) == 0 || !pix[0].Primary || pix[0].Columns[0].Name != "id" {
		t.Errorf("rowid primary key = %+v", pix)
	}
	cons, err := d.Constraints(ctx, db, "main", "", "child")
	if err != nil || len(cons) != 2 || cons[0].Type != "PRIMARY KEY" || strings.Join(cons[0].Columns, ",") != "b,a" ||
		cons[1].Type != "FOREIGN KEY" || cons[1].RefTable != "parent" || cons[1].OnDelete != "CASCADE" {
		t.Errorf("constraints = %+v", cons)
	}
	ddl, err := d.DDL(ctx, db, "main", "", dialect.Object{Name: "parent", Kind: "table"})
	if err != nil || !strings.Contains(ddl, "CREATE TABLE parent") || !strings.Contains(ddl, "CREATE TRIGGER trg") {
		t.Errorf("ddl = %s, %v", ddl, err)
	}
	stats, err := d.TableStats(ctx, db, "main", "")
	if err != nil || len(stats) != 3 {
		t.Errorf("stats = %+v, %v", stats, err)
	}
	up := d.Upsert(`"parent"`, []string{"id", "code"}, []string{"id"})
	for _, code := range []string{"a", "b"} {
		if _, err := db.ExecContext(ctx, up, 7, code); err != nil {
			t.Fatalf("%s: %v", up, err)
		}
	}
	if cur, _, _ := d.Current(ctx, db); cur != "main" {
		t.Error("current database")
	}
	if d.Qualify("main", "", "t") != `"t"` || d.Qualify("aux", "", "t") != `"aux"."t"` {
		t.Error("qualify")
	}
}

func TestOpenModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.db")
	if _, err := (&Driver{}).Open(t.Context(), profile.Profile{Database: path}, "", dialect.Network{}); err == nil {
		t.Error("a missing file must not be created without the create option")
	}
	rw := open(t, profile.Profile{ID: "rw", Database: path, Options: map[string]string{"create": "true"}})
	if _, err := rw.ExecContext(t.Context(), "CREATE TABLE t (a INT)"); err != nil {
		t.Fatal(err)
	}
	ro := open(t, profile.Profile{ID: "ro", Database: path, ReadOnly: true})
	if _, err := ro.ExecContext(t.Context(), "INSERT INTO t VALUES (1)"); err == nil {
		t.Error("read-only database accepted a write")
	}
	// An in-memory database is shared by the pool's connections.
	mem := open(t, profile.Profile{ID: "mem", Database: ":memory:"})
	c1, _ := mem.Conn(t.Context())
	c2, _ := mem.Conn(t.Context())
	defer func() { _ = c1.Close(); _ = c2.Close() }()
	if _, err := c1.ExecContext(t.Context(), "CREATE TABLE shared (a INT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := c2.ExecContext(t.Context(), "INSERT INTO shared VALUES (1)"); err != nil {
		t.Fatalf("second connection does not see the memory table: %v", err)
	}
}

func TestKeyExpressions(t *testing.T) {
	got := keyExpressions(`CREATE INDEX i ON t (lower(a) DESC, "b c" COLLATE NOCASE, substr(x, 1, 2))`)
	if strings.Join(got, "|") != `lower(a)|"b c"|substr(x, 1, 2)` {
		t.Errorf("keys = %q", got)
	}
}

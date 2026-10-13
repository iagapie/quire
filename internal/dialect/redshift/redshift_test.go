package redshift

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
)

func TestVersion(t *testing.T) {
	v := "PostgreSQL 8.0.2 on i686-pc-linux-gnu, compiled by GCC gcc (GCC) 3.4.2 20041017 (Red Hat 3.4.2-6.fc3), Redshift 1.0.77467"
	if m := versionRe.FindString(v); m != "Redshift 1.0.77467" {
		t.Errorf("version = %q", m)
	}
	if !slices.Equal(numbers("2,1, 3"), []int{2, 1, 3}) || len(numbers("")) != 0 {
		t.Error("numbers")
	}
	if _, ok := dialect.CopierOf(newDriver()); ok {
		t.Error("Redshift has no COPY FROM STDIN")
	}
}

// There is no Redshift to test against: the catalog sticks to what PostgreSQL 8.0
// had, so it runs on the PostgreSQL test server (QUIRE_TEST_POSTGRES) too. Redshift's
// own parts (SHOW TABLE, SVV_TABLE_INFO) fall back there.
func TestCatalogOnPostgres(t *testing.T) {
	addr := os.Getenv("QUIRE_TEST_POSTGRES")
	if addr == "" {
		t.Skip("QUIRE_TEST_POSTGRES not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	d := newDriver()
	db, err := d.Open(t.Context(), profile.Profile{Host: host, Port: n, User: "quire", Password: "quirepw", Database: "shop", TLS: profile.TLS{Mode: profile.TLSDisable}}, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := t.Context()
	for _, q := range []string{
		`DROP SCHEMA IF EXISTS quire_rs CASCADE`, `CREATE SCHEMA quire_rs`,
		`CREATE TABLE quire_rs.parent (id int PRIMARY KEY, code varchar(10))`,
		`CREATE TABLE quire_rs.child (a int, b int, name varchar(20) NOT NULL DEFAULT 'x', parent_id int REFERENCES quire_rs.parent(id), PRIMARY KEY (b, a))`,
		`COMMENT ON COLUMN quire_rs.child.name IS 'the name'`,
		`CREATE VIEW quire_rs.v AS SELECT id FROM quire_rs.parent`,
		`CREATE FUNCTION quire_rs.f(int) RETURNS int IMMUTABLE AS $$ SELECT $1 + 1 $$ LANGUAGE sql`,
		`CREATE PROCEDURE quire_rs.p(int) AS $$ BEGIN END $$ LANGUAGE plpgsql`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS quire_rs CASCADE`) })

	if s, err := d.Schemas(ctx, db, ""); err != nil || !slices.Contains(s, "quire_rs") || slices.Contains(s, "pg_toast") {
		t.Errorf("schemas = %v, %v", s, err)
	}
	if dbs, err := d.Databases(ctx, db); err != nil || !slices.Contains(dbs, "shop") || slices.Contains(dbs, "template1") {
		t.Errorf("databases = %v, %v", dbs, err)
	}
	objs, err := d.Objects(ctx, db, "", "quire_rs")
	kinds := map[string]string{}
	byName := map[string]dialect.Object{}
	for _, o := range objs {
		kinds[o.Name], byName[o.Name] = o.Kind, o
	}
	if err != nil || kinds["parent"] != "table" || kinds["v"] != "view" || kinds["f"] != "function" || kinds["p"] != "procedure" || byName["f"].Detail != "integer" {
		t.Fatalf("objects = %+v, %v", objs, err)
	}
	cols, err := d.Columns(ctx, db, "", "quire_rs", "child")
	if err != nil || len(cols) != 4 || cols[0].PK != 2 || cols[1].PK != 1 || cols[2].Nullable || cols[2].Comment != "the name" || cols[2].Default == "" {
		t.Errorf("columns = %+v, %v", cols, err)
	}
	cons, err := d.Constraints(ctx, db, "", "quire_rs", "child")
	if err != nil || len(cons) != 2 || !slices.Equal(cons[0].Columns, []string{"b", "a"}) || cons[1].RefTable != "parent" || !slices.Equal(cons[1].RefColumns, []string{"id"}) {
		t.Errorf("constraints = %+v, %v", cons, err)
	}
	fks, err := d.ForeignKeys(ctx, db, "", "quire_rs")
	if err != nil || len(fks) != 1 || !slices.Equal(fks[0].Columns, []string{"parent_id"}) || !slices.Equal(fks[0].RefColumns, []string{"id"}) {
		t.Errorf("foreign keys = %+v, %v", fks, err)
	}
	all, err := d.SchemaColumns(ctx, db, "", "quire_rs")
	if err != nil || len(all["child"]) != 4 || len(all["v"]) != 1 {
		t.Errorf("schema columns = %v, %v", all, err)
	}
	stats, err := d.TableStats(ctx, db, "", "quire_rs")
	if err != nil || len(stats) != 3 {
		t.Errorf("stats = %+v, %v", stats, err)
	}
	for _, o := range []dialect.Object{byName["child"], byName["v"], byName["f"], byName["p"]} {
		ddl, err := d.DDL(ctx, db, "", "quire_rs", o)
		if err != nil || !strings.HasPrefix(ddl, "CREATE") {
			t.Errorf("DDL %s = %q, %v", o.Name, ddl, err)
		}
	}
	ddl, _ := d.DDL(ctx, db, "", "quire_rs", byName["child"])
	for _, q := range []string{`DROP TABLE quire_rs.child`, ddl} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if v, err := d.Version(ctx, db); err != nil || v == "" {
		t.Errorf("version = %q, %v", v, err)
	}
}

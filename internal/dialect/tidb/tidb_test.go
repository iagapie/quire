package tidb

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/iagapie/quire/internal/dialect"
	mysqldialect "github.com/iagapie/quire/internal/dialect/mysql"
	"github.com/iagapie/quire/internal/profile"
)

func TestVersion(t *testing.T) {
	if m := versionRe.FindStringSubmatch("8.0.11-TiDB-v8.5.4"); m == nil || m[1] != "v8.5.4" {
		t.Errorf("version = %v", m)
	}
}

// QUIRE_TEST_TIDB=127.0.0.1:54000 go test ./internal/dialect/tidb (user root, no password).
func TestCatalog(t *testing.T) {
	addr := os.Getenv("QUIRE_TEST_TIDB")
	if addr == "" {
		t.Skip("QUIRE_TEST_TIDB not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	d := &Driver{mysqldialect.New(info(), mysqldialect.ClientReadOnly())}
	p := profile.Profile{Host: host, Port: n, User: "root", Database: "test", TLS: profile.TLS{Mode: profile.TLSPrefer}}
	db, err := d.Open(t.Context(), p, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := t.Context()
	exec := func(q string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec("DROP DATABASE IF EXISTS quire_cat")
	exec("CREATE DATABASE quire_cat")
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP DATABASE IF EXISTS quire_cat") })
	exec("CREATE TABLE quire_cat.parent (id INT PRIMARY KEY, code VARCHAR(20) UNIQUE) COMMENT 'parents'")
	exec("CREATE TABLE quire_cat.child (a INT, b INT, name VARCHAR(50) NOT NULL, parent_id INT, PRIMARY KEY (b, a), FOREIGN KEY (parent_id) REFERENCES quire_cat.parent (id))")
	exec("CREATE INDEX child_name ON quire_cat.child (name(10), parent_id) INVISIBLE")
	exec("CREATE INDEX child_lower ON quire_cat.child ((LOWER(name)))")
	exec("CREATE VIEW quire_cat.v AS SELECT id FROM quire_cat.parent")
	exec("CREATE SEQUENCE quire_cat.seq START 10 INCREMENT 5")

	if v, err := d.Version(ctx, db); err != nil || !strings.HasPrefix(v, "v") {
		t.Errorf("version = %q, %v", v, err)
	}
	if dbs, err := d.Databases(ctx, db); err != nil || strings.EqualFold(dbs[0], "METRICS_SCHEMA") || strings.EqualFold(dbs[0], "INFORMATION_SCHEMA") {
		t.Errorf("databases = %v, %v", dbs, err)
	}
	objs, err := d.Objects(ctx, db, "quire_cat", "")
	kinds := map[string]string{}
	for _, o := range objs {
		kinds[o.Name] = o.Kind
	}
	if err != nil || kinds["parent"] != "table" || kinds["v"] != "view" || kinds["seq"] != "sequence" || len(kinds) != 4 {
		t.Errorf("objects = %v, %v", kinds, err)
	}
	cols, err := d.Columns(ctx, db, "quire_cat", "", "child")
	if err != nil || len(cols) != 4 || cols[0].PK != 2 || cols[1].PK != 1 {
		t.Errorf("columns = %+v, %v", cols, err)
	}
	ixs, err := d.Indexes(ctx, db, "quire_cat", "", "child")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]dialect.Index{}
	for _, ix := range ixs {
		byName[ix.Name] = ix
	}
	if ix := byName["child_name"]; !ix.Invisible || ix.Columns[0].Length != 10 {
		t.Errorf("child_name = %+v", ix)
	}
	if ix := byName["child_lower"]; len(ix.Columns) != 1 || !strings.Contains(strings.ToLower(ix.Columns[0].Expression), "lower") {
		t.Errorf("child_lower = %+v", ix)
	}
	for _, ix := range []dialect.Index{byName["child_name"], byName["child_lower"]} {
		stmt, err := d.CreateIndex("quire_cat", "", ix, false, false)
		if err != nil {
			t.Fatal(err)
		}
		exec(d.DropIndex("quire_cat", "", "child", ix.Name, false))
		exec(stmt)
	}
	fks, err := d.ForeignKeys(ctx, db, "quire_cat", "")
	if err != nil || len(fks) != 1 || fks[0].RefTable != "parent" {
		t.Errorf("foreign keys = %+v, %v", fks, err)
	}
	for _, o := range []dialect.Object{{Name: "child", Kind: "table"}, {Name: "v", Kind: "view"}, {Name: "seq", Kind: "sequence"}} {
		if ddl, err := d.DDL(ctx, db, "quire_cat", "", o); err != nil || !strings.Contains(ddl, "CREATE") {
			t.Errorf("ddl %s = %q, %v", o.Name, ddl, err)
		}
	}
	if stats, err := d.TableStats(ctx, db, "quire_cat", ""); err != nil || len(stats) < 3 {
		t.Errorf("stats = %+v, %v", stats, err)
	}
	up := d.Upsert("quire_cat.parent", []string{"id", "code"}, []string{"id"})
	for _, code := range []string{"a", "b"} {
		if _, err := db.ExecContext(ctx, up, 1, code); err != nil {
			t.Fatalf("%s: %v", up, err)
		}
	}
	// A read-only profile still connects (the server has no read-only session).
	p.ReadOnly = true
	ro, err := d.Open(ctx, p, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	if err := ro.PingContext(ctx); err != nil {
		t.Errorf("read-only connection: %v", err)
	}
}

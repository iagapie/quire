package yugabyte

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/dialect/postgres"
	"github.com/iagapie/quire/internal/profile"
)

func TestVersion(t *testing.T) {
	if m := versionRe.FindStringSubmatch("15.12-YB-2026.1.2.0-b0"); m == nil || m[1] != "15.12" || m[2] != "2026.1.2.0-b0" {
		t.Errorf("version = %v", m)
	}
}

// QUIRE_TEST_YUGABYTE=127.0.0.1:55433 go test ./internal/dialect/yugabyte
func TestCatalog(t *testing.T) {
	addr := os.Getenv("QUIRE_TEST_YUGABYTE")
	if addr == "" {
		t.Skip("QUIRE_TEST_YUGABYTE not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	d := &Driver{postgres.New(info())}
	pool, err := d.Open(t.Context(), profile.Profile{Host: host, Port: n, User: "yugabyte", Database: "yugabyte", TLS: profile.TLS{Mode: profile.TLSDisable}}, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pool.Close() }()
	ctx := t.Context()
	// One connection: with DDL of other tests running, a new catalog object can take a
	// moment to show on the other connections of the pool.
	db, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, q := range []string{
		`DROP SCHEMA IF EXISTS quire_cat CASCADE`, `CREATE SCHEMA quire_cat`,
		`CREATE TABLE quire_cat.parent (id int PRIMARY KEY, code text UNIQUE)`,
		`CREATE TABLE quire_cat.child (a int, b int, name text, parent_id int REFERENCES quire_cat.parent(id), PRIMARY KEY (b HASH, a ASC))`,
		`CREATE INDEX child_partial ON quire_cat.child (name DESC NULLS LAST, lower(name)) INCLUDE (parent_id) WHERE a > 10`,
		`CREATE MATERIALIZED VIEW quire_cat.mv AS SELECT count(*) AS n FROM quire_cat.parent`,
	} {
		// DDL from tests of other packages running at the same time can conflict
		// (40001): the server asks to retry.
		// YugabyteDB also retries such DDL itself, so a statement may report "already
		// exists" for what it has just created: the schema is new, so that means done.
		var err error
		for range 5 {
			_, err = db.ExecContext(ctx, q)
			if err != nil && strings.Contains(err.Error(), "already exists") {
				err = nil
			}
			if err == nil || !d.Transient(err) {
				break
			}
			time.Sleep(time.Second)
		}
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS quire_cat CASCADE`) })

	if v, err := d.Version(ctx, db); err != nil || !strings.Contains(v, "(PostgreSQL ") {
		t.Errorf("version = %q, %v", v, err)
	}
	objs, err := d.Objects(ctx, db, "", "quire_cat")
	if err != nil || len(objs) != 3 {
		t.Errorf("objects = %+v, %v", objs, err)
	}
	ixs, err := d.Indexes(ctx, db, "", "quire_cat", "child")
	if err != nil || len(ixs) != 2 || ixs[1].Method != "lsm" || ixs[1].Where != "a > 10" || !slices.Equal(ixs[1].Include, []string{"parent_id"}) {
		t.Fatalf("indexes = %+v, %v", ixs, err)
	}
	stmt, err := d.CreateIndex("", "quire_cat", ixs[1], false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, d.DropIndex("", "quire_cat", "child", ixs[1].Name, false)); err != nil {
		t.Fatal(err)
	}
	// With DDL of other tests running, the drop can take a moment to show.
	for range 20 {
		if now, _ := d.Indexes(ctx, db, "", "quire_cat", "child"); len(now) == 1 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		t.Fatalf("%s: %v", stmt, err)
	}
	again, _ := d.Indexes(ctx, db, "", "quire_cat", "child")
	if len(again) != 2 || again[1].Definition != ixs[1].Definition {
		t.Errorf("recreated index differs:\n%s\n%s", again[1].Definition, ixs[1].Definition)
	}
	ddl, err := d.DDL(ctx, db, "", "quire_cat", dialect.Object{Name: "child", Kind: "table"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE quire_cat.child`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("table DDL does not run: %v\n%s", err, ddl)
	}
}

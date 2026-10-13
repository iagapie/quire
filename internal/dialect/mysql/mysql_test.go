package mysql

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

// servers are the test servers: QUIRE_TEST_MYSQL=127.0.0.1:53306 and
// QUIRE_TEST_MARIADB=127.0.0.1:53307 (see scripts/test-databases.sh).
func servers(t *testing.T) map[string]*Driver {
	t.Helper()
	out := map[string]*Driver{}
	if os.Getenv("QUIRE_TEST_MYSQL") != "" {
		out["QUIRE_TEST_MYSQL"] = &Driver{info: info()}
	}
	if os.Getenv("QUIRE_TEST_MARIADB") != "" {
		out["QUIRE_TEST_MARIADB"] = &Driver{info: mariaInfo(), maria: true}
	}
	if len(out) == 0 {
		t.Skip("QUIRE_TEST_MYSQL and QUIRE_TEST_MARIADB not set")
	}
	return out
}

func testProfile(env string) profile.Profile {
	host, port, _ := strings.Cut(os.Getenv(env), ":")
	n, _ := strconv.Atoi(port)
	return profile.Profile{Driver: "mysql", Host: host, Port: n, User: "quire", Password: "quirepw", Database: "shop", TLS: profile.TLS{Mode: profile.TLSDisable}}
}

func open(t *testing.T, d *Driver, p profile.Profile) *sql.DB {
	t.Helper()
	db, err := d.Open(t.Context(), p, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestCatalog(t *testing.T) {
	for env, d := range servers(t) {
		t.Run(d.info.Label, func(t *testing.T) {
			db := open(t, d, testProfile(env))
			ctx := t.Context()
			exec := func(q string) {
				t.Helper()
				if _, err := db.ExecContext(ctx, q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			for _, q := range []string{"DROP TRIGGER IF EXISTS trg", "DROP VIEW IF EXISTS v", "DROP TABLE IF EXISTS `child table`", "DROP TABLE IF EXISTS parent",
				"DROP FUNCTION IF EXISTS f", "DROP PROCEDURE IF EXISTS p"} {
				exec(q)
			}
			t.Cleanup(func() {
				for _, q := range []string{"DROP VIEW IF EXISTS v", "DROP TABLE IF EXISTS `child table`", "DROP TABLE IF EXISTS parent", "DROP FUNCTION IF EXISTS f", "DROP PROCEDURE IF EXISTS p"} {
					_, _ = db.ExecContext(context.Background(), q)
				}
			})
			exec("CREATE TABLE parent (id INT PRIMARY KEY, code VARCHAR(20) UNIQUE, flags BIT(4)) COMMENT 'parents'")
			exec("CREATE TABLE `child table` (a INT, b INT, name VARCHAR(50) NOT NULL DEFAULT 'x' COMMENT 'the name', total INT GENERATED ALWAYS AS (a * 2) VIRTUAL, " +
				"parent_id INT, PRIMARY KEY (b, a), CONSTRAINT fk_parent FOREIGN KEY (parent_id) REFERENCES parent (id) ON DELETE CASCADE, CONSTRAINT positive CHECK (a > 0))")
			exec("CREATE INDEX child_name ON `child table` (name(10) DESC, parent_id)")
			exec("CREATE VIEW v AS SELECT id FROM parent")
			exec("CREATE FUNCTION f(x INT) RETURNS INT DETERMINISTIC RETURN x + 1")
			exec("CREATE PROCEDURE p() BEGIN SELECT 1; END")
			exec("CREATE TRIGGER trg BEFORE INSERT ON parent FOR EACH ROW SET NEW.code = UPPER(NEW.code)")

			dbs, err := d.Databases(ctx, db)
			if err != nil || dbs[0] != "shop" || !slices.Contains(dbs, "information_schema") {
				t.Errorf("databases = %v, %v", dbs, err)
			}
			objs, err := d.Objects(ctx, db, "shop", "")
			if err != nil {
				t.Fatal(err)
			}
			kinds := map[string]string{}
			for _, o := range objs {
				kinds[o.Name] = o.Kind
				if o.Name == "trg" && o.Table != "parent" {
					t.Errorf("trigger table = %q", o.Table)
				}
				if o.Name == "parent" && o.Comment != "parents" {
					t.Errorf("table comment = %q", o.Comment)
				}
			}
			want := map[string]string{"parent": "table", "child table": "table", "v": "view", "f": "function", "p": "procedure", "trg": "trigger"}
			for name, kind := range want {
				if kinds[name] != kind {
					t.Errorf("object %s = %q, want %s (all: %v)", name, kinds[name], kind, kinds)
				}
			}
			cols, err := d.Columns(ctx, db, "shop", "", "child table")
			if err != nil || len(cols) != 5 {
				t.Fatalf("columns = %+v, %v", cols, err)
			}
			if cols[0].PK != 2 || cols[1].PK != 1 || cols[2].Nullable || cols[2].Comment != "the name" || !cols[3].Generated || cols[4].Generated {
				t.Errorf("columns = %+v", cols)
			}
			ixs, err := d.Indexes(ctx, db, "shop", "", "child table")
			if err != nil {
				t.Fatal(err)
			}
			byName := map[string]dialect.Index{}
			for _, ix := range ixs {
				byName[ix.Name] = ix
			}
			if pk := byName["PRIMARY"]; !pk.Primary || len(pk.Columns) != 2 || pk.Columns[0].Name != "b" {
				t.Errorf("primary = %+v", pk)
			}
			ix := byName["child_name"]
			if ix.Columns[0].Length != 10 || ix.Columns[1].Name != "parent_id" || ix.Definition != "CREATE INDEX `child_name` ON `shop`.`child table` (`name`(10)"+descIfSupported(ix)+", `parent_id`)" {
				t.Errorf("index = %+v", ix)
			}
			cons, err := d.Constraints(ctx, db, "shop", "", "child table")
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]dialect.Constraint{}
			for _, c := range cons {
				got[c.Type] = c
			}
			if fk := got["FOREIGN KEY"]; fk.RefTable != "parent" || fk.OnDelete != "CASCADE" || fk.Definition != "FOREIGN KEY (`parent_id`) REFERENCES `parent` (`id`) ON DELETE CASCADE" {
				t.Errorf("foreign key = %+v", fk)
			}
			if ck := got["CHECK"]; !strings.Contains(ck.Definition, "> 0") {
				t.Errorf("check = %+v", ck)
			}
			for _, o := range []dialect.Object{{Name: "child table", Kind: "table"}, {Name: "v", Kind: "view"}, {Name: "f", Kind: "function"}, {Name: "trg", Kind: "trigger"}} {
				ddl, err := d.DDL(ctx, db, "shop", "", o)
				if err != nil || !strings.Contains(ddl, "CREATE") {
					t.Errorf("ddl %s = %q, %v", o.Name, ddl, err)
				}
			}
			stats, err := d.TableStats(ctx, db, "shop", "")
			// Other packages' tests may have tables in shop too.
			seen := map[string]string{}
			for _, st := range stats {
				seen[st.Name] = st.Kind
			}
			if err != nil || seen["parent"] != "table" || seen["child table"] != "table" || seen["v"] != "view" {
				t.Errorf("stats = %+v, %v", stats, err)
			}
			up := d.Upsert("`parent`", []string{"id", "code"}, []string{"id"})
			for _, code := range []string{"a", "b"} {
				if _, err := db.ExecContext(ctx, up, 7, code); err != nil {
					t.Fatalf("%s: %v", up, err)
				}
			}
			var code string
			if err := db.QueryRowContext(ctx, "SELECT code FROM parent WHERE id = 7").Scan(&code); err != nil || code != "B" { // the trigger upper-cases it
				t.Errorf("upsert left %q, %v", code, err)
			}
			if cur, _, err := d.Current(ctx, db); err != nil || cur != "shop" {
				t.Errorf("current = %q, %v", cur, err)
			}
			if v, err := d.Version(ctx, db); err != nil || v == "" {
				t.Errorf("version = %q, %v", v, err)
			}
			// Index DDL round trip.
			stmt, err := d.CreateIndex("shop", "", dialect.Index{Name: "by_code", Table: "parent", Unique: true, Columns: []dialect.IndexColumn{{Name: "code"}}}, false, true)
			if err != nil {
				t.Fatal(err)
			}
			exec(stmt)
			exec(d.DropIndex("shop", "", "parent", "by_code", false))
		})
	}
}

// descIfSupported is " DESC" when the server keeps descending index keys (MySQL 8,
// MariaDB 10.8+).
func descIfSupported(ix dialect.Index) string {
	if ix.Columns[0].Desc {
		return " DESC"
	}
	return ""
}

func TestReadOnly(t *testing.T) {
	for env, d := range servers(t) {
		t.Run(d.info.Label, func(t *testing.T) {
			rw := open(t, d, testProfile(env))
			if _, err := rw.ExecContext(t.Context(), "CREATE TABLE IF NOT EXISTS ro_t (a INT)"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = rw.ExecContext(context.Background(), "DROP TABLE IF EXISTS ro_t") })
			p := testProfile(env)
			p.ReadOnly = true
			ro := open(t, d, p)
			if _, err := ro.ExecContext(t.Context(), "INSERT INTO ro_t VALUES (1)"); err == nil || !strings.Contains(err.Error(), "READ ONLY") {
				t.Errorf("read-only session accepted a write: %v", err)
			}
			var n int
			if err := ro.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM ro_t").Scan(&n); err != nil {
				t.Errorf("read-only session cannot read: %v", err)
			}
		})
	}
}

func TestSyntax(t *testing.T) {
	d := &Driver{info: info()}
	if got := fromURL("mysql://u:p%40ss@db.example:3307/shop?tls=true"); got != "u:p@ss@tcp(db.example:3307)/shop?tls=true" {
		t.Errorf("url = %s", got)
	}
	if got := fromURL("mysql://u@db/shop"); got != "u@tcp(db:3306)/shop" {
		t.Errorf("url without port = %s", got)
	}
	cfg, err := d.config(profile.Profile{Host: "h", User: "u", Password: "p", Database: "d", Options: map[string]string{"params": "charset=latin1&sql_mode=ANSI"}}, "", dialect.Network{})
	if err != nil || cfg.Addr != "h:3306" || cfg.Params["sql_mode"] != "ANSI" || !cfg.AllowFallbackToPlaintext {
		t.Errorf("config = %+v, %v", cfg, err)
	}
	if v, ok := d.FormatValue("BIT", []byte{1, 2}); !ok || v != "258" {
		t.Errorf("bit = %s", v)
	}
	if d.Quote("a`b") != "`a``b`" || d.Qualify("db", "", "t") != "`db`.`t`" {
		t.Error("quoting")
	}
	if got := d.Upsert("`t`", []string{"id"}, []string{"id"}); got != "INSERT INTO `t` (`id`) VALUES (?) ON DUPLICATE KEY UPDATE `id` = `id`" {
		t.Errorf("upsert of keys only = %s", got)
	}
}

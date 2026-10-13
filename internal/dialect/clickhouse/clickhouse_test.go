package clickhouse

import (
	"context"
	"crypto/tls"
	"database/sql"
	"net"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/query"
)

func TestHelpers(t *testing.T) {
	if got := splitTop("a, toDate(b, 'x,y'), `c d`"); !slices.Equal(got, []string{"a", "toDate(b, 'x,y')", "`c d`"}) {
		t.Errorf("splitTop = %q", got)
	}
	s, err := parseSettings("max_threads=4, max_execution_time=60")
	if err != nil || s["max_threads"] != "4" || s["max_execution_time"] != "60" {
		t.Errorf("settings = %v, %v", s, err)
	}
	if _, err := parseSettings("oops"); err == nil {
		t.Error("a setting without a value was accepted")
	}
	d := &Driver{}
	if lit, _ := d.Literal("text", `a'b\c`); lit != `'a\'b\\c'` {
		t.Errorf("literal = %s", lit)
	}
	if lit, _ := d.Literal(dialect.ValueBinary, `\x0102`); lit != "unhex('0102')" {
		t.Errorf("binary literal = %s", lit)
	}
	m := checks("CREATE TABLE db.t (`a` Int32, CONSTRAINT positive CHECK a > 0, CONSTRAINT `two words` CHECK a < 10) ENGINE = MergeTree ORDER BY a")
	if len(m) != 2 || m[0].Definition != "CHECK a > 0" || m[1].Name != "two words" || m[1].Definition != "CHECK a < 10" {
		t.Errorf("constraints = %+v", m)
	}
}

// QUIRE_TEST_CLICKHOUSE=127.0.0.1:59000 (native protocol; user quire, password quirepw).
func open(t *testing.T, p profile.Profile) (*Driver, *sql.DB) {
	t.Helper()
	addr := os.Getenv("QUIRE_TEST_CLICKHOUSE")
	if addr == "" {
		t.Skip("QUIRE_TEST_CLICKHOUSE not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	p.Host = host
	p.Port, _ = strconv.Atoi(port)
	p.User, p.Password = "quire", "quirepw"
	d := &Driver{}
	db, err := d.Open(t.Context(), p, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return d, db
}

func TestCatalog(t *testing.T) {
	d, db := open(t, profile.Profile{Database: "default", Options: map[string]string{"compress": "true"}})
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
	exec("CREATE TABLE quire_cat.events (id UInt64, day Date, name LowCardinality(String) COMMENT 'the name', note Nullable(String), " +
		"tags Array(String), attrs Map(String, UInt8), price Decimal(10, 2), doubled UInt64 MATERIALIZED id * 2, " +
		"INDEX by_name name TYPE bloom_filter GRANULARITY 4, CONSTRAINT positive CHECK id > 0) ENGINE = MergeTree ORDER BY (day, id)")
	exec("CREATE VIEW quire_cat.v AS SELECT id FROM quire_cat.events")
	exec("CREATE MATERIALIZED VIEW quire_cat.mv ENGINE = SummingMergeTree ORDER BY day AS SELECT day, count() AS n FROM quire_cat.events GROUP BY day")
	if _, err := db.ExecContext(ctx, "INSERT INTO quire_cat.events (id, day, name, note, tags, attrs, price) VALUES (?, ?, ?, ?, ?, ?, ?)",
		1, "2026-10-01", "a'b", nil, []string{"x", "y"}, map[string]uint8{"k": 1}, "12.50"); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO quire_cat.events (id, day, name, note, tags, attrs, price) VALUES (2, '2026-10-02', 'b', 'n', [], {}, 1)`)

	if v, err := d.Version(ctx, db); err != nil || v == "" {
		t.Errorf("version = %q, %v", v, err)
	}
	dbs, err := d.Databases(ctx, db)
	if err != nil || !slices.Contains(dbs, "quire_cat") || dbs[len(dbs)-1] == "quire_cat" {
		t.Errorf("databases = %v, %v", dbs, err)
	}
	objs, err := d.Objects(ctx, db, "quire_cat", "")
	kinds := map[string]string{}
	for _, o := range objs {
		kinds[o.Name] = o.Kind
	}
	if err != nil || len(kinds) != 3 || kinds["events"] != "table" || kinds["v"] != "view" || kinds["mv"] != "matview" {
		t.Errorf("objects = %+v, %v", objs, err)
	}
	cols, err := d.Columns(ctx, db, "quire_cat", "", "events")
	if err != nil || len(cols) != 8 || cols[2].Comment != "the name" || !cols[3].Nullable || cols[2].Nullable || !cols[7].Generated || cols[0].PK != 0 {
		t.Errorf("columns = %+v, %v", cols, err)
	}
	ixs, err := d.Indexes(ctx, db, "quire_cat", "", "events")
	if err != nil || len(ixs) != 2 || !ixs[0].Primary || len(ixs[0].Columns) != 2 || ixs[0].Columns[0].Name != "day" || ixs[1].Name != "by_name" || ixs[1].Method != "bloom_filter" {
		t.Errorf("indexes = %+v, %v", ixs, err)
	}
	cons, err := d.Constraints(ctx, db, "quire_cat", "", "events")
	if err != nil || len(cons) != 1 || cons[0].Name != "positive" {
		t.Errorf("constraints = %+v, %v", cons, err)
	}
	if ddl, err := d.DDL(ctx, db, "quire_cat", "", dialect.Object{Name: "events", Kind: "table"}); err != nil || !strings.Contains(ddl, "MergeTree") {
		t.Errorf("ddl = %q, %v", ddl, err)
	}
	stats, err := d.TableStats(ctx, db, "quire_cat", "")
	if err != nil || len(stats) != 3 || stats[0].Name != "events" || stats[0].Rows != 2 {
		t.Errorf("stats = %+v, %v", stats, err)
	}
	all, err := d.SchemaColumns(ctx, db, "quire_cat", "")
	if err != nil || len(all["events"]) != 8 {
		t.Errorf("schema columns = %v, %v", all, err)
	}

	// Values render as text: NULL, arrays and maps as JSON, decimals and dates.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	for _, q := range d.Use("quire_cat", "") {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := conn.QueryContext(ctx, "SELECT id, day, name, note, tags, attrs, price FROM events ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	qcols, _ := query.Columns(rows)
	var got []string
	for rows.Next() {
		row, err := query.FullRow(rows, qcols, d)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range row {
			if v == nil {
				got = append(got, "<NULL>")
			} else {
				got = append(got, *v)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"1", "2026-10-01", "a'b", "<NULL>", `["x","y"]`, `{"k":1}`, "12.5", "2", "2026-10-02", "b", "n", "[]", "{}", "1"}
	if !slices.Equal(got, want) {
		t.Errorf("values =\n%q\nwant\n%q", got, want)
	}
	if qcols[4].Kind != query.KindJSON || qcols[2].Kind != query.KindText || qcols[6].Kind != query.KindNumber {
		t.Errorf("kinds = %+v", qcols)
	}
}

func TestReadOnly(t *testing.T) {
	_, db := open(t, profile.Profile{Database: "default", ReadOnly: true})
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE quire_ro (a Int32) ENGINE = Memory"); err == nil {
		_, _ = db.ExecContext(context.Background(), "DROP TABLE quire_ro")
		t.Fatal("a read-only connection created a table")
	}
	var n uint8
	if err := db.QueryRowContext(t.Context(), "SELECT 1").Scan(&n); err != nil || n != 1 {
		t.Errorf("read-only connection cannot read: %v", err)
	}
}

func TestHTTP(t *testing.T) {
	addr := os.Getenv("QUIRE_TEST_CLICKHOUSE_HTTP")
	if addr == "" {
		t.Skip("QUIRE_TEST_CLICKHOUSE_HTTP not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	d := &Driver{}
	db, err := d.Open(t.Context(), profile.Profile{Host: host, Port: n, User: "quire", Password: "quirepw", Options: map[string]string{"protocol": "http"}}, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if v, err := d.Version(t.Context(), db); err != nil || v == "" {
		t.Errorf("version over HTTP = %q, %v", v, err)
	}
}

// The native protocol gets TLS from our dialer (the client skips it with a custom
// dialer); "prefer" stays plain, since ClickHouse serves TLS on other ports.
func TestNativeDialerUsesTLS(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	host, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	n, _ := strconv.Atoi(port)
	d := &Driver{}
	for mode, wantTLS := range map[string]bool{profile.TLSRequire: true, profile.TLSPrefer: false, profile.TLSDisable: false} {
		opt, err := d.options(profile.Profile{Host: host, Port: n, TLS: profile.TLS{Mode: mode}}, "", dialect.Network{})
		if err != nil {
			t.Fatal(err)
		}
		c, err := opt.DialContext(t.Context(), srv.Listener.Addr().String())
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		_, isTLS := c.(*tls.Conn)
		_ = c.Close()
		if isTLS != wantTLS {
			t.Errorf("%s: TLS = %v, want %v", mode, isTLS, wantTLS)
		}
	}
	// Over HTTP the transport adds TLS: the dialer returns the plain connection.
	opt, _ := d.options(profile.Profile{Host: host, Port: n, TLS: profile.TLS{Mode: profile.TLSRequire}, Options: map[string]string{"protocol": "http"}}, "", dialect.Network{})
	c, err := opt.DialContext(t.Context(), srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, isTLS := c.(*tls.Conn); isTLS || opt.TLS == nil {
		t.Errorf("http dial TLS = %v, transport TLS = %v", isTLS, opt.TLS != nil)
	}
}

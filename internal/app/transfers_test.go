package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/iagapie/quire/internal/dialect/all"
	_ "github.com/iagapie/quire/internal/dialect/duckdb" // built into the app only with -tags duckdb
	"github.com/iagapie/quire/internal/docsession"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/query"
)

// connect opens a session for p with a file store in a temporary directory: tests
// never touch the keychain.
func connect(t *testing.T, p profile.Profile) (*Connections, string) {
	t.Helper()
	store, err := profile.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	engine := query.New()
	t.Cleanup(engine.Close)
	c := NewConnections(store, engine, docsession.NewRegistry())
	t.Cleanup(func() { _ = c.ServiceShutdown() })
	saved, err := c.SaveProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Connect(t.Context(), saved.ID); err != nil {
		t.Fatal(err)
	}
	return c, saved.ID
}

func waitJob(t *testing.T, tr *Transfers, id string) TransferJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		j, err := tr.Job(id)
		if err != nil {
			t.Fatal(err)
		}
		if j.State != stateRunning {
			return j
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the transfer did not finish")
	return TransferJob{}
}

// server is a database the round trip runs on.
type server struct {
	name     string
	profile  profile.Profile
	database string
	schema   string
	// setup creates the source table t_src and an empty t_dst with the same columns.
	setup []string
	// cleanup drops them.
	cleanup []string
	// noKeys: the database does not enforce primary keys (ClickHouse), so inserts never
	// collide and upserts are not available.
	noKeys bool
	// completion creates q_parent and q_child (q_child.parent_id references q_parent.id
	// where foreign keys exist); nil uses the standard statements.
	completion []string
	// upper: unquoted names are folded to upper case (Oracle).
	upper bool
}

// id is how the database names an object created with an unquoted name.
func (s server) id(name string) string {
	if s.upper {
		return strings.ToUpper(name)
	}
	return name
}

func (s server) ids(names ...string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = s.id(n)
	}
	return out
}

func servers(t *testing.T) []server {
	t.Helper()
	dir := t.TempDir()
	out := []server{{
		name:    "SQLite",
		profile: profile.Profile{Name: "sqlite", Driver: "sqlite", Database: filepath.Join(dir, "t.db"), Options: map[string]string{"create": "true"}},
		setup: []string{
			`CREATE TABLE t_src (id INTEGER PRIMARY KEY, name TEXT, price REAL, ok BOOLEAN, data BLOB, note TEXT)`,
			`CREATE TABLE t_dst (id INTEGER PRIMARY KEY, name TEXT, price REAL, ok BOOLEAN, data BLOB, note TEXT)`,
		},
		database: "main",
	}}
	hostPort := func(env string) (string, int) {
		host, port, _ := strings.Cut(os.Getenv(env), ":")
		n, _ := strconv.Atoi(port)
		return host, n
	}
	if os.Getenv("QUIRE_TEST_POSTGRES") != "" {
		host, port := hostPort("QUIRE_TEST_POSTGRES")
		out = append(out, server{
			name:     "PostgreSQL",
			profile:  profile.Profile{Name: "pg", Driver: "postgres", Host: host, Port: port, User: "quire", Password: "quirepw", Database: "shop", TLS: profile.TLS{Mode: profile.TLSDisable}},
			database: "shop", schema: "public",
			setup: []string{
				`CREATE TABLE t_src (id int PRIMARY KEY, name text, price numeric(10,2), ok boolean, data bytea, note jsonb)`,
				`CREATE TABLE t_dst (id int PRIMARY KEY, name text, price numeric(10,2), ok boolean, data bytea, note jsonb)`,
			},
		})
	}
	if os.Getenv("QUIRE_TEST_MYSQL") != "" {
		host, port := hostPort("QUIRE_TEST_MYSQL")
		out = append(out, server{
			name:     "MySQL",
			profile:  profile.Profile{Name: "my", Driver: "mysql", Host: host, Port: port, User: "quire", Password: "quirepw", Database: "shop", TLS: profile.TLS{Mode: profile.TLSDisable}},
			database: "shop",
			setup: []string{
				"CREATE TABLE t_src (id INT PRIMARY KEY, name VARCHAR(50), price DECIMAL(10,2), ok BOOL, data VARBINARY(20), note JSON)",
				"CREATE TABLE t_dst (id INT PRIMARY KEY, name VARCHAR(50), price DECIMAL(10,2), ok BOOL, data VARBINARY(20), note JSON)",
			},
		})
	}
	if os.Getenv("QUIRE_TEST_SQLSERVER") != "" {
		host, port := hostPort("QUIRE_TEST_SQLSERVER")
		out = append(out, server{
			name: "SQL Server",
			profile: profile.Profile{Name: "ms", Driver: "sqlserver", Host: host, Port: port, User: "sa", Password: os.Getenv("QUIRE_TEST_SQLSERVER_PASSWORD"),
				Database: "master", TLS: profile.TLS{Mode: profile.TLSPrefer}},
			database: "master", schema: "dbo",
			setup: []string{
				"CREATE TABLE t_src (id int PRIMARY KEY, name nvarchar(50), price decimal(10,2), ok bit, data varbinary(20), note nvarchar(max))",
				"CREATE TABLE t_dst (id int PRIMARY KEY, name nvarchar(50), price decimal(10,2), ok bit, data varbinary(20), note nvarchar(max))",
			},
		})
	}
	out = append(out, server{
		name:     "DuckDB",
		profile:  profile.Profile{Name: "duck", Driver: "duckdb", Database: filepath.Join(dir, "t.duckdb"), Options: map[string]string{"create": "true"}},
		database: "t", schema: "main",
		setup: []string{
			`CREATE TABLE t_src (id INTEGER PRIMARY KEY, name VARCHAR, price DECIMAL(10,2), ok BOOLEAN, data BLOB, note JSON)`,
			`CREATE TABLE t_dst (id INTEGER PRIMARY KEY, name VARCHAR, price DECIMAL(10,2), ok BOOLEAN, data BLOB, note JSON)`,
		},
	})
	pgLike := func(name, env, driver, user, password, database string) {
		if os.Getenv(env) == "" {
			return
		}
		host, port := hostPort(env)
		out = append(out, server{
			name:     name,
			profile:  profile.Profile{Name: driver, Driver: driver, Host: host, Port: port, User: user, Password: password, Database: database, TLS: profile.TLS{Mode: profile.TLSDisable}},
			database: database, schema: "public",
			setup: []string{
				`CREATE TABLE t_src (id int PRIMARY KEY, name text, price numeric(10,2), ok boolean, data bytea, note jsonb)`,
				`CREATE TABLE t_dst (id int PRIMARY KEY, name text, price numeric(10,2), ok boolean, data bytea, note jsonb)`,
			},
		})
	}
	pgLike("CockroachDB", "QUIRE_TEST_COCKROACH", "cockroachdb", "root", "", "defaultdb")
	pgLike("YugabyteDB", "QUIRE_TEST_YUGABYTE", "yugabytedb", "yugabyte", "", "yugabyte")
	if os.Getenv("QUIRE_TEST_TIDB") != "" {
		host, port := hostPort("QUIRE_TEST_TIDB")
		out = append(out, server{
			name:     "TiDB",
			profile:  profile.Profile{Name: "tidb", Driver: "tidb", Host: host, Port: port, User: "root", Database: "test", TLS: profile.TLS{Mode: profile.TLSPrefer}},
			database: "test",
			setup: []string{
				"CREATE TABLE t_src (id INT PRIMARY KEY, name VARCHAR(50), price DECIMAL(10,2), ok BOOL, data VARBINARY(20), note JSON)",
				"CREATE TABLE t_dst (id INT PRIMARY KEY, name VARCHAR(50), price DECIMAL(10,2), ok BOOL, data VARBINARY(20), note JSON)",
			},
		})
	}
	if os.Getenv("QUIRE_TEST_ORACLE") != "" {
		host, port := hostPort("QUIRE_TEST_ORACLE")
		out = append(out, server{
			name: "Oracle (built-in client)",
			profile: profile.Profile{Name: "ora", Driver: "oracle", Host: host, Port: port, User: "quire", Password: "quirepw", Database: "FREEPDB1",
				Options: map[string]string{"client": "builtin"}},
			schema: "QUIRE",
			setup: []string{
				"CREATE TABLE t_src (id NUMBER(10) PRIMARY KEY, name VARCHAR2(50), price NUMBER(10,2), ok NUMBER(1), data RAW(20), note CLOB)",
				"CREATE TABLE t_dst (id NUMBER(10) PRIMARY KEY, name VARCHAR2(50), price NUMBER(10,2), ok NUMBER(1), data RAW(20), note CLOB)",
			},
			upper: true,
		})
	}
	if os.Getenv("QUIRE_TEST_CLICKHOUSE") != "" {
		host, port := hostPort("QUIRE_TEST_CLICKHOUSE")
		out = append(out, server{
			name:     "ClickHouse",
			profile:  profile.Profile{Name: "ch", Driver: "clickhouse", Host: host, Port: port, User: "quire", Password: "quirepw", Database: "default"},
			database: "default",
			setup: []string{
				"CREATE TABLE t_src (id Int32, name Nullable(String), price Decimal(10, 2), ok Bool, data String, note Nullable(String)) ENGINE = MergeTree ORDER BY id",
				"CREATE TABLE t_dst (id Int32, name Nullable(String), price Decimal(10, 2), ok Bool, data String, note Nullable(String)) ENGINE = MergeTree ORDER BY id",
			},
			noKeys: true,
			completion: []string{
				"CREATE TABLE q_parent (id Int32, code String) ENGINE = MergeTree ORDER BY id",
				"CREATE TABLE q_child (id Int32, parent_id Int32, note String) ENGINE = MergeTree ORDER BY id",
			},
		})
	}
	for i := range out {
		out[i].cleanup = []string{"DROP TABLE IF EXISTS t_src", "DROP TABLE IF EXISTS t_dst"}
	}
	return out
}

// TestTransferRoundTrip exports a table in every format and imports it back into an
// empty table: the rows must come back the same, NULLs and empty strings included.
func TestTransferRoundTrip(t *testing.T) {
	for _, srv := range servers(t) {
		t.Run(srv.name, func(t *testing.T) {
			c, id := connect(t, srv.profile)
			s, _ := c.get(id)
			db, err := s.DB(t.Context(), srv.database)
			if err != nil {
				t.Fatal(err)
			}
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			for _, q := range srv.cleanup {
				exec(q)
			}
			t.Cleanup(func() {
				for _, q := range srv.cleanup {
					_, _ = db.ExecContext(context.Background(), q)
				}
			})
			for _, q := range srv.setup {
				exec(q)
			}
			// 100 rows per INSERT: ClickHouse writes a part per statement.
			var args []any
			flush := func() {
				if len(args) == 0 {
					return
				}
				rows := make([]string, len(args)/6)
				for r := range rows {
					ph := make([]string, 6)
					for k := range ph {
						ph[k] = s.Driver.Placeholder(r*6 + k + 1)
					}
					rows[r] = "(" + strings.Join(ph, ", ") + ")"
				}
				exec("INSERT INTO t_src (id, name, price, ok, data, note) VALUES "+strings.Join(rows, ", "), args...)
				args = args[:0]
			}
			for id := 1; id <= 1200; id++ {
				var name, note any = "name " + strconv.Itoa(id), `{"k": ` + strconv.Itoa(id) + `}`
				switch id {
				case 2:
					name = ""
				case 3:
					name, note = nil, nil
				case 4:
					name = "comma, \"quote\"\nnew line ünicode"
				}
				args = append(args, id, name, "12.50", id%2 == 0, []byte{0, 1, byte(id)}, note)
				if id%100 == 0 {
					flush()
				}
			}
			flush()
			tr := NewTransfers(c)
			dir := t.TempDir()
			formats := []string{"csv", "jsonl", "json", "sql"}
			for _, f := range formats {
				job, err := tr.StartExport(ExportRequest{SessionID: id, Database: srv.database, Schema: srv.schema, Tables: []string{srv.id("t_src")},
					Dir: filepath.Join(dir, f), Format: f, Gzip: f == "jsonl", Definitions: true})
				if err != nil {
					t.Fatal(err)
				}
				j := waitJob(t, tr, job.ID)
				if j.State != stateDone || j.Items[0].Rows != 1200 {
					t.Fatalf("export %s = %+v", f, j)
				}
			}
			want := dump(t, db, "t_src")
			for _, f := range []string{"csv", "jsonl", "json"} {
				scan := tr.ScanImportFiles([]string{filepath.Join(dir, f)})
				if len(scan.Files) != 1 || scan.Files[0].Table != srv.id("t_src") || scan.Files[0].Definition == "" {
					t.Fatalf("scan %s = %+v", f, scan)
				}
				scan.Files[0].Table = srv.id("t_dst")
				job, err := tr.StartImport(ImportRequest{SessionID: id, Database: srv.database, Schema: srv.schema, Files: scan.Files, Mode: ImportReplace})
				if err != nil {
					t.Fatal(err)
				}
				j := waitJob(t, tr, job.ID)
				if j.State != stateDone || j.Items[0].Rows != 1200 || j.Items[0].Failed != 0 {
					t.Fatalf("import %s = %+v", f, j)
				}
				if got := dump(t, db, "t_dst"); got != want {
					t.Fatalf("import %s changed the rows:\n%s", f, firstDiff(got, want))
				}
			}
			if srv.noKeys {
				return // no duplicate keys to reject, no upserts
			}
			// Insert mode counts the rows the server rejects (duplicate keys).
			scan := tr.ScanImportFiles([]string{filepath.Join(dir, "csv", srv.id("t_src")+".csv")})
			scan.Files[0].Table = srv.id("t_dst")
			exec("DELETE FROM t_dst WHERE id > 1000")
			job, _ := tr.StartImport(ImportRequest{SessionID: id, Database: srv.database, Schema: srv.schema, Files: scan.Files, Mode: ImportInsert})
			j := waitJob(t, tr, job.ID)
			if j.State != stateDone || j.Items[0].Rows != 200 || j.Items[0].Failed != 1000 || j.Items[0].Warning == "" {
				t.Fatalf("insert over existing rows = %+v", j.Items[0])
			}
			// Upsert replaces rows with the same key.
			exec("UPDATE t_dst SET name = 'changed' WHERE id = 1")
			job, _ = tr.StartImport(ImportRequest{SessionID: id, Database: srv.database, Schema: srv.schema, Files: scan.Files, Mode: ImportUpsert})
			if j := waitJob(t, tr, job.ID); j.State != stateDone || j.Items[0].Failed != 0 {
				t.Fatalf("upsert = %+v", j.Items[0])
			}
			if got := dump(t, db, "t_dst"); got != want {
				t.Fatalf("upsert:\n%s", firstDiff(got, want))
			}
			// The SQL dump is a script: it recreates the table and its rows.
			for _, q := range srv.cleanup {
				exec(q)
			}
			sqlScan := tr.ScanImportFiles([]string{filepath.Join(dir, "sql")})
			if len(sqlScan.Files) != 1 || sqlScan.Files[0].Format != "sql" {
				t.Fatalf("sql scan = %+v", sqlScan)
			}
			job, _ = tr.StartImport(ImportRequest{SessionID: id, Database: srv.database, Schema: srv.schema, Files: sqlScan.Files, StopOnError: true})
			if j := waitJob(t, tr, job.ID); j.State != stateDone || j.Items[0].Failed != 0 {
				t.Fatalf("sql import = %+v", j.Items[0])
			}
			if got := dump(t, db, "t_src"); got != want {
				t.Fatalf("sql import:\n%s", firstDiff(got, want))
			}
		})
	}
}

// dump renders a table as text, ordered by id.
func dump(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT * FROM "+table+" ORDER BY id") //nolint:gosec // G202: a test table
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := query.Columns(rows)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for rows.Next() {
		row, err := query.FullRow(rows, cols, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range row {
			if k > 0 {
				b.WriteString(" | ")
			}
			if v == nil {
				b.WriteString("<NULL>")
			} else {
				b.WriteString(strconv.Quote(*v))
			}
		}
		b.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// firstDiff shows the first line where got and want differ.
func firstDiff(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for k := range max(len(g), len(w)) {
		var a, b string
		if k < len(g) {
			a = g[k]
		}
		if k < len(w) {
			b = w[k]
		}
		if a != b {
			return "line " + strconv.Itoa(k+1) + ":\n got " + a + "\nwant " + b
		}
	}
	return "(no difference)"
}

// TestExportResumesAfterFailure fails an export in the middle: it resumes after the
// rows already written, without duplicates.
func TestExportResumesAfterFailure(t *testing.T) {
	c, id := connect(t, profile.Profile{Name: "sqlite", Driver: "sqlite", Database: filepath.Join(t.TempDir(), "r.db"), Options: map[string]string{"create": "true"}})
	s, _ := c.get(id)
	db, _ := s.DB(t.Context(), "")
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT);
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 3000) INSERT INTO t SELECT i, 'v' || i FROM n`); err != nil {
		t.Fatal(err)
	}
	failed := false
	exportFault = func(written int64) error {
		if written == 1700 && !failed {
			failed = true
			return errors.New("database is locked") // transient for SQLite
		}
		return nil
	}
	oldBackoff := transferBackoff
	transferBackoff = 10 * time.Millisecond
	t.Cleanup(func() { exportFault, transferBackoff = nil, oldBackoff })
	tr := NewTransfers(c)
	dir := t.TempDir()
	job, err := tr.StartExport(ExportRequest{SessionID: id, Database: "main", Tables: []string{"t"}, Dir: dir, Format: "jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, tr, job.ID)
	if j.State != stateDone || j.Items[0].Rows != 3000 || !failed {
		t.Fatalf("export = %+v", j)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "t.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3000 || lines[1700] != `{"id": 1701, "v": "v1701"}` {
		t.Fatalf("%d lines, line 1701 = %s", len(lines), lines[1700])
	}
}

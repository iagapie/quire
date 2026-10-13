package query

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iagapie/quire/internal/dialect"
	_ "github.com/iagapie/quire/internal/dialect/clickhouse"
	_ "github.com/iagapie/quire/internal/dialect/cockroach"
	_ "github.com/iagapie/quire/internal/dialect/oracle"
	_ "github.com/iagapie/quire/internal/dialect/tidb"
	_ "github.com/iagapie/quire/internal/dialect/yugabyte"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/session"
)

// engineCase is a database the engine runs on: paging from the open cursor, Count,
// and a Stop that keeps the session state.
type engineCase struct {
	name    string
	profile profile.Profile
	// rows returns 250 rows numbered 1..250 in column n.
	rows string
	// set changes session state; get reads it back as "7".
	set, get string
	// slow runs for a long time.
	slow string
	// probe is a trivial query ("SELECT 1" when empty).
	probe string
	// container is the server container (from QUIRE_TEST_*_CONTAINER), paused to
	// check the recovery.
	container string
}

func engineCases(t *testing.T) []engineCase {
	t.Helper()
	hostPort := func(env string) (string, int) {
		host, port, _ := strings.Cut(os.Getenv(env), ":")
		n, _ := strconv.Atoi(port)
		return host, n
	}
	var out []engineCase
	// DuckDB is registered by duckdb_test.go, except where duckdb-go has no library.
	if _, ok := dialect.Get("duckdb"); ok {
		out = append(out, engineCase{
			name:    "DuckDB",
			profile: profile.Profile{Driver: "duckdb", Database: filepath.Join(t.TempDir(), "e.duckdb"), Options: map[string]string{"create": "true"}},
			rows:    "SELECT range + 1 AS n FROM range(250) ORDER BY n",
			set:     "SET VARIABLE v = 7", get: "SELECT getvariable('v')",
			slow: "SELECT count(*) FROM range(1000000000000) a",
		})
	}
	pg := func(name, env, driver, user, database string) {
		if os.Getenv(env) == "" {
			return
		}
		host, port := hostPort(env)
		out = append(out, engineCase{
			name: name, container: os.Getenv(env + "_CONTAINER"),
			profile: profile.Profile{Driver: driver, Host: host, Port: port, User: user, Database: database, TLS: profile.TLS{Mode: profile.TLSDisable}},
			rows:    "SELECT n FROM generate_series(1, 250) AS g(n) ORDER BY n",
			set:     "SET application_name = '7'", get: "SELECT current_setting('application_name')",
			slow: "SELECT pg_sleep(20)",
		})
	}
	pg("CockroachDB", "QUIRE_TEST_COCKROACH", "cockroachdb", "root", "defaultdb")
	pg("YugabyteDB", "QUIRE_TEST_YUGABYTE", "yugabytedb", "yugabyte", "yugabyte")
	if os.Getenv("QUIRE_TEST_TIDB") != "" {
		host, port := hostPort("QUIRE_TEST_TIDB")
		out = append(out, engineCase{
			name: "TiDB", container: os.Getenv("QUIRE_TEST_TIDB_CONTAINER"),
			profile: profile.Profile{Driver: "tidb", Host: host, Port: port, User: "root", Database: "test", TLS: profile.TLS{Mode: profile.TLSPrefer}},
			rows:    "WITH RECURSIVE g(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM g WHERE n < 250) SELECT n FROM g ORDER BY n",
			set:     "SET @v = 7", get: "SELECT @v",
			slow: "SELECT SLEEP(20)",
		})
	}
	if os.Getenv("QUIRE_TEST_ORACLE") != "" {
		host, port := hostPort("QUIRE_TEST_ORACLE")
		out = append(out, engineCase{
			name: "Oracle (built-in client)", container: os.Getenv("QUIRE_TEST_ORACLE_CONTAINER"),
			profile: profile.Profile{Driver: "oracle", Host: host, Port: port, User: "quire", Password: "quirepw", Database: "FREEPDB1",
				Options: map[string]string{"client": "builtin"}},
			rows: "SELECT level AS n FROM dual CONNECT BY level <= 250 ORDER BY n",
			set:  "BEGIN DBMS_SESSION.SET_IDENTIFIER('7'); END;", get: "SELECT SYS_CONTEXT('USERENV', 'CLIENT_IDENTIFIER') FROM dual",
			// DBMS_SESSION.SLEEP cannot be interrupted until it ends; a long query can.
			slow:  "SELECT COUNT(*) FROM all_objects a, all_objects b, all_objects c",
			probe: "SELECT 1 FROM dual",
		})
	}
	if os.Getenv("QUIRE_TEST_CLICKHOUSE") != "" {
		host, port := hostPort("QUIRE_TEST_CLICKHOUSE")
		out = append(out, engineCase{
			name: "ClickHouse", container: os.Getenv("QUIRE_TEST_CLICKHOUSE_CONTAINER"),
			profile: profile.Profile{Driver: "clickhouse", Host: host, Port: port, User: "quire", Password: "quirepw", Database: "default"},
			rows:    "SELECT number + 1 AS n FROM numbers(250) ORDER BY n",
			set:     "SET max_threads = 7", get: "SELECT toString(getSetting('max_threads'))",
			slow: "SELECT sleepEachRow(1) FROM numbers(30) SETTINGS max_block_size = 1",
		})
	}
	return out
}

func TestEngineOnOtherDialects(t *testing.T) {
	for _, c := range engineCases(t) {
		t.Run(c.name, func(t *testing.T) {
			p := c.profile
			p.ID, p.Name, p.QueryTimeoutMs = "e-"+p.Driver, c.name, 60_000
			p.Normalize()
			s, err := session.Open(t.Context(), p.ID, p, session.Options{PingInterval: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			e := New()
			t.Cleanup(e.Close)
			tab := "t-" + p.Driver

			run(t, e, s, tab, c.set, ModeAll, 0)
			res := run(t, e, s, tab, c.rows, ModeAll, 0)
			r := res.Results[0]
			if r.Error != "" || r.Kind != "rows" || len(r.Page.Rows) != 10 || !r.Page.HasMore {
				t.Fatalf("first page = %+v", r)
			}
			page, err := e.FetchPage(t.Context(), s, tab, r.ID, "", 240, 10)
			if err != nil || len(page.Rows) != 10 || *page.Rows[0][0] != "241" || page.HasMore {
				t.Fatalf("last page = %+v, %v", page, err)
			}
			if n, err := e.Count(t.Context(), s, tab, r.ID, ""); err != nil || n != 250 {
				t.Errorf("count = %d, %v", n, err)
			}

			done := make(chan RunResult)
			go func() {
				done <- e.Run(context.Background(), s, tab, "op-slow", RunRequest{Script: c.slow, Mode: ModeAll, PageSize: 10})
			}()
			time.Sleep(700 * time.Millisecond)
			start := time.Now()
			s.Cancel("op-slow")
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("stop did not end the statement")
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("stop took %s", d)
			}
			res = run(t, e, s, tab, c.get, ModeAll, 0)
			if res.Results[0].Error != "" || len(res.Results[0].Page.Rows) != 1 {
				t.Fatalf("after stop = %+v", res.Results[0])
			}
			if v := *res.Results[0].Page.Rows[0][0]; v != "7" {
				t.Errorf("session state after stop = %q (%v)", v, res.Output)
			}
		})
	}
}

func TestOtherDialectsRecoverWhenServerPauses(t *testing.T) {
	for _, c := range engineCases(t) {
		if c.container == "" {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			p := c.profile
			p.ID, p.Name, p.QueryTimeoutMs = "p-"+p.Driver, c.name, 2000
			p.Normalize()
			s, err := session.Open(t.Context(), p.ID, p, session.Options{PingInterval: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			probe := c.probe
			if probe == "" {
				probe = "SELECT 1"
			}
			pauseAndRecover(t, s, c.container, probe)
		})
	}
}

// DBMS_OUTPUT lines reach the Output tab with the built-in Oracle client too.
func TestOracleBuiltinServerOutput(t *testing.T) {
	addr := os.Getenv("QUIRE_TEST_ORACLE")
	if addr == "" {
		t.Skip("QUIRE_TEST_ORACLE not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	p := profile.Profile{ID: "ora-out", Name: "ora", Driver: "oracle", Host: host, Port: n, User: "quire", Password: "quirepw", Database: "FREEPDB1",
		Options: map[string]string{"client": "builtin"}, QueryTimeoutMs: 60_000}
	p.Normalize()
	s, err := session.Open(t.Context(), p.ID, p, session.Options{PingInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	e := New()
	t.Cleanup(e.Close)
	res := run(t, e, s, "out", "BEGIN\n  DBMS_OUTPUT.PUT_LINE('hello from PL/SQL');\nEND;\n/\n", ModeAll, 0)
	if !strings.Contains(strings.Join(res.Output, "\n"), "hello from PL/SQL") {
		t.Errorf("output = %q, results %+v", res.Output, res.Results)
	}
}

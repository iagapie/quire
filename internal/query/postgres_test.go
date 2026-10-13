package query

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/iagapie/quire/internal/dialect/postgres"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/session"
)

// Integration tests against a disposable PostgreSQL:
//
//	QUIRE_TEST_POSTGRES=127.0.0.1:55432 QUIRE_TEST_POSTGRES_CONTAINER=quire-test-pg go test ./internal/query -run Postgres
//
// The user is quire/quirepw and the database shop. The container name enables the tests
// that pause the server.
func openPostgres(t *testing.T, timeout time.Duration) *session.Session {
	t.Helper()
	addr := os.Getenv("QUIRE_TEST_POSTGRES")
	if addr == "" {
		t.Skip("QUIRE_TEST_POSTGRES not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	p := profile.Profile{ID: "pg", Name: "pg", Driver: "postgres", Host: host, Port: n, User: "quire", Password: "quirepw", Database: "shop",
		TLS: profile.TLS{Mode: profile.TLSDisable}, QueryTimeoutMs: int(timeout.Milliseconds())}
	p.Normalize()
	s, err := session.Open(t.Context(), "pg", p, session.Options{PingInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func terminate(t *testing.T, s *session.Session, appName string) {
	t.Helper()
	db, err := s.DB(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = $1 AND pid <> pg_backend_pid()`, appName); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCursorSurvivesDroppedConnection(t *testing.T) {
	s := openPostgres(t, 30*time.Second)
	e := New()
	t.Cleanup(e.Close)
	tab := "pgtab"
	run(t, e, s, tab, "SET application_name = 'quire-cursor-test'", ModeAll, 0)
	res := run(t, e, s, tab, "SELECT g AS n FROM generate_series(1, 5000) g ORDER BY g", ModeAll, 0)
	r := res.Results[0]
	if r.Kind != "rows" || !r.Page.HasMore {
		t.Fatalf("first page = %+v", r)
	}
	// The server kills the tab connection while the cursor is open.
	terminate(t, s, "quire-cursor-test")
	// The next page is rebuilt by running only that SELECT again, with LIMIT/OFFSET.
	p, err := e.FetchPage(t.Context(), s, tab, r.ID, "", 2000, 10)
	if err != nil {
		t.Fatalf("page after drop: %v", err)
	}
	if len(p.Rows) != 10 || *p.Rows[0][0] != "2001" {
		t.Fatalf("page after drop = %v", p.Rows)
	}
	// The lost session (application_name) is reported on the next run.
	res = run(t, e, s, tab, "SELECT current_setting('application_name')", ModeAll, 0)
	if len(res.Output) == 0 || !strings.Contains(res.Output[0], "Session state was reset") {
		t.Fatalf("no session reset notice: %v", res.Output)
	}
	if *res.Results[0].Page.Rows[0][0] != "Quire" {
		t.Fatalf("application_name = %s", *res.Results[0].Page.Rows[0][0])
	}
}

func TestPostgresReadRetriesOnBrokenConnection(t *testing.T) {
	s := openPostgres(t, 30*time.Second)
	e := New()
	t.Cleanup(e.Close)
	old := preflightAfter
	preflightAfter = time.Hour // send the statement into the dead connection
	t.Cleanup(func() { preflightAfter = old })
	var retries int
	e.OnRetry = func(string, int, error) { retries++ }
	tab := "retrytab"
	run(t, e, s, tab, "SET application_name = 'quire-retry-test'", ModeAll, 0)
	terminate(t, s, "quire-retry-test")
	res := run(t, e, s, tab, "SELECT 42", ModeAll, 0)
	if res.Results[0].Kind != "rows" || *res.Results[0].Page.Rows[0][0] != "42" {
		t.Fatalf("read after drop = %+v", res.Results[0])
	}
	if retries == 0 {
		t.Error("the read was not retried")
	}
	// A write is not retried: it fails and says so.
	run(t, e, s, tab, "CREATE TEMP TABLE w (x int); SET application_name = 'quire-retry-test'", ModeAll, 0)
	terminate(t, s, "quire-retry-test")
	res = run(t, e, s, tab, "INSERT INTO w VALUES (1)", ModeAll, 0)
	if res.Results[0].Kind != "error" {
		t.Fatalf("write after drop = %+v", res.Results[0])
	}
}

func TestPostgresManualTransactionAndEdits(t *testing.T) {
	s := openPostgres(t, 30*time.Second)
	e := New()
	t.Cleanup(e.Close)
	run(t, e, s, "setup", "DROP TABLE IF EXISTS public.quire_tx; CREATE TABLE public.quire_tx (id int PRIMARY KEY, v text, n numeric, b boolean, d date, j jsonb)", ModeAll, 0)
	t.Cleanup(func() { run(t, e, s, "cleanup", "DROP TABLE IF EXISTS public.quire_tx", ModeAll, 0) })

	if _, err := e.SetManual(s, "tx", true); err != nil {
		t.Fatal(err)
	}
	res := run(t, e, s, "tx", "INSERT INTO quire_tx VALUES (1, 'a', 1.5, true, '2026-01-02', '{\"k\": 1}')", ModeAll, 0)
	if res.Results[0].RowsAffected != 1 || !res.Tx.Open {
		t.Fatalf("insert in tx = %+v %+v", res.Results[0], res.Tx)
	}
	// Another tab does not see the uncommitted row.
	other := run(t, e, s, "other", "SELECT count(*) FROM quire_tx", ModeAll, 0)
	if *other.Results[0].Page.Rows[0][0] != "0" {
		t.Fatal("uncommitted row visible to another tab")
	}
	if _, err := e.Commit(t.Context(), s, "tx"); err != nil {
		t.Fatal(err)
	}
	other = run(t, e, s, "other", "SELECT count(*) FROM quire_tx", ModeAll, 0)
	if *other.Results[0].Page.Rows[0][0] != "1" {
		t.Fatal("commit not visible")
	}

	// Edits with string values for typed columns.
	res = run(t, e, s, "ed", "SELECT * FROM quire_tx", ModeAll, 0)
	ti := res.Results[0].Table
	if ti == nil || len(ti.Key) != 1 {
		t.Fatalf("table info = %+v", ti)
	}
	str := func(v string) *string { return &v }
	_, err := e.ApplyEdits(t.Context(), s, "ed", EditRequest{Table: *ti, Edits: []Edit{
		{Op: "update", Key: map[string]*string{"id": str("1")}, Values: map[string]*string{"n": str("2.25"), "b": str("false"), "d": str("2026-12-31"), "j": str(`{"k": 2}`)}},
		{Op: "insert", Values: map[string]*string{"id": str("2"), "v": nil}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res = run(t, e, s, "ed", "SELECT n, b, d, j->>'k' FROM quire_tx WHERE id = 1", ModeAll, 0)
	row := res.Results[0].Page.Rows[0]
	if *row[0] != "2.25" || *row[1] != "false" || *row[2] != "2026-12-31" || *row[3] != "2" {
		t.Fatalf("after edits: %v %v %v %v", *row[0], *row[1], *row[2], *row[3])
	}
}

func TestPostgresTimeoutAndRecoveryWhenServerPauses(t *testing.T) {
	container := os.Getenv("QUIRE_TEST_POSTGRES_CONTAINER")
	if container == "" {
		t.Skip("QUIRE_TEST_POSTGRES_CONTAINER not set")
	}
	s := openPostgres(t, 2*time.Second)
	e := New()
	t.Cleanup(e.Close)
	docker := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil { //nolint:gosec // G204: test helper, fixed binary and test-controlled arguments
			t.Fatalf("docker %v: %v %s", args, err, out)
		}
	}
	docker("pause", container)
	t.Cleanup(func() { _ = exec.Command("docker", "unpause", container).Run() }) //nolint:gosec,noctx // G204: test cleanup with the test container name
	waitFrozen(t, os.Getenv("QUIRE_TEST_POSTGRES"))
	res := run(t, e, s, "p", "SELECT 1", ModeAll, 0)
	if res.Results[0].Kind != "error" || !strings.Contains(res.Results[0].Error, "timed out") {
		t.Fatalf("paused server = %+v", res.Results[0])
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.Health() != session.HealthReconnecting && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if s.Health() != session.HealthReconnecting {
		t.Errorf("health = %s, want reconnecting", s.Health())
	}
	docker("unpause", container)
	// No reconnect by hand: the same session works again.
	res = run(t, e, s, "p", "SELECT 2", ModeAll, 0)
	if res.Results[0].Kind != "rows" || *res.Results[0].Page.Rows[0][0] != "2" {
		t.Fatalf("after unpause = %+v", res.Results[0])
	}
	deadline = time.Now().Add(5 * time.Second)
	for s.Health() != session.HealthOnline && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if s.Health() != session.HealthOnline {
		t.Errorf("health = %s, want online", s.Health())
	}
}

// waitFrozen waits until the paused server stops answering (docker pause takes effect
// asynchronously on Docker Desktop).
func waitFrozen(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var d net.Dialer
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		c, err := d.DialContext(ctx, "tcp", addr)
		cancel()
		if err == nil {
			// An SSLRequest gets one byte back from a live server.
			_, _ = c.Write([]byte{0, 0, 0, 8, 4, 210, 22, 47})
			_ = c.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
			_, rerr := c.Read(make([]byte, 1))
			_ = c.Close()
			if rerr != nil {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the server did not pause")
}

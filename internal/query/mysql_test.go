package query

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iagapie/quire/internal/dialect"
	_ "github.com/iagapie/quire/internal/dialect/mysql"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/session"
)

// Integration tests against a disposable MySQL (see scripts/test-databases.sh):
//
//	QUIRE_TEST_MYSQL=127.0.0.1:53306 QUIRE_TEST_MYSQL_CONTAINER=quire-test-mysql go test ./internal/query -run MySQL
func openMySQL(t *testing.T, timeout time.Duration) *session.Session {
	t.Helper()
	addr := os.Getenv("QUIRE_TEST_MYSQL")
	if addr == "" {
		t.Skip("QUIRE_TEST_MYSQL not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	p := profile.Profile{ID: "my", Name: "my", Driver: "mysql", Host: host, Port: n, User: "quire", Password: "quirepw", Database: "shop",
		TLS: profile.TLS{Mode: profile.TLSDisable}, QueryTimeoutMs: int(timeout.Milliseconds())}
	p.Normalize()
	s, err := session.Open(t.Context(), "my", p, session.Options{PingInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func noReset(t *testing.T, res RunResult) {
	t.Helper()
	for _, o := range res.Output {
		if strings.Contains(o, "Session state was reset") {
			t.Fatalf("the session was lost: %v", res.Output)
		}
	}
}

// Stopping a statement, a timeout and leaving an open cursor all interrupt the
// statement from another connection: the tab connection, and its session, survive.
func TestMySQLStopKeepsSession(t *testing.T) {
	s := openMySQL(t, 1500*time.Millisecond)
	e := New()
	t.Cleanup(e.Close)
	tab := "mystop"
	run(t, e, s, tab, "SET @v = 7", ModeAll, 0)

	// Stop.
	done := make(chan RunResult)
	go func() {
		done <- e.Run(context.Background(), s, tab, "op-sleep", RunRequest{Script: "SELECT SLEEP(20)", Mode: ModeAll, PageSize: 100})
	}()
	time.Sleep(400 * time.Millisecond)
	start := time.Now()
	s.Cancel("op-sleep")
	res := <-done
	if time.Since(start) > 5*time.Second {
		t.Errorf("stop took %s", time.Since(start))
	}
	// SLEEP returns 1 when it is interrupted, instead of failing.
	if r := res.Results[0]; r.Kind == "rows" && *r.Page.Rows[0][0] != "1" {
		t.Fatalf("stopped sleep = %+v", r)
	}
	res = run(t, e, s, tab, "SELECT @v", ModeAll, 0)
	noReset(t, res)
	if *res.Results[0].Page.Rows[0][0] != "7" {
		t.Fatalf("@v after stop = %v", *res.Results[0].Page.Rows[0][0])
	}

	// Timeout of a statement that does not stop by itself.
	res = run(t, e, s, tab, "SELECT COUNT(*) FROM information_schema.columns a, information_schema.columns b, information_schema.columns c", ModeAll, 0)
	if res.Results[0].Kind != "error" || !strings.Contains(res.Results[0].Error, "timed out") {
		t.Fatalf("timeout = %+v", res.Results[0])
	}
	res = run(t, e, s, tab, "SELECT @v", ModeAll, 0)
	noReset(t, res)

	// An open cursor replaced by the next run.
	res = run(t, e, s, tab, "SELECT a.ORDINAL_POSITION FROM information_schema.columns a, information_schema.columns b", ModeAll, 0)
	if r := res.Results[0]; r.Kind != "rows" || !r.Page.HasMore {
		t.Fatalf("big result = %+v", r)
	}
	p, err := e.FetchPage(t.Context(), s, tab, res.Results[0].ID, "", 100, 100)
	if err != nil || len(p.Rows) != 100 {
		t.Fatalf("second page = %d rows, %v", len(p.Rows), err)
	}
	res = run(t, e, s, tab, "SELECT @v", ModeAll, 0)
	noReset(t, res)
	if *res.Results[0].Page.Rows[0][0] != "7" {
		t.Fatalf("@v after the cursor = %v", *res.Results[0].Page.Rows[0][0])
	}
}

func TestMySQLScriptTransactionsAndEdits(t *testing.T) {
	s := openMySQL(t, 30*time.Second)
	e := New()
	t.Cleanup(e.Close)
	run(t, e, s, "setup", "DROP TABLE IF EXISTS quire_my; CREATE TABLE quire_my (id INT PRIMARY KEY, v VARCHAR(20), n DECIMAL(10,2), d DATETIME, b BIT(1))", ModeAll, 0)
	t.Cleanup(func() { run(t, e, s, "cleanup", "DROP TABLE IF EXISTS quire_my", ModeAll, 0) })

	// A transaction started by the script is tracked, and warned about.
	res := run(t, e, s, "tx", "START TRANSACTION; INSERT INTO quire_my VALUES (1, 'a', 1.5, '2026-01-02 03:04:05', b'1')", ModeAll, 0)
	if !res.Tx.Open || res.Results[1].RowsAffected != 1 {
		t.Fatalf("script transaction = %+v %+v", res.Tx, res.Results)
	}
	if got := e.OpenTransactions(); len(got) != 1 || got[0] != "tx" {
		t.Fatalf("open transactions = %v", got)
	}
	if _, err := e.Commit(t.Context(), s, "tx"); err != nil {
		t.Fatal(err)
	}
	if e.State("tx").Open {
		t.Fatal("still open after commit")
	}

	// DELIMITER scripts.
	res = run(t, e, s, "tx", "DROP PROCEDURE IF EXISTS quire_p;\nDELIMITER //\nCREATE PROCEDURE quire_p() BEGIN SELECT COUNT(*) AS n FROM quire_my; END//\nDELIMITER ;\nCALL quire_p();\nDROP PROCEDURE quire_p", ModeAll, 0)
	for _, r := range res.Results {
		if r.Kind == "error" {
			t.Fatalf("delimiter script: %+v", r)
		}
	}
	if r := res.Results[2]; r.Kind != "rows" || *r.Page.Rows[0][0] != "1" {
		t.Fatalf("call = %+v", r)
	}

	// Explain.
	res = e.Run(t.Context(), s, "tx", "", RunRequest{Script: "SELECT * FROM quire_my WHERE id = 1", Mode: ModeAll, PageSize: 100, Explain: true})
	if res.Results[0].Kind != "rows" || !strings.HasPrefix(res.Results[0].Title, "Plan") {
		t.Fatalf("explain = %+v", res.Results)
	}

	// Values and edits.
	res = run(t, e, s, "ed", "SELECT * FROM quire_my", ModeAll, 0)
	r := res.Results[0]
	if r.Table == nil || len(r.Table.Key) != 1 {
		t.Fatalf("table info = %+v", r.Table)
	}
	row := r.Page.Rows[0]
	if *row[2] != "1.50" || *row[3] != "2026-01-02 03:04:05" || *row[4] != "1" {
		t.Fatalf("values = %v %v %v", *row[2], *row[3], *row[4])
	}
	str := func(v string) *string { return &v }
	_, err := e.ApplyEdits(t.Context(), s, "ed", EditRequest{Table: *r.Table, Edits: []Edit{
		{Op: "update", Key: map[string]*string{"id": str("1")}, Values: map[string]*string{"n": str("2.25"), "d": str("2026-12-31 00:00:00")}},
		{Op: "insert", Values: map[string]*string{"id": str("2"), "v": nil}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res = run(t, e, s, "ed", "SELECT n, d FROM quire_my WHERE id = 1; SELECT COUNT(*) FROM quire_my", ModeAll, 0)
	if *res.Results[0].Page.Rows[0][0] != "2.25" || *res.Results[1].Page.Rows[0][0] != "2" {
		t.Fatalf("after edits = %v", res.Results)
	}
	// Requery with a filter.
	res = run(t, e, s, "ed", "SELECT * FROM quire_my", ModeAll, 0)
	q, err := e.Requery(t.Context(), s, "ed", res.Results[0].ID, "", "id > 1", "id DESC", 10, 100)
	if err != nil || len(q.Page.Rows) != 1 || *q.Page.Rows[0][0] != "2" {
		t.Fatalf("requery = %+v, %v", q, err)
	}
}

func TestMySQLTimeoutAndRecoveryWhenServerPauses(t *testing.T) {
	container := os.Getenv("QUIRE_TEST_MYSQL_CONTAINER")
	if container == "" {
		t.Skip("QUIRE_TEST_MYSQL_CONTAINER not set")
	}
	s := openMySQL(t, 2*time.Second)
	pauseAndRecover(t, s, container, "SELECT 1")
}

// pauseAndRecover pauses the server container: a run times out, the session turns to
// reconnecting, and after the pause it works again without reconnecting by hand.
func pauseAndRecover(t *testing.T, s *session.Session, container, probe string) {
	t.Helper()
	e := New()
	t.Cleanup(e.Close)
	run(t, e, s, "p", probe, ModeAll, 0)
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
	start := time.Now()
	waitFrozenDB(t, s)
	t.Logf("paused after %s", time.Since(start).Round(time.Millisecond))
	start = time.Now()
	res := run(t, e, s, "p", probe, ModeAll, 0)
	t.Logf("the run failed after %s: %s", time.Since(start).Round(time.Millisecond), res.Results[0].Error)
	if res.Results[0].Kind != "error" || !strings.Contains(res.Results[0].Error, "timed out") {
		t.Fatalf("paused server = %+v", res.Results[0])
	}
	deadline := time.Now().Add(10 * time.Second)
	for s.Health() != session.HealthReconnecting && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if s.Health() != session.HealthReconnecting {
		t.Errorf("health = %s, want reconnecting", s.Health())
	}
	docker("unpause", container)
	start = time.Now()
	res = run(t, e, s, "p", probe, ModeAll, 0)
	t.Logf("first run after the pause took %s", time.Since(start).Round(time.Millisecond))
	if res.Results[0].Kind != "rows" {
		t.Fatalf("after unpause = %+v", res.Results[0])
	}
	deadline = time.Now().Add(10 * time.Second)
	for s.Health() != session.HealthOnline && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if s.Health() != session.HealthOnline {
		t.Errorf("health = %s, want online", s.Health())
	}
}

// waitFrozenDB waits until a new connection to the paused server cannot be made
// (docker pause takes effect asynchronously on Docker Desktop).
func waitFrozenDB(t *testing.T, s *session.Session) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		db, err := s.Driver.Open(t.Context(), s.Profile, "", noNetwork)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		err = pingNew(ctx, db)
		cancel()
		_ = db.Close()
		if err != nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the server did not pause")
}

var noNetwork = dialect.Network{}

func pingNew(ctx context.Context, db *sql.DB) error {
	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.PingContext(ctx)
}

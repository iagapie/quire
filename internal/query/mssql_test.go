package query

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/iagapie/quire/internal/dialect/mssql"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/session"
)

// Integration tests against a disposable SQL Server (see scripts/test-databases.sh):
//
//	QUIRE_TEST_SQLSERVER=127.0.0.1:51433 QUIRE_TEST_SQLSERVER_PASSWORD=… go test ./internal/query -run SQLServer
func openSQLServer(t *testing.T, timeout time.Duration) *session.Session {
	t.Helper()
	addr := os.Getenv("QUIRE_TEST_SQLSERVER")
	if addr == "" {
		t.Skip("QUIRE_TEST_SQLSERVER not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	p := profile.Profile{ID: "ms", Name: "ms", Driver: "sqlserver", Host: host, Port: n, User: "sa", Password: os.Getenv("QUIRE_TEST_SQLSERVER_PASSWORD"),
		Database: "master", TLS: profile.TLS{Mode: profile.TLSPrefer}, QueryTimeoutMs: int(timeout.Milliseconds())}
	p.Normalize()
	s, err := session.Open(t.Context(), "ms", p, session.Options{PingInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestSQLServerBatchesMessagesAndPaging(t *testing.T) {
	s := openSQLServer(t, 30*time.Second)
	e := New()
	t.Cleanup(e.Close)
	tab := "ms"
	setup := run(t, e, s, tab, "IF DB_ID('quire_test') IS NULL CREATE DATABASE quire_test", ModeAll, 0)
	if setup.Results[0].Kind == "error" {
		t.Fatal(setup.Results[0].Error)
	}
	req := func(script string) RunRequest {
		return RunRequest{Script: script, Mode: ModeAll, PageSize: 100, Database: "quire_test"}
	}
	res := e.Run(t.Context(), s, tab, "", req(`DROP TABLE IF EXISTS dbo.nums;
CREATE TABLE dbo.nums (id int PRIMARY KEY, label nvarchar(20) NULL);
GO
DECLARE @i int = 1;
WHILE @i <= 500 BEGIN INSERT INTO dbo.nums VALUES (@i, CONCAT('n', @i)); SET @i += 1; END;
PRINT 'filled';
SELECT COUNT(*) AS n FROM dbo.nums;
SELECT TOP 3 id FROM dbo.nums ORDER BY id DESC;
GO`))
	if len(res.Results) != 3 {
		t.Fatalf("results = %+v", res.Results)
	}
	if res.Results[0].Message != statementExecuted && res.Results[0].Kind != "message" {
		t.Errorf("first batch = %+v", res.Results[0])
	}
	if r := res.Results[1]; r.Kind != "rows" || *r.Page.Rows[0][0] != "500" || r.Title != "Result 1 (line 4)" {
		t.Errorf("count = %+v", r)
	}
	if r := res.Results[2]; r.Kind != "rows" || len(r.Page.Rows) != 3 || *r.Page.Rows[0][0] != "500" {
		t.Errorf("top 3 = %+v", r)
	}
	if !strings.Contains(strings.Join(res.Output, "\n"), "filled") {
		t.Errorf("PRINT is not in the output: %v", res.Output)
	}

	// A plain query keeps its cursor; pages come from it and, after it is gone, from
	// OFFSET … FETCH appended to the ORDER BY.
	res = e.Run(t.Context(), s, tab, "", req("SELECT id, label FROM dbo.nums ORDER BY id DESC"))
	r := res.Results[0]
	if r.Kind != "rows" || !r.Page.HasMore || r.Table == nil || len(r.Table.Key) != 1 {
		t.Fatalf("query = %+v", r)
	}
	p, err := e.FetchPage(t.Context(), s, tab, r.ID, "", 200, 10)
	if err != nil || *p.Rows[0][0] != "300" {
		t.Fatalf("page from the cursor = %v, %v", p.Rows, err)
	}
	e.mu.Lock()
	cached := e.results[r.ID]
	e.mu.Unlock()
	tb, _ := e.existing(tab)
	tb.mu.Lock()
	tb.closeCursorLocked()
	cached.rows, cached.start, cached.done = nil, 0, false
	tb.mu.Unlock()
	p, err = e.FetchPage(t.Context(), s, tab, r.ID, "", 450, 10)
	if err != nil || *p.Rows[0][0] != "50" || len(p.Rows) != 10 {
		t.Fatalf("rebuilt page = %v, %v", p.Rows, err)
	}
	n, err := e.Count(t.Context(), s, tab, r.ID, "")
	if err != nil || n != 500 {
		t.Fatalf("count = %d, %v", n, err)
	}

	// Explain shows the plan instead of running the statement.
	res = e.Run(t.Context(), s, tab, "", RunRequest{Script: "SELECT * FROM dbo.nums WHERE id = 3", Mode: ModeAll, PageSize: 100, Database: "quire_test", Explain: true})
	if r := res.Results[0]; r.Kind != "rows" || r.Columns[0].Name != "StmtText" {
		t.Fatalf("plan = %+v", r)
	}
	res = e.Run(t.Context(), s, tab, "", req("SELECT COUNT(*) FROM dbo.nums"))
	if *res.Results[0].Page.Rows[0][0] != "500" {
		t.Fatalf("SHOWPLAN left on: %+v", res.Results[0])
	}

	// A transaction opened by the script is seen through @@TRANCOUNT.
	res = e.Run(t.Context(), s, tab, "", req("BEGIN TRANSACTION;\nUPDATE dbo.nums SET label = 'x' WHERE id = 1;"))
	if !res.Tx.Open {
		t.Fatalf("transaction = %+v %+v", res.Tx, res.Results)
	}
	if _, err := e.Rollback(t.Context(), s, tab); err != nil {
		t.Fatal(err)
	}
	res = e.Run(t.Context(), s, tab, "", req("SELECT label FROM dbo.nums WHERE id = 1"))
	if *res.Results[0].Page.Rows[0][0] != "n1" || res.Tx.Open {
		t.Fatalf("after rollback = %+v %+v", res.Results[0], res.Tx)
	}

	// Edits use @p parameters.
	res = e.Run(t.Context(), s, tab, "", req("SELECT * FROM dbo.nums WHERE id <= 2"))
	ti := res.Results[0].Table
	str := func(v string) *string { return &v }
	if _, err := e.ApplyEdits(t.Context(), s, tab, EditRequest{Table: *ti, Edits: []Edit{
		{Op: "update", Key: map[string]*string{"id": str("2")}, Values: map[string]*string{"label": str("two")}},
		{Op: "delete", Key: map[string]*string{"id": str("1")}},
	}}); err != nil {
		t.Fatal(err)
	}
	res = e.Run(t.Context(), s, tab, "", req("SELECT COUNT(*) FROM dbo.nums WHERE id = 1 OR label = 'two'"))
	if *res.Results[0].Page.Rows[0][0] != "1" {
		t.Fatalf("after edits = %+v", res.Results[0])
	}

	// Stop keeps the session: SQL Server cancels with an attention, not by closing.
	e.Run(t.Context(), s, tab, "", req("CREATE TABLE #tmp (x int)"))
	done := make(chan RunResult)
	go func() {
		done <- e.Run(context.Background(), s, tab, "op-wait", RunRequest{Script: "WAITFOR DELAY '00:00:20'", Mode: ModeAll, PageSize: 100, Database: "quire_test"})
	}()
	time.Sleep(500 * time.Millisecond)
	s.Cancel("op-wait")
	if r := <-done; r.Results[0].Kind != "error" || !strings.Contains(r.Results[0].Error, "cancelled") {
		t.Fatalf("stopped wait = %+v", r.Results[0])
	}
	res = e.Run(t.Context(), s, tab, "", req("SELECT COUNT(*) FROM #tmp"))
	noReset(t, res)
	if res.Results[0].Kind != "rows" {
		t.Fatalf("temporary table after stop = %+v", res.Results[0])
	}
	run(t, e, s, "cleanup", "DROP TABLE IF EXISTS quire_test.dbo.nums", ModeAll, 0)
}

func TestSQLServerTimeoutAndRecoveryWhenServerPauses(t *testing.T) {
	container := os.Getenv("QUIRE_TEST_SQLSERVER_CONTAINER")
	if container == "" {
		t.Skip("QUIRE_TEST_SQLSERVER_CONTAINER not set")
	}
	s := openSQLServer(t, 3*time.Second)
	pauseAndRecover(t, s, container, "SELECT 1")
}

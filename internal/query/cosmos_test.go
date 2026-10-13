package query

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iagapie/quire/internal/dialect/cosmos"
	"github.com/iagapie/quire/internal/dialect/cosmos/cosmostest"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/session"
)

// Integration test against the Cosmos DB emulator (see scripts/test-databases.sh):
//
//	QUIRE_TEST_COSMOS=http://127.0.0.1:58081 go test ./internal/query -run Cosmos
func TestCosmosQueriesPagesAndPartitions(t *testing.T) {
	endpoint := os.Getenv("QUIRE_TEST_COSMOS")
	if endpoint == "" {
		t.Skip("QUIRE_TEST_COSMOS not set")
	}
	if err := cosmostest.Fill(t.Context(), endpoint); err != nil {
		t.Fatal(err)
	}
	p := profile.Profile{ID: "cos", Name: "cos", Driver: "cosmos", Host: endpoint, Password: cosmos.EmulatorKey, Database: "quire", QueryTimeoutMs: 30000}
	p.Normalize()
	s, err := session.Open(t.Context(), "cos", p, session.Options{PingInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	e := New()
	t.Cleanup(e.Close)
	req := func(script, partition string) RunRequest {
		return RunRequest{Script: script, Mode: ModeAll, PageSize: 100, Database: "quire", Partition: partition}
	}
	res := e.Run(t.Context(), s, "cos", "", req("SELECT * FROM orders o", ""))
	r := res.Results[0]
	if r.Kind != "rows" || len(r.Columns) != 1 || r.Columns[0].Kind != KindJSON || !r.Page.HasMore || r.Table != nil {
		t.Fatalf("query = %+v", r)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(*r.Page.Rows[0][0]), &doc); err != nil || doc["id"] == nil {
		t.Fatalf("document = %s, %v", *r.Page.Rows[0][0], err)
	}
	// Pages come from the open cursor (the query continuation).
	pg, err := e.FetchPage(t.Context(), s, "cos", r.ID, "", 200, 100)
	if err != nil || len(pg.Rows) != 50 || pg.HasMore {
		t.Fatalf("last page = %d rows, more %v, %v", len(pg.Rows), pg.HasMore, err)
	}
	if n, err := e.Count(t.Context(), s, "cos", r.ID, ""); err != nil || n != 250 {
		t.Fatalf("count = %d, %v", n, err)
	}
	// Lost cursor: the page is rebuilt by reading the query again and skipping rows.
	tb, _ := e.existing("cos")
	tb.mu.Lock()
	tb.closeCursorLocked()
	tb.mu.Unlock()
	e.mu.Lock()
	cached := e.results[r.ID]
	e.mu.Unlock()
	cached.rows, cached.start, cached.done = nil, 0, false
	pg, err = e.FetchPage(t.Context(), s, "cos", r.ID, "", 120, 10)
	if err != nil || len(pg.Rows) != 10 {
		t.Fatalf("rebuilt page = %d rows, %v", len(pg.Rows), err)
	}

	// A partition key limits the queries of the tab.
	res = e.Run(t.Context(), s, "cos", "", req("SELECT VALUE COUNT(1) FROM orders o", `"c3"`))
	if r := res.Results[0]; r.Kind != "rows" || *r.Page.Rows[0][0] != "50" {
		t.Fatalf("count in a partition = %+v", r)
	}
	res = e.Run(t.Context(), s, "cos", "", req("USE orders;\nSELECT VALUE c.id FROM c WHERE c.total < 3", ""))
	if r := res.Results[len(res.Results)-1]; r.Kind != "rows" || len(r.Page.Rows) != 3 || *r.Page.Rows[0][0] != `"o000"` {
		t.Fatalf("after USE = %+v", res.Results)
	}
	// Writes are refused.
	res = e.Run(t.Context(), s, "cos", "", req("DELETE FROM c", ""))
	if r := res.Results[0]; r.Kind != "error" || !strings.Contains(r.Error, "queries only") {
		t.Fatalf("delete = %+v", r)
	}
}

func TestCosmosTimeoutAndRecoveryWhenServerPauses(t *testing.T) {
	endpoint, container := os.Getenv("QUIRE_TEST_COSMOS"), os.Getenv("QUIRE_TEST_COSMOS_CONTAINER")
	if endpoint == "" || container == "" {
		t.Skip("QUIRE_TEST_COSMOS or QUIRE_TEST_COSMOS_CONTAINER not set")
	}
	if err := cosmostest.Fill(t.Context(), endpoint); err != nil {
		t.Fatal(err)
	}
	p := profile.Profile{ID: "cosp", Name: "cosp", Driver: "cosmos", Host: endpoint, Password: cosmos.EmulatorKey, Database: "quire", QueryTimeoutMs: 2000}
	p.Normalize()
	s, err := session.Open(t.Context(), "cosp", p, session.Options{PingInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	pauseAndRecover(t, s, container, "SELECT VALUE 1 FROM orders o")
}

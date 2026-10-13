package cosmos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/dialect/cosmos/cosmostest"
	"github.com/iagapie/quire/internal/profile"
)

// testProfile returns the profile of the test emulator (QUIRE_TEST_COSMOS, the
// endpoint, see scripts/test-databases.sh) after filling it (see cosmostest.Fill).
func testProfile(t *testing.T) profile.Profile {
	t.Helper()
	endpoint := os.Getenv("QUIRE_TEST_COSMOS")
	if endpoint == "" {
		t.Skip("QUIRE_TEST_COSMOS not set")
	}
	if err := cosmostest.Fill(t.Context(), endpoint); err != nil {
		t.Fatal(err)
	}
	return profile.Profile{ID: "cosmos", Name: "cosmos", Driver: "cosmos", Host: endpoint, Password: EmulatorKey, Database: "quire"}
}

func TestCatalogAndQueries(t *testing.T) {
	p := testProfile(t)
	d := &Driver{}
	db, err := d.Open(t.Context(), p, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := t.Context()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	dbs, err := d.Databases(ctx, db)
	if err != nil || !strings.Contains(strings.Join(dbs, ","), "quire") {
		t.Fatalf("databases = %v, %v", dbs, err)
	}
	objs, err := d.Objects(ctx, db, "quire", "")
	if err != nil || len(objs) != 2 || objs[0].Name != "events-2024" || objs[1].Detail != "/customer" {
		t.Fatalf("containers = %+v, %v", objs, err)
	}
	cols, err := d.Columns(ctx, db, "quire", "", "orders")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]dialect.Column{}
	for _, c := range cols {
		byName[c.Name] = c
	}
	if cols[0].Name != "id" || cols[0].PK != 1 || byName["customer"].Comment != "partition key" || byName["total"].Type != "number" ||
		byName["address.city"].Type != "string" || !byName["note"].Nullable || byName["note"].Type != "null" || !byName["_ts"].Generated ||
		cols[len(cols)-1].Name != "_ts" {
		t.Errorf("columns = %+v", cols)
	}
	if ixs, err := d.Indexes(ctx, db, "quire", "", "orders"); err != nil || len(ixs) == 0 {
		t.Errorf("indexes = %+v, %v", ixs, err)
	}
	if cons, err := d.Constraints(ctx, db, "quire", "", "orders"); err != nil || cons[0].Definition != "PARTITION KEY (/customer)" {
		t.Errorf("constraints = %+v, %v", cons, err)
	}
	if ddl, err := d.DDL(ctx, db, "quire", "", dialect.Object{Name: "orders", Kind: "container"}); err != nil || !strings.Contains(ddl, `"partitionKey"`) {
		t.Errorf("ddl = %s, %v", ddl, err)
	}

	count := func(q string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, d.Count(q)).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", q, err)
		}
		return n
	}
	// The container comes from FROM, also for names that are not identifiers.
	if n := count("SELECT * FROM orders o WHERE o.total > 100 ORDER BY o.total"); n != 150 {
		t.Errorf("count = %d", n)
	}
	if n := count(`SELECT * FROM ["events-2024"] e`); n != 3 {
		t.Errorf("events count = %d", n)
	}
	// Several containers and no container in FROM: say how to choose one.
	var ignored []byte
	if err := db.QueryRowContext(ctx, "SELECT * FROM c").Scan(&ignored); err == nil || !strings.Contains(err.Error(), "USE") {
		t.Errorf("ambiguous container = %v", err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "USE orders"); err != nil {
		t.Fatal(err)
	}
	n, err := readC1(ctx, conn)
	if err != nil || n != 50 {
		t.Errorf("rows of c1 = %d, %v", n, err)
	}

	// ORDER BY across partitions is refused with an explanation; with a partition key
	// it runs.
	err = conn.QueryRowContext(ctx, "SELECT * FROM c ORDER BY c.total DESC").Scan(&ignored)
	if err == nil {
		t.Log("the emulator served ORDER BY across partitions")
	} else if !errors.Is(err, errCrossPartition) {
		t.Errorf("ORDER BY across partitions: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `SET PARTITION KEY "c2"`); err != nil {
		t.Fatal(err)
	}
	var top []byte
	if err := conn.QueryRowContext(ctx, "SELECT TOP 1 VALUE c.total FROM c ORDER BY c.total DESC").Scan(&top); err != nil || string(top) != "247.5" {
		t.Errorf("top total in c2 = %s, %v", top, err)
	}
	if _, err := conn.ExecContext(ctx, "SET PARTITION KEY NONE"); err != nil {
		t.Fatal(err)
	}
	// Across partitions TOP is applied by Quire: the first n documents.
	rows, err := conn.QueryContext(ctx, "SELECT TOP 7 c.id, c.total FROM c WHERE c.total > 0")
	if err != nil {
		t.Fatalf("TOP across partitions: %v", err)
	}
	defer func() { _ = rows.Close() }()
	got := 0
	for rows.Next() {
		got++
	}
	if err := rows.Err(); err != nil || got != 7 {
		t.Errorf("TOP 7 across partitions = %d rows, %v", got, err)
	}
	if n := count("SELECT TOP (20) * FROM orders o"); n != 20 {
		t.Errorf("count of TOP 20 = %d", n)
	}
	// The query opening a container runs and names its properties.
	open := d.OpenTable("orders", cols, 1000)
	if !strings.HasPrefix(open, "SELECT TOP 1000 c.id, ") || strings.Contains(open, "_ts") || !strings.HasSuffix(open, "\nFROM orders c") {
		t.Errorf("open query = %q", open)
	}
	if n := count(open); n != 250 { // every document: fewer than 1000
		t.Errorf("rows of the open query = %d", n)
	}
	if _, err := conn.ExecContext(ctx, "DELETE FROM c"); err == nil {
		t.Error("a write was accepted")
	}
	if _, err := conn.ExecContext(ctx, "USE nothing"); err == nil || !strings.Contains(err.Error(), "orders") {
		t.Errorf("USE of a missing container = %v", err)
	}
}

func TestHelpers(t *testing.T) {
	d := &Driver{}
	if d.Quote("orders") != "orders" || d.Quote("events-2024") != `["events-2024"]` || d.Quote("value") != `["value"]` {
		t.Error("quote")
	}
	if got := countable("SELECT c.id, c.total FROM orders c WHERE c.total > 1 ORDER BY c.total OFFSET 10 LIMIT 5"); got != "SELECT VALUE 1 FROM orders c WHERE c.total > 1" {
		t.Errorf("countable = %q", got)
	}
	if got := countable("SELECT DISTINCT VALUE c.customer FROM c"); got != "SELECT DISTINCT VALUE c.customer FROM c" {
		t.Errorf("countable distinct = %q", got)
	}
	for in, want := range map[string]azcosmos.PartitionKey{
		`"abc"`: azcosmos.NewPartitionKeyString("abc"), `12`: azcosmos.NewPartitionKeyNumber(12), `true`: azcosmos.NewPartitionKeyBool(true),
		`["a", 1]`: azcosmos.NewPartitionKey().AppendString("a").AppendNumber(1), `abc`: azcosmos.NewPartitionKeyString("abc"),
		`'it''s'`: azcosmos.NewPartitionKeyString("it's"),
	} {
		pk, err := ParsePartitionKey(in)
		if err != nil || !reflect.DeepEqual(pk, want) {
			t.Errorf("partition key %s = %v, %v", in, pk, err)
		}
	}
	endpoint, key, err := parseConnectionString("AccountEndpoint=https://a.documents.azure.com:443/;AccountKey=k==;")
	if err != nil || endpoint != "https://a.documents.azure.com:443/" || key != "k==" {
		t.Errorf("connection string = %s %s %v", endpoint, key, err)
	}
	cols := inferColumns([][]byte{[]byte(`{"id":"1","b":1,"a":{"y":true,"x":null},"_ts":5}`), []byte(`{"id":"2","b":"x"}`)}, map[string]bool{"b": true})
	var names []string
	for _, c := range cols {
		names = append(names, c.Name+":"+c.Type)
	}
	if got := strings.Join(names, ","); got != "id:string,b:number | string,a:object,a.x:null,a.y:boolean,_ts:number" {
		t.Errorf("inferred = %s", got)
	}
	if q, err := d.GenerateSQL("orders", []dialect.Column{{Name: "id"}, {Name: "first name"}, {Name: "a.b"}, {Name: "_ts"}}, "select"); err != nil ||
		q != "SELECT c.id, c[\"first name\"]\nFROM orders c\nWHERE " {
		t.Errorf("generate = %q, %v", q, err)
	}
}

func TestFriendlyErrors(t *testing.T) {
	respond := func(code int, body string) error {
		return runtime.NewResponseError(&http.Response{StatusCode: code, Status: http.StatusText(code), Header: http.Header{},
			Body: io.NopCloser(strings.NewReader(body)), Request: &http.Request{Method: http.MethodPost, URL: &url.URL{Scheme: "https", Host: "a"}}})
	}
	cross := friendly(respond(400, `{"code":"BadRequest","message":"The provided cross partition query can not be directly served by the gateway. This is a first chance (internal) exception..."}`))
	if !errors.Is(cross, errCrossPartition) {
		t.Errorf("cross partition = %v", cross)
	}
	syntax := friendly(respond(400, `{"code":"BadRequest","message":"Message: {\"Errors\":[\"Syntax error, incorrect syntax near 'FORM'.\"]}\r\nActivityId: 1"}`))
	if syntax.Error() != "Syntax error, incorrect syntax near 'FORM'." {
		t.Errorf("syntax = %q", syntax)
	}
	if d := (&Driver{}); !d.Transient(respond(429, `{}`)) || d.Transient(respond(400, `{}`)) {
		t.Error("transient")
	}
	if auth := friendly(respond(401, `{}`)); !strings.Contains(auth.Error(), "account key") {
		t.Errorf("auth = %v", auth)
	}
}

// readC1 reads the projection of customer c1's orders and counts the documents.
func readC1(ctx context.Context, conn *sql.Conn) (int, error) {
	rows, err := conn.QueryContext(ctx, "SELECT c.id, c.address.zip FROM c WHERE c.customer = 'c1'")
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	if types, _ := rows.ColumnTypes(); len(types) != 1 || types[0].DatabaseTypeName() != "JSON" {
		return 0, fmt.Errorf("column types = %v", types)
	}
	n := 0
	for rows.Next() {
		var doc []byte
		if err := rows.Scan(&doc); err != nil {
			return n, err
		}
		if !strings.Contains(string(doc), `"id":"o`) {
			return n, fmt.Errorf("document = %s", doc)
		}
		n++
	}
	return n, rows.Err()
}

func TestClientTop(t *testing.T) {
	for q, want := range map[string]struct {
		text string
		n    int64
	}{
		"SELECT TOP 1000 c.id FROM orders c":               {"SELECT c.id FROM orders c", 1000},
		"select top (5) * from c where c.a = 1":            {"select * from c where c.a = 1", 5},
		"SELECT TOP 3 VALUE c.id FROM c":                   {"SELECT VALUE c.id FROM c", 3},
		"SELECT TOP 3 * FROM c ORDER BY c.ts DESC":         {"SELECT TOP 3 * FROM c ORDER BY c.ts DESC", -1},
		"SELECT TOP 3 DISTINCT c.a FROM c":                 {"SELECT TOP 3 DISTINCT c.a FROM c", -1},
		"SELECT TOP @n * FROM c":                           {"SELECT TOP @n * FROM c", -1},
		"SELECT * FROM c":                                  {"SELECT * FROM c", -1},
		"SELECT (SELECT TOP 1 x FROM x IN c.items) FROM c": {"SELECT (SELECT TOP 1 x FROM x IN c.items) FROM c", -1},
	} {
		text, n := clientTop(q)
		if text != want.text || n != want.n {
			t.Errorf("clientTop(%q) = %q, %d", q, text, n)
		}
	}
	d := &Driver{}
	cols := []dialect.Column{{Name: "id"}, {Name: "first name"}, {Name: "address.city"}, {Name: "_ts"}}
	if got := d.OpenTable("orders", cols, 1000); got != "SELECT TOP 1000 c.id, c[\"first name\"]\nFROM orders c" {
		t.Errorf("open = %q", got)
	}
	if got := d.OpenTable(`["events-2024"]`, nil, 10); got != "SELECT TOP 10 *\nFROM [\"events-2024\"] c" {
		t.Errorf("open without columns = %q", got)
	}
}

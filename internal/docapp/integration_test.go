package docapp

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/shell"
)

// Run with a disposable server whose root user is root/rootpw, e.g.:
//
//	QUIRE_TEST_MONGODB=127.0.0.1:57017 go test ./internal/docapp -run Integration -v
//
// The fixture recreates database "shop" (collection "orders", view "orders_view")
// and user reader/readerpw (role read@shop, defined in "shop").
func newTestConns(t *testing.T) (*testConns, *Shell) {
	t.Helper()
	host := os.Getenv("QUIRE_TEST_MONGODB")
	if host == "" {
		t.Skip("QUIRE_TEST_MONGODB not set")
	}
	seedOnce.Do(func() { errSeed = seed(host) })
	if errSeed != nil {
		t.Fatalf("seed test data: %v", errSeed)
	}
	store, err := profile.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := newConns(store)
	t.Cleanup(func() { _ = c.ServiceShutdown() })
	return c, c.sh
}

var (
	seedOnce sync.Once
	errSeed  error
)

func seed(host string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().SetHosts([]string{host}).SetDirect(true).
		SetAuth(options.Credential{Username: "root", Password: "rootpw"}))
	if err != nil {
		return err
	}
	defer func() { _ = client.Disconnect(context.Background()) }()
	db := client.Database("shop")
	if err := db.Drop(ctx); err != nil {
		return err
	}
	_, err = db.Collection("orders").InsertMany(ctx, []any{
		bson.D{{Key: "sku", Value: "AXIS2JGS-903068_A"}, {Key: "qty", Value: int32(2)}, {Key: "price", Value: 9.5}, {Key: "at", Value: bson.NewDateTimeFromTime(time.Now())}},
		bson.D{{Key: "sku", Value: "B-1"}, {Key: "qty", Value: int32(5)}, {Key: "tags", Value: bson.A{"x", "y"}}, {Key: "nested", Value: bson.D{{Key: "a", Value: bson.D{{Key: "b", Value: int32(1)}}}}}},
		bson.D{{Key: "sku", Value: "AXIS2JGS-903068_B"}, {Key: "qty", Value: int64(7)}},
	})
	if err != nil {
		return err
	}
	if _, err := db.Collection("orders").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "sku", Value: 1}}}); err != nil {
		return err
	}
	if err := db.CreateView(ctx, "orders_view", "orders", bson.A{bson.D{{Key: "$match", Value: bson.D{{Key: "qty", Value: bson.D{{Key: "$gt", Value: 1}}}}}}}); err != nil {
		return err
	}
	_ = db.RunCommand(ctx, bson.D{{Key: "dropUser", Value: "reader"}}).Err()
	return db.RunCommand(ctx, bson.D{
		{Key: "createUser", Value: "reader"},
		{Key: "pwd", Value: "readerpw"},
		{Key: "roles", Value: bson.A{bson.D{{Key: "role", Value: "read"}, {Key: "db", Value: "shop"}}}},
	}).Err()
}

func save(t *testing.T, c *testConns, p profile.Profile) string {
	t.Helper()
	host := os.Getenv("QUIRE_TEST_MONGODB")
	p.Driver, p.Host = "mongodb", host
	if p.Options == nil {
		p.Options = map[string]string{}
	}
	p.Options["directConnection"] = "true"
	p.Normalize()
	saved, err := c.store.Save(p, false)
	if err != nil {
		t.Fatal(err)
	}
	return saved.ID
}

func TestIntegrationReaderWithoutAdmin(t *testing.T) {
	c, sh := newTestConns(t)
	id := save(t, c, profile.Profile{Name: "reader", User: "reader", Password: "readerpw", Options: map[string]string{"authSource": "shop"}, Database: "shop"})
	ctx := t.Context()
	if _, err := c.Connect(ctx, id); err != nil {
		t.Fatal(err)
	}
	dbs, err := c.ListDatabases(ctx, id)
	if err != nil || !slices.Contains(dbs, "shop") {
		t.Fatalf("dbs = %v, %v", dbs, err)
	}
	colls, err := c.ListCollections(ctx, id, "shop")
	if err != nil || len(colls) < 2 {
		t.Fatalf("collections = %v, %v", colls, err)
	}

	res, err := sh.Run(ctx, id, "shop", "op1", shell.RunRequest{
		Script: "db.getCollection(\"orders\").find({sku: /AXIS2JGS-903068_/}).sort({sku: 1})\n\ndb.orders.find({}).size()\n\nprint(db.orders.findOne({sku: 'B-1'}).nested.a.b)",
		Mode:   shell.ModeAll, PageSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 2 {
		t.Fatalf("results = %+v", res.Results)
	}
	find := res.Results[0]
	if find.Kind != "find" || len(find.Page.Docs) != 1 || !find.Page.HasMore || !strings.Contains(find.Page.Docs[0], "903068_A") {
		t.Fatalf("find = %+v", find)
	}
	if res.Results[1].Value != `{"$numberInt":"3"}` {
		t.Errorf("size = %s", res.Results[1].Value)
	}
	if len(res.Output) != 1 || res.Output[0] != "1" {
		t.Errorf("output = %v", res.Output)
	}

	// Re-query with an edited filter and next page, without re-running the script.
	q := *find.Query
	page, err := sh.FetchPage(ctx, id, "op2", q, 1, 1)
	if err != nil || len(page.Docs) != 1 || page.HasMore || !strings.Contains(page.Docs[0], "903068_B") {
		t.Fatalf("page 2 = %+v, %v", page, err)
	}
	q.Filter = `{ qty: { $gte: 5 } }`
	n, err := sh.Count(ctx, id, "op3", q)
	if err != nil || n != 2 {
		t.Fatalf("count = %d, %v", n, err)
	}

	// The server rejects writes for this user; the error surfaces as a result.
	res, _ = sh.Run(ctx, id, "shop", "op4", shell.RunRequest{Script: `db.orders.insertOne({x: 1})`, Mode: shell.ModeAll})
	if len(res.Results) != 1 || res.Results[0].Kind != "error" || !strings.Contains(strings.ToLower(res.Results[0].Error), "not authorized") {
		t.Fatalf("write as reader = %+v", res.Results)
	}
}

func TestIntegrationEditAndReadOnly(t *testing.T) {
	c, sh := newTestConns(t)
	ctx := t.Context()
	rw := save(t, c, profile.Profile{Name: "root", User: "root", Password: "rootpw", Database: "shop"})
	if _, err := c.Connect(ctx, rw); err != nil {
		t.Fatal(err)
	}
	idJSON, err := sh.InsertDocument(ctx, rw, "shop", "scratch", `{ name: "a", n: NumberLong(1), d: Double(2) }`)
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.SetField(ctx, rw, "shop", "scratch", idJSON, "nested.v", `ISODate("2026-01-02T03:04:05Z")`); err != nil {
		t.Fatal(err)
	}
	doc, err := sh.GetDocument(ctx, rw, "shop", "scratch", idJSON)
	if err != nil || !strings.Contains(doc, `"$numberLong":"1"`) || !strings.Contains(doc, `"$numberDouble":"2.0"`) || !strings.Contains(doc, `"$date"`) {
		t.Fatalf("doc = %s, %v", doc, err)
	}
	text, err := sh.FormatDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := sh.ReplaceDocument(ctx, rw, "shop", "scratch", idJSON, strings.Replace(text, `"a"`, `"b"`, 1)); err != nil {
		t.Fatal(err)
	}
	doc2, _ := sh.GetDocument(ctx, rw, "shop", "scratch", idJSON)
	if !strings.Contains(doc2, `"b"`) || !strings.Contains(doc2, `"$numberDouble":"2.0"`) {
		t.Fatalf("after replace = %s", doc2)
	}
	if n, err := sh.DeleteDocuments(ctx, rw, "shop", "scratch", []string{idJSON}); err != nil || n != 1 {
		t.Fatalf("delete = %d, %v", n, err)
	}

	ro := save(t, c, profile.Profile{Name: "ro", User: "root", Password: "rootpw", ReadOnly: true})
	if _, err := c.Connect(ctx, ro); err != nil {
		t.Fatal(err)
	}
	res, _ := sh.Run(ctx, ro, "shop", "op", shell.RunRequest{Script: `db.scratch.deleteMany({})`, Mode: shell.ModeAll})
	if len(res.Results) != 1 || !strings.Contains(res.Results[0].Error, "read-only") {
		t.Fatalf("read-only = %+v", res.Results)
	}
	if _, err := sh.InsertDocument(ctx, ro, "shop", "scratch", `{}`); err == nil {
		t.Fatal("insert on read-only connection succeeded")
	}
}

func TestIntegrationCancel(t *testing.T) {
	c, sh := newTestConns(t)
	ctx := t.Context()
	id := save(t, c, profile.Profile{Name: "root", User: "root", Password: "rootpw"})
	if _, err := c.Connect(ctx, id); err != nil {
		t.Fatal(err)
	}
	done := make(chan shell.RunResult)
	go func() {
		res, _ := sh.Run(ctx, id, "shop", "slow", shell.RunRequest{Script: `sleep(20000); db.orders.find({})`, Mode: shell.ModeAll})
		done <- res
	}()
	for !sh.Cancel(id, "slow") {
	}
	res := <-done
	if len(res.Results) != 1 || res.Results[0].Error != "operation cancelled" {
		t.Fatalf("cancel = %+v", res.Results)
	}
}

func TestIntegrationConnectionLifecycle(t *testing.T) {
	c, sh := newTestConns(t)
	ctx := t.Context()
	id := save(t, c, profile.Profile{Name: "root", User: "root", Password: "rootpw", Database: "shop"})

	res, err := c.TestConnection(ctx, profile.Profile{ID: id, Name: "root", Driver: "mongodb", Host: os.Getenv("QUIRE_TEST_MONGODB"), Options: map[string]string{"directConnection": "true"}, User: "root"})
	if err != nil || res.ServerVersion == "" || res.Databases == 0 {
		t.Fatalf("TestConnection (password from the store) = %+v, %v", res, err)
	}
	if _, err := c.TestConnection(ctx, profile.Profile{Name: "x"}); err == nil {
		t.Error("expected a validation error")
	}

	info, err := c.Connect(ctx, id)
	if err != nil || info.Health != "online" || info.ServerVersion == "" || info.Database != "shop" {
		t.Fatalf("Connect = %+v, %v", info, err)
	}
	again, err := c.Connect(ctx, id)
	if err != nil || again.ID != info.ID || len(c.Sessions()) != 1 {
		t.Errorf("second Connect must reuse the session: %+v, %v, sessions=%d", again, err, len(c.Sessions()))
	}

	idx, err := c.ListIndexes(ctx, id, "shop", "orders")
	if err != nil || len(idx) != 2 {
		t.Errorf("indexes = %+v, %v", idx, err)
	}

	// FetchPage applies an edited filter and paging; Count matches it.
	q := shell.Query{Kind: "find", Database: "shop", Collection: "orders", Filter: `{ qty: { $gte: 2 } }`, Sort: `{ qty: 1 }`}
	page, err := sh.FetchPage(ctx, id, "p", q, 0, 2)
	if err != nil || len(page.Docs) != 2 || !page.HasMore {
		t.Errorf("page = %+v, %v", page, err)
	}
	if n, err := sh.Count(ctx, id, "c", q); err != nil || n != 3 {
		t.Errorf("count = %d, %v", n, err)
	}
	if _, err := sh.FetchPage(ctx, id, "p", shell.Query{Kind: "find", Database: "shop", Collection: "orders", Filter: `{$nope: 1}`}, 0, 0); err == nil {
		t.Error("expected a server error for an invalid filter")
	}

	// Document helpers on a scratch collection.
	newID, err := sh.InsertDocument(ctx, id, "shop", "lifecycle", `{ _id: "k1", a: 1 }`)
	if err != nil || newID != `"k1"` {
		t.Fatalf("insert = %s, %v", newID, err)
	}
	if err := sh.UnsetField(ctx, id, "shop", "lifecycle", newID, "a"); err != nil {
		t.Fatal(err)
	}
	if doc, _ := sh.GetDocument(ctx, id, "shop", "lifecycle", newID); strings.Contains(doc, `"a"`) {
		t.Errorf("field not removed: %s", doc)
	}
	if err := sh.SetField(ctx, id, "shop", "lifecycle", `"missing"`, "a", "1"); err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Errorf("update of a deleted document: %v", err)
	}
	if _, err := sh.GetDocument(ctx, id, "shop", "lifecycle", `"missing"`); err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Errorf("get of a deleted document: %v", err)
	}
	if err := sh.ReplaceDocument(ctx, id, "shop", "lifecycle", `"missing"`, `{}`); err == nil {
		t.Error("replace of a deleted document must fail")
	}
	if _, err := sh.DeleteDocuments(ctx, id, "shop", "lifecycle", []string{newID}); err != nil {
		t.Fatal(err)
	}

	// Indexes can be dropped from the tree, except on read-only connections.
	if err := c.DropIndex(ctx, id, "shop", "orders", "sku_1"); err != nil {
		t.Fatal(err)
	}
	ro := save(t, c, profile.Profile{Name: "ro", User: "root", Password: "rootpw", ReadOnly: true})
	if _, err := c.Connect(ctx, ro); err != nil {
		t.Fatal(err)
	}
	if err := c.DropIndex(ctx, ro, "shop", "orders", "_id_"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("DropIndex on read-only: %v", err)
	}

	c.Disconnect(id)
	if _, err := c.ListDatabases(ctx, id); !errors.Is(err, ErrNotConnected) {
		t.Errorf("after disconnect: %v", err)
	}
	if err := c.DeleteProfile(ro); err != nil || len(c.Sessions()) != 0 {
		t.Errorf("DeleteProfile must close the session: %v, %d", err, len(c.Sessions()))
	}
}

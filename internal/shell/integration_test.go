package shell

import (
	"os"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/iagapie/quire/internal/docsession"
	"github.com/iagapie/quire/internal/profile"
)

// QUIRE_TEST_MONGODB=127.0.0.1:57017 go test ./internal/shell (root user root/rootpw).
func testSession(t *testing.T, readOnly bool) *docsession.Session {
	t.Helper()
	host := os.Getenv("QUIRE_TEST_MONGODB")
	if host == "" {
		t.Skip("QUIRE_TEST_MONGODB not set")
	}
	p := profile.Profile{Driver: "mongodb", Host: host, Options: map[string]string{"directConnection": "true"}, User: "root", Password: "rootpw", ReadOnly: readOnly}
	p.Normalize()
	s, err := docsession.Open(t.Context(), "shell-test", p, docsession.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// run executes a script and fails the test on any statement error.
func run(t *testing.T, s *docsession.Session, script string) RunResult {
	t.Helper()
	res := Run(t.Context(), Env{Session: s, Database: "shell_test"}, RunRequest{Script: script, Mode: ModeAll, PageSize: 2})
	for _, r := range res.Results {
		if r.Kind == "error" {
			t.Fatalf("line %d (%s): %s", r.Line, r.Statement, r.Error)
		}
	}
	return res
}

func TestIntegrationShellAPI(t *testing.T) {
	s := testSession(t, false)
	res := run(t, s, `
db.dropDatabase()
db.items.insertMany([
  {_id: 1, sku: "a", qty: 5, tags: ["x"], grades: [1, 9]},
  {_id: 2, sku: "b", qty: 1},
  {_id: 3, sku: "c", qty: 10},
])
const ins = db.items.insertOne({sku: "d", qty: 0})
print("insertedId", typeof ins.insertedId.str)

print("find", db.items.find({qty: {$gt: 0}}).sort({qty: -1}).limit(2).toArray().map(d => d.sku).join(","))
print("skip", db.items.find({}, {sku: 1, _id: 0}).sort({sku: 1}).skip(3).toArray().map(d => JSON.stringify(d)).join())
print("projection", Object.keys(db.items.find({_id: 1}).projection({sku: 1}).next()).join(","))
print("counts", db.items.countDocuments({qty: {$gte: 1}}), db.items.count(), db.items.estimatedDocumentCount(),
  db.items.find().count(), db.items.find().limit(2).size(), db.items.find().itcount(), db.items.countDocuments({}, {limit: 2}))
const cur = db.items.find({_id: {$lte: 2}}).sort({_id: 1})
print("iterate", cur.hasNext(), cur.next()._id, cur.next()._id, cur.hasNext(), cur.next())
let seen = []
db.items.find({_id: {$in: [1, 3]}}).forEach(d => seen.push(d._id))
print("forEach", seen.join(","))
print("findOne", db.items.findOne({sku: "c"}).qty, db.items.findOne({sku: "zzz"}), db.items.findOne({_id: 1}, {sku: 1}).sku)
print("distinct", db.items.distinct("sku", {qty: {$gt: 1}}).sort().join(","))
print("agg", db.items.aggregate([{$group: {_id: null, total: {$sum: "$qty"}}}]).toArray()[0].total,
  db.items.aggregate({$match: {qty: 1}}, {$project: {_id: 1}}).toArray().length)

const u1 = db.items.updateOne({_id: 1}, {$inc: {qty: 1}})
const u2 = db.items.updateMany({qty: {$lt: 2}}, {$set: {low: true}})
const u3 = db.items.updateOne({_id: 9}, {$set: {sku: "z"}}, {upsert: true})
const u4 = db.items.updateOne({_id: 1}, {$set: {"grades.$[g]": 0}}, {arrayFilters: [{g: {$gt: 5}}]})
print("update", u1.matchedCount, u1.modifiedCount, u2.modifiedCount, u3.upsertedCount, u3.insertedId, db.items.findOne({_id: 1}).grades.join(","))
print("replace", db.items.replaceOne({_id: 2}, {sku: "b2"}).modifiedCount, db.items.findOne({_id: 2}).qty)
print("fau", db.items.findOneAndUpdate({_id: 3}, {$set: {qty: 11}}, {returnDocument: "after"}).qty,
  db.items.findOneAndUpdate({_id: 3}, {$set: {qty: 12}}).qty,
  db.items.findOneAndReplace({_id: 3}, {sku: "c3"}, {returnNewDocument: true}).sku,
  db.items.findOneAndDelete({_id: 9}).sku, db.items.findOneAndDelete({_id: 9}))
print("delete", db.items.deleteOne({_id: 2}).deletedCount, db.items.deleteMany({low: true}).deletedCount,
  db.items.remove({_id: 3}, {justOne: true}).deletedCount)

print("index", db.items.createIndex({sku: 1, qty: -1}), db.items.createIndex({qty: 1}, {name: "q", unique: false}),
  db.items.getIndexes().map(i => i.name).join(","))
db.items.dropIndex("q")
print("indexes", db.items.getIndexes().length)
db.items.dropIndexes()
print("indexes", db.items.getIndexes().length)

db.createCollection("other")
db.createView("items_view", "items", [{$match: {}}])
print("names", db.getCollectionNames().join(","), db.getCollectionInfos({name: "items_view"})[0].type)
print("cmd", db.runCommand({ping: 1}).ok, db.runCommand("ping").ok, db.adminCommand({ping: 1}).ok, db.stats().db, typeof db.version())
print("explain", typeof db.items.find({sku: "a"}).explain().queryPlanner, typeof db.items.aggregate([]).explain())
print("names2", db.getSiblingDB("shell_test").getName(), db.items.getFullName(), db.getCollection("a.b").getName(), db.system.profile.getName())
db.other.renameCollection("renamed")
print("renamed", db.getCollectionNames().includes("renamed"), db.renamed.drop())

db.items.find({}).sort({_id: 1})
db.items.aggregate([{$sort: {_id: 1}}])
db.items.findOne({_id: 1})
`)

	want := map[string]string{
		"insertedId": "string",
		"find":       "c,a",
		"skip":       `{"sku":"d"}`,
		"projection": "_id,sku",
		"counts":     "3 4 4 4 2 4 2",
		"iterate":    "true 1 2 false null",
		"forEach":    "1,3",
		"findOne":    "10 null a",
		"distinct":   "a,c",
		"agg":        "16 1",
		"update":     "1 1 2 1 9 1,0",
		"replace":    "1 undefined",
		"fau":        "11 11 c3 z null",
		"delete":     "1 1 1",
		"index":      "sku_1_qty_-1 q _id_,sku_1_qty_-1,q",
		"explain":    "object object",
		"names2":     "shell_test shell_test.items a.b system.profile",
		"renamed":    "true true",
	}
	got := map[string][]string{}
	for _, line := range res.Output {
		k, v, _ := strings.Cut(line, " ")
		got[k] = append(got[k], v)
	}
	for k, w := range want {
		if len(got[k]) == 0 || got[k][0] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	if g := got["indexes"]; len(g) != 2 || g[0] != "2" || g[1] != "1" {
		t.Errorf("indexes = %q", g)
	}
	if g := got["names"]; len(g) != 1 || !strings.Contains(g[0], "items,items_view,other") || !strings.HasSuffix(g[0], " view") {
		t.Errorf("names = %q", g)
	}
	if g := got["cmd"]; len(g) != 1 || g[0] != "1 1 1 shell_test string" {
		t.Errorf("cmd = %q", g)
	}

	// The last three statements produce results: two pageable cursors and a value.
	n := len(res.Results)
	if n < 3 {
		t.Fatalf("results = %+v", res.Results)
	}
	find, agg, one := res.Results[n-3], res.Results[n-2], res.Results[n-1]
	if find.Kind != "find" || find.Query.Collection != "items" || find.Query.Sort != "{ _id: 1 }" || len(find.Page.Docs) != 1 || find.Page.HasMore {
		t.Errorf("find result = %+v %+v", find, find.Page)
	}
	if agg.Kind != "aggregate" || agg.Page == nil || len(agg.Page.Docs) != 1 {
		t.Errorf("aggregate result = %+v", agg)
	}
	if one.Kind != "value" || !strings.Contains(one.Value, `"sku":"a"`) {
		t.Errorf("value result = %+v", one)
	}
}

func TestIntegrationPaging(t *testing.T) {
	s := testSession(t, false)
	run(t, s, `db.paging.drop(); db.paging.insertMany(Array.from({length: 7}, (_, i) => ({_id: i})))`)
	q := QuerySpec{Kind: "find", Database: "shell_test", Collection: "paging", Sort: bson.D{{Key: "_id", Value: 1}}}

	type step struct {
		skip, limit, offset, size int64
		docs                      int
		more                      bool
	}
	for _, st := range []step{
		{0, 0, 0, 3, 3, true},
		{0, 0, 3, 3, 3, true},
		{0, 0, 6, 3, 1, false},
		{0, 5, 3, 3, 2, false}, // the limit ends the last page early
		{0, 6, 3, 3, 3, false}, // exactly at the limit
		{2, 0, 0, 10, 5, false},
		{0, 2, 4, 3, 0, false}, // past the limit
	} {
		q.Skip, q.Limit = st.skip, st.limit
		page, err := FetchPage(t.Context(), s.Client, q, st.offset, st.size)
		if err != nil || len(page.Docs) != st.docs || page.HasMore != st.more || page.Offset != st.offset {
			t.Errorf("%+v: docs=%d more=%v offset=%d err=%v", st, len(page.Docs), page.HasMore, page.Offset, err)
		}
	}

	agg := QuerySpec{Kind: "aggregate", Database: "shell_test", Collection: "paging", Pipeline: bson.A{bson.D{{Key: "$sort", Value: bson.D{{Key: "_id", Value: -1}}}}}}
	page, err := FetchPage(t.Context(), s.Client, agg, 5, 3)
	if err != nil || len(page.Docs) != 2 || page.HasMore || !strings.Contains(page.Docs[0], `"$numberInt":"1"`) {
		t.Errorf("aggregate page = %+v, %v", page, err)
	}

	for _, c := range []struct {
		q    QuerySpec
		want int64
	}{
		{QuerySpec{Kind: "find", Database: "shell_test", Collection: "paging"}, 7},
		{QuerySpec{Kind: "find", Database: "shell_test", Collection: "paging", Filter: bson.D{{Key: "_id", Value: bson.D{{Key: "$gte", Value: 3}}}}}, 4},
		{QuerySpec{Kind: "find", Database: "shell_test", Collection: "paging", Skip: 2, Limit: 3}, 3},
		{agg, 7},
		{QuerySpec{Kind: "aggregate", Database: "shell_test", Collection: "paging", Skip: 5, Limit: 10}, 2},
		{QuerySpec{Kind: "aggregate", Database: "shell_test", Collection: "paging", Pipeline: bson.A{bson.D{{Key: "$match", Value: bson.D{{Key: "_id", Value: -1}}}}}}, 0},
	} {
		if n, err := Count(t.Context(), s.Client, c.q); err != nil || n != c.want {
			t.Errorf("Count(%+v) = %d, %v; want %d", c.q, n, err, c.want)
		}
	}
}

func TestIntegrationReadOnly(t *testing.T) {
	rw := testSession(t, false)
	run(t, rw, `db.ro.drop(); db.ro.insertOne({_id: 1})`)

	ro := testSession(t, true)
	for _, stmt := range []string{
		`db.ro.insertOne({})`, `db.ro.insertMany([{}])`, `db.ro.updateOne({}, {$set: {a: 1}})`, `db.ro.updateMany({}, {$set: {a: 1}})`,
		`db.ro.replaceOne({}, {})`, `db.ro.deleteOne({})`, `db.ro.deleteMany({})`, `db.ro.remove({})`,
		`db.ro.findOneAndUpdate({}, {$set: {a: 1}})`, `db.ro.findOneAndReplace({}, {})`, `db.ro.findOneAndDelete({})`,
		`db.ro.createIndex({a: 1})`, `db.ro.dropIndex("a_1")`, `db.ro.dropIndexes()`, `db.ro.drop()`, `db.ro.renameCollection("x")`,
		`db.createCollection("x")`, `db.createView("v", "ro", [])`, `db.dropDatabase()`, `db.runCommand({insert: "ro", documents: [{}]})`,
	} {
		res := Run(t.Context(), Env{Session: ro, Database: "shell_test"}, RunRequest{Script: stmt, Mode: ModeAll})
		if len(res.Results) != 1 || !strings.Contains(res.Results[0].Error, "read-only") {
			t.Errorf("%s: %+v", stmt, res.Results)
		}
	}
	// Reads still work.
	res := run(t, ro, `print(db.ro.countDocuments({}))`)
	if len(res.Output) != 1 || res.Output[0] != "1" {
		t.Errorf("read on read-only connection = %q", res.Output)
	}
}

func TestIntegrationServerErrors(t *testing.T) {
	s := testSession(t, false)
	res := Run(t.Context(), Env{Session: s, Database: "shell_test"}, RunRequest{Script: `db.runCommand({noSuchCommand: 1})`, Mode: ModeAll})
	if len(res.Results) != 1 || res.Results[0].Kind != "error" || !strings.Contains(res.Results[0].Error, "noSuchCommand") {
		t.Errorf("results = %+v", res.Results)
	}
	res = Run(t.Context(), Env{Session: s, Database: "shell_test"}, RunRequest{Script: `db.items.find({$bad: 1})`, Mode: ModeAll})
	if len(res.Results) != 1 || res.Results[0].Kind != "error" || res.Results[0].Query == nil {
		t.Errorf("bad filter results = %+v", res.Results)
	}
}

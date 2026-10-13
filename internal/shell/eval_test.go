package shell

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestEvalConstructors(t *testing.T) {
	tests := []struct {
		expr string
		want string // canonical Extended JSON
	}{
		{`NumberInt(5)`, `{"$numberInt":"5"}`},
		{`NumberInt("12")`, `{"$numberInt":"12"}`},
		{`NumberLong("9223372036854775807")`, `{"$numberLong":"9223372036854775807"}`},
		{`Long(3)`, `{"$numberLong":"3"}`},
		{`Double(4)`, `{"$numberDouble":"4.0"}`},
		{`NumberDecimal("1.10")`, `{"$numberDecimal":"1.10"}`},
		{`ISODate("2026-01-02")`, `{"$date":{"$numberLong":"1767312000000"}}`},
		{`ISODate("2026-01-02T03:04:05.006Z")`, `{"$date":{"$numberLong":"1767323045006"}}`},
		{`ISODate("2026-01-02 03:04:05")`, `{"$date":{"$numberLong":"1767323045000"}}`},
		{`new Date(0)`, `{"$date":{"$numberLong":"0"}}`},
		{`ObjectId("6ac59d2ab231244641a263b2")`, `{"$oid":"6ac59d2ab231244641a263b2"}`},
		{`ObjectId(1)`, `{"$oid":"000000010000000000000000"}`},
		{`UUID("5a58cb19-4890-4f7d-9ac9-def4ffb97dae")`, `{"$binary":{"base64":"WljLGUiQT32ayd70/7l9rg==","subType":"04"}}`},
		{`BinData(0, "aGk=")`, `{"$binary":{"base64":"aGk=","subType":"00"}}`},
		{`Timestamp(10, 2)`, `{"$timestamp":{"t":10,"i":2}}`},
		{`Timestamp({t: 10, i: 2})`, `{"$timestamp":{"t":10,"i":2}}`},
		{`MinKey()`, `{"$minKey":1}`},
		{`MaxKey()`, `{"$maxKey":1}`},
		{`/ab+c/gi`, `{"$regularExpression":{"pattern":"ab+c","options":"i"}}`},
		{`/a\/b\\/`, `{"$regularExpression":{"pattern":"a/b\\\\","options":""}}`},
		{`/a\.b/`, `{"$regularExpression":{"pattern":"a\\.b","options":""}}`},
		{`[1, 2.5, "x", null, true]`, `[{"$numberInt":"1"},{"$numberDouble":"2.5"},"x",null,true]`},
		{`3000000000`, `{"$numberLong":"3000000000"}`},
		{`-0`, `{"$numberDouble":"-0.0"}`},
		{`1/2`, `{"$numberDouble":"0.5"}`},
		{`{a: undefined, b: function() {}, c: 1}`, `{"c":{"$numberInt":"1"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			v, err := Eval(tt.expr)
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := CanonicalValue(v); got != tt.want {
				t.Errorf("Eval(%s) = %s, want %s", tt.expr, got, tt.want)
			}
		})
	}
}

func TestEvalErrors(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		{`{a: `, "SyntaxError"},
		{`ObjectId("nope")`, "invalid ObjectId"},
		{`ObjectId(-1)`, "out of range"},
		{`NumberInt(3000000000)`, "out of range"},
		{`NumberLong("x")`, "invalid NumberLong"},
		{`NumberDecimal("x")`, "invalid NumberDecimal"},
		{`ISODate("yesterday")`, "invalid ISODate"},
		{`UUID("123")`, "invalid UUID"},
		{`BinData(300, "")`, "invalid BinData subtype"},
		{`BinData(0, "***")`, "invalid base64"},
		{`Timestamp(-1, 0)`, "out of range"},
		{`db.orders.find()`, "cursor"},
		{`undefinedVariable`, "ReferenceError"},
		{`(() => { throw new Error("boom") })()`, "boom"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			if _, err := Eval(tt.expr); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Eval(%s) error = %v, want it to contain %q", tt.expr, err, tt.want)
			}
		})
	}
}

func TestEvalDocAndArray(t *testing.T) {
	if d, err := EvalDoc("  ", "filter"); err != nil || len(d) != 0 {
		t.Errorf("empty filter = %v, %v", d, err)
	}
	if _, err := EvalDoc("[1]", "filter"); err == nil || !strings.Contains(err.Error(), "filter must be a document") {
		t.Errorf("array as filter: %v", err)
	}
	if a, err := EvalArray("", "pipeline"); err != nil || len(a) != 0 {
		t.Errorf("empty pipeline = %v, %v", a, err)
	}
	if a, err := EvalArray(`[{$match: {}}, {$limit: 1}]`, "pipeline"); err != nil || len(a) != 2 {
		t.Errorf("pipeline = %v, %v", a, err)
	}
	if _, err := EvalArray(`{}`, "pipeline"); err == nil || !strings.Contains(err.Error(), "pipeline must be an array") {
		t.Errorf("object as pipeline: %v", err)
	}
	// Comments and trailing commas are fine: the text is JavaScript.
	d, err := EvalDoc("{\n  // the sku\n  sku: 'a', // trailing\n}", "filter")
	if err != nil || len(d) != 1 || d[0].Key != "sku" {
		t.Errorf("filter with comments = %v, %v", d, err)
	}
}

func TestJSValuesAndHelpers(t *testing.T) {
	script := `
const id = ObjectId("6ac59d2ab231244641a263b2")
print(id.str, id.toHexString() === id.str, id.getTimestamp().toISOString())
print(id.equals(ObjectId("6ac59d2ab231244641a263b2")), id.equals(ObjectId()))
print(NumberLong(41) + 1, NumberInt(2) * 3, NumberDecimal("1.5") + 1)
print(tojson({a: NumberLong(1)}))
print(typeof UUID().hex(), UUID("5a58cb19-4890-4f7d-9ac9-def4ffb97dae").toString())
console.log("log", 1, [1, 2], {x: null})
use("other")
db.getName()
`
	res := Run(t.Context(), Env{Database: "shop"}, RunRequest{Script: script, Mode: ModeAll})
	for _, r := range res.Results {
		if r.Kind == "error" {
			t.Fatalf("line %d: %s", r.Line, r.Error)
		}
	}
	wantOut := []string{
		"6ac59d2ab231244641a263b2 true 2026-10-07T01:15:22.000Z",
		"true false",
		"42 6 2.5",
		"{\n  a: NumberLong(1)\n}",
		"string 5a58cb19-4890-4f7d-9ac9-def4ffb97dae",
		"log 1 [\n  1,\n  2\n] {\n  x: null\n}",
		"switched to db other",
	}
	if len(res.Output) != len(wantOut) {
		t.Fatalf("output = %q", res.Output)
	}
	for i, w := range wantOut {
		if res.Output[i] != w {
			t.Errorf("output[%d] = %q, want %q", i, res.Output[i], w)
		}
	}
	if res.Database != "other" || len(res.Results) != 1 || res.Results[0].Value != `"other"` {
		t.Errorf("database = %q, results = %+v", res.Database, res.Results)
	}
}

func TestRunStopsAtFirstError(t *testing.T) {
	res := Run(t.Context(), Env{}, RunRequest{Script: "1\nthrow new TypeError('bad')\n2", Mode: ModeAll})
	if len(res.Results) != 2 {
		t.Fatalf("results = %+v", res.Results)
	}
	if r := res.Results[1]; r.Kind != "error" || r.Line != 2 || r.Error != "TypeError: bad" || r.Title != "Error (line 2)" {
		t.Errorf("error result = %+v", r)
	}
}

func TestRunSyntaxError(t *testing.T) {
	res := Run(t.Context(), Env{}, RunRequest{Script: "db.a.find({})\n\ndb.b.find({ x: }", Mode: ModeAll})
	if len(res.Results) != 1 || res.Results[0].Kind != "error" || res.Results[0].Line != 3 {
		t.Errorf("results = %+v", res.Results)
	}
}

func TestRunWritesNeedConnection(t *testing.T) {
	for _, stmt := range []string{
		`db.c.insertOne({})`, `db.c.findOne()`, `db.c.countDocuments()`, `db.runCommand({ping: 1})`, `db.c.find().toArray()`,
	} {
		res := Run(t.Context(), Env{}, RunRequest{Script: stmt, Mode: ModeAll})
		if len(res.Results) != 1 || !strings.Contains(res.Results[0].Error, "not connected") {
			t.Errorf("%s: %+v", stmt, res.Results)
		}
	}
}

func TestRunCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := Run(ctx, Env{}, RunRequest{Script: "while (true) {}", Mode: ModeAll})
	if time.Since(start) > 2*time.Second {
		t.Fatalf("infinite loop was not interrupted (%v)", time.Since(start))
	}
	if len(res.Results) != 1 || res.Results[0].Error != "operation timed out" {
		t.Errorf("results = %+v", res.Results)
	}

	ctx, cancel = context.WithCancel(t.Context())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	res = Run(ctx, Env{}, RunRequest{Script: "sleep(10000)", Mode: ModeAll})
	if len(res.Results) != 1 || res.Results[0].Error != "operation cancelled" {
		t.Errorf("results = %+v", res.Results)
	}
}

func TestSelection(t *testing.T) {
	script := "let a = 1\nlet b = a + 1\nb * 10\n"
	start := strings.Index(script, "b * 10")
	st, err := selectStatements(RunRequest{Script: script, Mode: ModeSelection, SelStart: start, SelEnd: start + len("b * 10")})
	if err != nil || len(st) != 1 || st[0].src != "b * 10" || st[0].line != 3 {
		t.Fatalf("selection = %+v, %v", st, err)
	}
	// An empty selection runs everything.
	st, err = selectStatements(RunRequest{Script: script, Mode: ModeSelection, SelStart: 4, SelEnd: 4})
	if err != nil || len(st) != 3 {
		t.Fatalf("empty selection = %+v, %v", st, err)
	}
}

func TestPickCurrentEdges(t *testing.T) {
	script := "a()\n\n\nb(); c()\n"
	cases := []struct {
		cursor int
		want   string
	}{
		{0, "a()"},
		{len("a()\n"), "a()"}, // blank line after a statement: the previous one
		{len(script), "c()"},  // end of file
		{strings.Index(script, "c()") - 1, "b()"}, // just after "b();": the statement before the cursor
		{strings.Index(script, "c()") + 1, "c()"},
	}
	for _, c := range cases {
		st, err := selectStatements(RunRequest{Script: script, Mode: ModeCurrent, Cursor: c.cursor})
		if err != nil || len(st) != 1 || st[0].src != c.want {
			t.Errorf("cursor %d: %+v, %v; want %q", c.cursor, st, err, c.want)
		}
	}
	if st, err := selectStatements(RunRequest{Script: "  \n", Mode: ModeCurrent}); err != nil || len(st) != 0 {
		t.Errorf("empty script: %+v, %v", st, err)
	}
	if _, err := paragraphAt("{ broken\n\n\n", 9); err == nil {
		t.Error("expected an error for a blank paragraph")
	}
}

func TestTextQuery(t *testing.T) {
	q := TextQuery(QuerySpec{
		Kind: "find", Database: "d", Collection: "c",
		Filter:     bson.D{{Key: "a", Value: int32(1)}},
		Projection: bson.D{{Key: "a", Value: int32(1)}},
		Skip:       2, Limit: 3,
	})
	if q.Filter != "{ a: 1 }" || q.Projection != "{ a: 1 }" || q.Sort != "" || q.Skip != 2 || q.Limit != 3 {
		t.Errorf("find query = %+v", q)
	}
	agg := TextQuery(QuerySpec{Kind: "aggregate", Pipeline: bson.A{bson.D{{Key: "$match", Value: bson.D{}}}}})
	if agg.Pipeline != "[\n  {\n    $match: {}\n  }\n]" {
		t.Errorf("pipeline = %q", agg.Pipeline)
	}
}

func FuzzEval(f *testing.F) {
	for _, seed := range []string{`{a: 1}`, `[ObjectId(), ISODate()]`, `/x/`, `NumberLong("1")`, `{`, `"\u0000"`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 512 || strings.Contains(s, "while") || strings.Contains(s, "for") || strings.Contains(s, "sleep") {
			t.Skip()
		}
		_, _ = Eval(s) // must not panic
	})
}

func FuzzSelectStatements(f *testing.F) {
	f.Add("db.a.find({})\n\nprint(1)", 3)
	f.Add("😀 x\n{", 1)
	f.Fuzz(func(t *testing.T, script string, cursor int) {
		for _, mode := range []Mode{ModeAll, ModeCurrent, ModeSelection} {
			_, _ = selectStatements(RunRequest{Script: script, Mode: mode, Cursor: cursor, SelStart: cursor / 2, SelEnd: cursor})
		}
		if b := byteOffset(script, cursor); b < 0 || b > len(script) {
			t.Fatalf("byteOffset out of range: %d", b)
		}
	})
}

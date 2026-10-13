package shell

import (
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestEvalDocTypes(t *testing.T) {
	d, err := EvalDoc(`{sku: /AXIS2JGS-903068_/i, n: 5, f: 5.5, l: NumberLong("9007199254740993"), d: Double(3),
		id: ObjectId("6ac59d2ab231244641a263b2"), at: ISODate("2026-10-07T11:33:09Z"), arr: [1, "x"], "a.b": null}`, "doc")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"sku": bson.Regex{Pattern: "AXIS2JGS-903068_", Options: "i"},
		"n":   int32(5),
		"f":   5.5,
		"l":   int64(9007199254740993),
		"d":   float64(3),
	}
	for k, w := range want {
		got, _ := lookup(d, k)
		if got != w {
			t.Errorf("%s = %#v, want %#v", k, got, w)
		}
	}
	if id, _ := lookup(d, "id"); id.(bson.ObjectID).Hex() != "6ac59d2ab231244641a263b2" {
		t.Errorf("id = %v", id)
	}
	if at, _ := lookup(d, "at"); at.(bson.DateTime).Time().Unix() != 1791372789 {
		t.Errorf("at = %v", at)
	}
	if d[len(d)-1].Key != "a.b" {
		t.Errorf("key order not preserved: %v", d)
	}
}

func TestFormatRoundTrip(t *testing.T) {
	src := `{ _id: ObjectId("6ac59d2ab231244641a263b2"), n: 5, d: Double(3), l: NumberLong(7), re: /a\/b/i, "x-y": [ 1, 2 ] }`
	d, err := EvalDoc(src, "doc")
	if err != nil {
		t.Fatal(err)
	}
	out := Format(d, "")
	d2, err := EvalDoc(out, "doc")
	if err != nil {
		t.Fatalf("re-parse %q: %v", out, err)
	}
	if Format(d2, "") != out {
		t.Errorf("round trip mismatch:\n%s\n%s", out, Format(d2, ""))
	}
}

func TestSelectCurrent(t *testing.T) {
	script := "db.a.find({})\n\nlet x = 1\ndb.b.find({x: x}).size()\n\ndb.c.find({sku: /A/})"
	cases := []struct {
		cursor int
		want   string
	}{
		{3, "db.a.find({})"},
		{len("db.a.find({})"), "db.a.find({})"},
		{strings.Index(script, "db.c") + 5, "db.c.find({sku: /A/})"},
		{strings.Index(script, ".size") + 2, "db.b.find({x: x}).size()"},
	}
	for _, c := range cases {
		st, err := selectStatements(RunRequest{Script: script, Mode: ModeCurrent, Cursor: c.cursor})
		if err != nil || len(st) != 1 || st[0].src != c.want {
			t.Errorf("cursor %d: got %+v, %v; want %q", c.cursor, st, err, c.want)
		}
	}
	all, err := selectStatements(RunRequest{Script: script, Mode: ModeAll})
	if err != nil || len(all) != 4 || all[3].line != 6 {
		t.Errorf("all: %+v %v", all, err)
	}
}

func TestSelectCurrentWithBrokenScript(t *testing.T) {
	script := "db.a.find({ broken\n\ndb.b.find({ok: 1})\n"
	st, err := selectStatements(RunRequest{Script: script, Mode: ModeCurrent, Cursor: strings.Index(script, "db.b") + 2})
	if err != nil || len(st) != 1 || !strings.HasPrefix(st[0].src, "db.b.find") || st[0].line != 3 {
		t.Fatalf("got %+v %v", st, err)
	}
}

func TestRunWithoutConnection(t *testing.T) {
	res := Run(t.Context(), Env{Database: "gateway"}, RunRequest{Script: "let a = [1,2].map(x => x * 2)\nprint(a)\na\ndb.getName()\ndb.orders.find({}).limit(2)", Mode: ModeAll})
	if len(res.Output) != 1 || !strings.Contains(res.Output[0], "2") {
		t.Errorf("output = %v", res.Output)
	}
	if len(res.Results) != 3 {
		t.Fatalf("results = %+v", res.Results)
	}
	if res.Results[0].Kind != "value" || res.Results[0].Value != `[{"$numberInt":"2"},{"$numberInt":"4"}]` {
		t.Errorf("value = %+v", res.Results[0])
	}
	if res.Results[1].Value != `"gateway"` {
		t.Errorf("db name = %+v", res.Results[1])
	}
	if r := res.Results[2]; r.Kind != "error" || r.Query == nil || r.Query.Limit != 2 {
		t.Errorf("cursor result = %+v", r)
	}
}

func TestUTF16Offsets(t *testing.T) {
	s := "// 😀 ă\nx"
	if got := byteOffset(s, len([]rune("// 😀 ă\n"))+1); s[got:] != "x" {
		t.Errorf("offset -> %q", s[got:])
	}
}

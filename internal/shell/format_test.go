package shell

import (
	"math"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestFormatScalars(t *testing.T) {
	oid, _ := bson.ObjectIDFromHex("6ac59d2ab231244641a263b2")
	dec, _ := bson.ParseDecimal128("12.50")
	at := time.Date(2026, 10, 7, 11, 33, 9, 5e6, time.UTC)
	uuid := bson.Binary{Subtype: bson.TypeBinaryUUID, Data: []byte{0x5a, 0x58, 0xcb, 0x19, 0x48, 0x90, 0x4f, 0x7d, 0x9a, 0xc9, 0xde, 0xf4, 0xff, 0xb9, 0x7d, 0xae}}

	tests := []struct {
		name string
		v    any
		want string
	}{
		{"nil", nil, "null"},
		{"null", bson.Null{}, "null"},
		{"undefined", bson.Undefined{}, "undefined"},
		{"string", `a "q"`, `"a \"q\""`},
		{"bool", true, "true"},
		{"int32", int32(-5), "-5"},
		{"int", 7, "7"},
		{"int64", int64(9007199254740993), "NumberLong(9007199254740993)"},
		{"double", 2.5, "2.5"},
		{"integral double", 3.0, "Double(3)"},
		{"large double", 1e20, "1e+20"},
		{"nan", math.NaN(), "NaN"},
		{"+inf", math.Inf(1), "Infinity"},
		{"-inf", math.Inf(-1), "-Infinity"},
		{"objectid", oid, `ObjectId("6ac59d2ab231244641a263b2")`},
		{"datetime", bson.NewDateTimeFromTime(at), `ISODate("2026-10-07T11:33:09.005Z")`},
		{"time", at, `ISODate("2026-10-07T11:33:09.005Z")`},
		{"regex", bson.Regex{Pattern: "a/b", Options: "i"}, `/a\/b/i`},
		{"regex already escaped", bson.Regex{Pattern: `a\/b`, Options: ""}, `/a\/b/`},
		{"empty regex", bson.Regex{}, "/(?:)/"},
		{"decimal", dec, `NumberDecimal("12.50")`},
		{"uuid", uuid, `UUID("5a58cb19-4890-4f7d-9ac9-def4ffb97dae")`},
		{"binary", bson.Binary{Subtype: 0, Data: []byte("hi")}, `BinData(0, "aGk=")`},
		{"timestamp", bson.Timestamp{T: 1, I: 2}, "Timestamp(1, 2)"},
		{"minkey", bson.MinKey{}, "MinKey()"},
		{"maxkey", bson.MaxKey{}, "MaxKey()"},
		{"code", bson.JavaScript("x()"), `Code("x()")`},
		{"symbol", bson.Symbol("s"), `"s"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Format(tt.v, ""); got != tt.want {
				t.Errorf("Format(%v) = %s, want %s", tt.v, got, tt.want)
			}
		})
	}
}

func TestFormatContainers(t *testing.T) {
	doc := bson.D{
		{Key: "_id", Value: int32(1)},
		{Key: "a-b", Value: bson.A{int32(1), "x"}},
		{Key: "nested", Value: bson.D{{Key: "k", Value: bson.D{}}}},
		{Key: "empty", Value: bson.A{}},
	}
	if got, want := Format(doc, ""), `{ _id: 1, "a-b": [1, "x"], nested: { k: {} }, empty: [] }`; got != want {
		t.Errorf("inline:\n got %s\nwant %s", got, want)
	}
	want := `{
  _id: 1,
  "a-b": [
    1,
    "x"
  ],
  nested: {
    k: {}
  },
  empty: []
}`
	if got := Format(doc, "  "); got != want {
		t.Errorf("indented:\n%s\nwant\n%s", got, want)
	}
	// bson.M is printed with sorted keys so output is stable.
	if got := Format(bson.M{"b": int32(2), "a": int32(1)}, ""); got != "{ a: 1, b: 2 }" {
		t.Errorf("bson.M = %s", got)
	}
	raw, _ := bson.Marshal(bson.D{{Key: "x", Value: int32(1)}})
	if got := Format(bson.Raw(raw), ""); got != "{ x: 1 }" {
		t.Errorf("bson.Raw = %s", got)
	}
}

func TestCanonicalValue(t *testing.T) {
	tests := []struct {
		v    any
		want string
	}{
		{int32(1), `{"$numberInt":"1"}`},
		{int64(1), `{"$numberLong":"1"}`},
		{2.0, `{"$numberDouble":"2.0"}`},
		{"s", `"s"`},
		{bson.A{true}, `[true]`},
	}
	for _, tt := range tests {
		if got, err := CanonicalValue(tt.v); err != nil || got != tt.want {
			t.Errorf("CanonicalValue(%v) = %s, %v; want %s", tt.v, got, err, tt.want)
		}
	}
}

// Everything Format prints must evaluate back to the same value.
func TestFormatEvalRoundTrip(t *testing.T) {
	oid, _ := bson.ObjectIDFromHex("6ac59d2ab231244641a263b2")
	dec, _ := bson.ParseDecimal128("-0.001")
	doc := bson.D{
		{Key: "oid", Value: oid},
		{Key: "i", Value: int32(-42)},
		{Key: "l", Value: int64(1) << 60},
		{Key: "d", Value: 7.0},
		{Key: "f", Value: 0.1},
		{Key: "s", Value: "line\nbreak \u00e9"},
		{Key: "b", Value: false},
		{Key: "n", Value: nil},
		{Key: "at", Value: bson.NewDateTimeFromTime(time.Date(2020, 2, 29, 23, 59, 59, 999e6, time.UTC))},
		{Key: "re", Value: bson.Regex{Pattern: "^a.*/x$", Options: "im"}},
		{Key: "dec", Value: dec},
		{Key: "uuid", Value: bson.Binary{Subtype: bson.TypeBinaryUUID, Data: make([]byte, 16)}},
		{Key: "bin", Value: bson.Binary{Subtype: 5, Data: []byte{1, 2, 3}}},
		{Key: "ts", Value: bson.Timestamp{T: 1700000000, I: 3}},
		{Key: "min", Value: bson.MinKey{}},
		{Key: "max", Value: bson.MaxKey{}},
		{Key: "arr", Value: bson.A{int32(1), bson.D{{Key: "x", Value: "y"}}}},
		{Key: "key with spaces", Value: int32(1)},
	}
	for _, indent := range []string{"", "  "} {
		text := Format(doc, indent)
		back, err := EvalDoc(text, "doc")
		if err != nil {
			t.Fatalf("EvalDoc(%s): %v", text, err)
		}
		a, _ := CanonicalValue(doc)
		b, _ := CanonicalValue(back)
		if a != b {
			t.Errorf("round trip with indent %q:\n got %s\nwant %s", indent, b, a)
		}
	}
}

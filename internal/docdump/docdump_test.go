package docdump

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

var fixedID, _ = bson.ObjectIDFromHex("5f1d7f3e9d1e8b3a4c8b4567")

func sampleDocs(t *testing.T) []bson.Raw {
	t.Helper()
	docs := []bson.D{
		{{Key: "_id", Value: fixedID}, {Key: "n", Value: int32(1)}, {Key: "big", Value: int64(1)}, {Key: "f", Value: 1.5}},
		{{Key: "_id", Value: "two"}, {Key: "at", Value: bson.NewDateTimeFromTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))}, {Key: "nested", Value: bson.D{{Key: "a", Value: bson.A{1, "x", nil}}}}},
		{{Key: "_id", Value: int64(3)}, {Key: "d", Value: bson.Decimal128{}}, {Key: "re", Value: bson.Regex{Pattern: "a/b", Options: "i"}}},
	}
	out := make([]bson.Raw, len(docs))
	for i, d := range docs {
		raw, err := bson.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = raw
	}
	return out
}

func roundTrip(t *testing.T, f Format, canonical, gz bool) []bson.Raw {
	t.Helper()
	var buf bytes.Buffer
	var w io.Writer = &buf
	var zw *gzip.Writer
	if gz {
		zw = gzip.NewWriter(&buf)
		w = zw
	}
	enc := NewEncoder(w, f, canonical)
	for _, d := range sampleDocs(t) {
		if err := enc.Encode(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	if zw != nil {
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	dec, err := NewDecoder(&buf, f)
	if err != nil {
		t.Fatal(err)
	}
	var got []bson.Raw
	for {
		d, err := dec.Next()
		if errors.Is(err, io.EOF) {
			return got
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, d)
	}
}

func TestRoundTripLossless(t *testing.T) {
	for _, tc := range []struct {
		f  Format
		gz bool
	}{{JSONL, false}, {JSONL, true}, {JSON, false}, {JSON, true}, {BSON, false}, {BSON, true}} {
		got := roundTrip(t, tc.f, true, tc.gz)
		want := sampleDocs(t)
		if len(got) != len(want) {
			t.Fatalf("%s gz=%v: %d docs, want %d", tc.f, tc.gz, len(got), len(want))
		}
		for i := range want {
			if !bytes.Equal(got[i], want[i]) {
				t.Errorf("%s gz=%v doc %d: %s, want %s", tc.f, tc.gz, i, got[i], want[i])
			}
		}
	}
}

func TestRelaxedKeepsValues(t *testing.T) {
	got := roundTrip(t, JSONL, false, false)
	if len(got) != 3 {
		t.Fatalf("%d docs", len(got))
	}
	if v := got[1].Lookup("nested", "a").Array().Index(1).StringValue(); v != "x" {
		t.Errorf("nested value %q", v)
	}
	if got[1].Lookup("at").Type != bson.TypeDateTime {
		t.Errorf("date type %v", got[1].Lookup("at").Type)
	}
}

func TestDecodeJSONVariants(t *testing.T) {
	for name, in := range map[string]string{
		"lines":        "{\"a\":1}\n{\"a\":2}\n\n",
		"array":        "[ {\"a\":1}, {\"a\":2} ]",
		"pretty":       "{\n  \"a\": 1\n}\n{\n  \"a\": 2\n}",
		"bom":          "\xEF\xBB\xBF{\"a\":1}\n{\"a\":2}",
		"mongoexport":  `{"_id":{"$oid":"5f1d7f3e9d1e8b3a4c8b4567"},"a":{"$numberLong":"1"}}` + "\n" + `{"a":2}`,
		"empty_array0": "[{\"a\":1},{\"a\":2}]\n",
	} {
		dec, err := NewDecoder(strings.NewReader(in), JSON)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		n := 0
		for {
			_, err := dec.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			n++
		}
		if n != 2 {
			t.Errorf("%s: %d docs", name, n)
		}
	}
}

func TestDecodeErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		in string
		f  Format
	}{
		"not object":  {"[1,2]", JSON},
		"bad json":    {"{\"a\":", JSONL},
		"bad bson":    {"\x03\x00\x00\x00", BSON},
		"short bson":  {"\x10\x00\x00\x00\x00", BSON},
		"bad ext":     {`{"a":{"$numberLong":"x"}}`, JSONL},
		"bad gzip in": {"\x1f\x8bxxxx", JSONL},
	} {
		dec, err := NewDecoder(strings.NewReader(tc.in), tc.f)
		if err == nil {
			_, err = dec.Next()
		}
		if err == nil || errors.Is(err, io.EOF) {
			t.Errorf("%s: want an error, got %v", name, err)
		}
	}
	for _, f := range []Format{JSON, JSONL, BSON} {
		dec, err := NewDecoder(strings.NewReader(""), f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := dec.Next(); !errors.Is(err, io.EOF) {
			t.Errorf("%s: empty input gave %v", f, err)
		}
	}
}

func TestEmptyJSONArray(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf, JSON, true)
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "[]\n" {
		t.Errorf("got %q", buf.String())
	}
	dec, _ := NewDecoder(&buf, JSON)
	if _, err := dec.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("got %v", err)
	}
}

func TestDetectAndNames(t *testing.T) {
	for in, want := range map[string]struct {
		f    Format
		gz   bool
		stem string
		ok   bool
	}{
		"/x/orders.jsonl":         {JSONL, false, "orders", true},
		"orders.JSON.gz":          {JSON, true, "orders", true},
		"a.b.bson":                {BSON, false, "a.b", true},
		"x.ndjson":                {JSONL, false, "x", true},
		"orders.metadata.json":    {"", false, "", false},
		"orders.metadata.json.gz": {"", false, "", false},
		"notes.txt":               {"", false, "", false},
	} {
		f, gz, stem, ok := Detect(in)
		if f != want.f || gz != want.gz || stem != want.stem || ok != want.ok {
			t.Errorf("Detect(%q) = %v %v %q %v", in, f, gz, stem, ok)
		}
	}
	if got := FileName("a/b:c", BSON, true); got != "a_b_c.bson.gz" {
		t.Errorf("FileName = %q", got)
	}
	if got := SafeName(".."); got != "_.." {
		t.Errorf("SafeName = %q", got)
	}
	if MetadataName("x") != "x.metadata.json" {
		t.Error("MetadataName")
	}
	if !JSON.Valid() || Format("csv").Valid() {
		t.Error("Valid")
	}
}

func TestMetadata(t *testing.T) {
	m := Metadata{
		CollectionName: "orders",
		Type:           "collection",
		Indexes:        []bson.D{{{Key: "v", Value: int32(2)}, {Key: "key", Value: bson.D{{Key: "sku", Value: int32(1)}}}, {Key: "name", Value: "sku_1"}}},
	}
	data, err := EncodeMetadata(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []func() io.Reader{
		func() io.Reader { return bytes.NewReader(data) },
		func() io.Reader {
			var b bytes.Buffer
			zw := gzip.NewWriter(&b)
			_, _ = zw.Write(data)
			_ = zw.Close()
			return &b
		},
	} {
		got, err := DecodeMetadata(src())
		if err != nil {
			t.Fatal(err)
		}
		if got.CollectionName != "orders" || len(got.Indexes) != 1 || got.Indexes[0][2].Value != "sku_1" {
			t.Errorf("got %+v", got)
		}
	}
	if _, err := DecodeMetadata(strings.NewReader("nope")); err == nil {
		t.Error("want error")
	}
}

func TestCounters(t *testing.T) {
	r := &CountingReader{R: strings.NewReader("hello")}
	_, _ = io.ReadAll(r)
	var b bytes.Buffer
	w := &CountingWriter{W: &b}
	_, _ = w.Write([]byte("abc"))
	if r.N != 5 || w.N != 3 {
		t.Errorf("counts %d %d", r.N, w.N)
	}
}

func FuzzDecoder(f *testing.F) {
	f.Add([]byte(`[{"a":1}]`), uint8(0))
	f.Add([]byte("{\"a\":{\"$date\":\"2020-01-01T00:00:00Z\"}}\n"), uint8(1))
	f.Add([]byte("\x05\x00\x00\x00\x00"), uint8(2))
	f.Fuzz(func(t *testing.T, data []byte, which uint8) {
		f := []Format{JSON, JSONL, BSON}[int(which)%3]
		dec, err := NewDecoder(bytes.NewReader(data), f)
		if err != nil {
			return
		}
		for range 1000 {
			if _, err := dec.Next(); err != nil {
				return
			}
		}
	})
}

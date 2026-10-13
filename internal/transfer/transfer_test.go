package transfer

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func str(s string) *string { return &s }

var cols = []Column{{Name: "id", Kind: KindNumber}, {Name: "name", Kind: "text"}, {Name: "ok", Kind: KindBool}, {Name: "doc", Kind: KindJSON}}

var rows = [][]*string{
	{str("1"), str("plain"), str("true"), str(`{"a": [1, 2]}`)},
	{str("2"), str(""), str("f"), nil},
	{str("3"), nil, nil, str("not json")},
	{str("4.5e3"), str("comma, \"quote\"\nline"), str("1"), str(`"s"`)},
}

func roundTrip(t *testing.T, f Format) []Record {
	t.Helper()
	var buf bytes.Buffer
	enc, err := NewEncoder(&buf, f, cols, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	dec, err := NewDecoder(&buf, f)
	if err != nil {
		t.Fatalf("%s: %v\n%s", f, err, buf.String())
	}
	var out []Record
	for {
		rec, err := dec.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out = append(out, rec)
	}
}

func texts(vals []*string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		if v == nil {
			out[i] = "<NULL>"
		} else {
			out[i] = *v
		}
	}
	return out
}

func TestCSVKeepsNullsAndEmptyStrings(t *testing.T) {
	got := roundTrip(t, CSV)
	if len(got) != len(rows) {
		t.Fatalf("rows = %d", len(got))
	}
	for i, r := range got {
		if !reflect.DeepEqual(r.Columns, []string{"id", "name", "ok", "doc"}) || !reflect.DeepEqual(texts(r.Values), texts(rows[i])) {
			t.Errorf("row %d = %q, want %q", i, texts(r.Values), texts(rows[i]))
		}
	}
}

func TestJSONFormatsKeepTypes(t *testing.T) {
	for _, f := range []Format{JSONL, JSON} {
		got := roundTrip(t, f)
		if len(got) != 4 {
			t.Fatalf("%s rows = %d", f, len(got))
		}
		want := [][]string{
			{"1", "plain", "true", `{"a": [1, 2]}`},
			{"2", "", "false", "<NULL>"},
			{"3", "<NULL>", "<NULL>", "not json"},
			{"4.5e3", "comma, \"quote\"\nline", "true", "s"},
		}
		for i, r := range got {
			if !reflect.DeepEqual(texts(r.Values), want[i]) {
				t.Errorf("%s row %d = %q, want %q", f, i, texts(r.Values), want[i])
			}
		}
	}
	var buf bytes.Buffer
	enc, _ := NewEncoder(&buf, JSONL, cols, "", nil)
	_ = enc.Encode(rows[0])
	_ = enc.Close()
	if got := buf.String(); got != `{"id": 1, "name": "plain", "ok": true, "doc": {"a": [1, 2]}}`+"\n" {
		t.Errorf("jsonl = %s", got)
	}
	buf.Reset()
	enc, _ = NewEncoder(&buf, JSON, cols, "", nil)
	_ = enc.Close()
	if buf.String() != "[]\n" {
		t.Errorf("empty json = %q", buf.String())
	}
}

func TestSQLFormat(t *testing.T) {
	var buf bytes.Buffer
	enc, err := NewEncoder(&buf, SQL, cols, "CREATE TABLE t (id int);", func(v []*string) string { return "INSERT INTO t VALUES (" + *v[0] + ")" })
	if err != nil {
		t.Fatal(err)
	}
	_ = enc.Encode(rows[0])
	_ = enc.Close()
	if got := buf.String(); got != "CREATE TABLE t (id int);\n\nINSERT INTO t VALUES (1);\n" {
		t.Errorf("sql = %q", got)
	}
}

func TestDetectAndNames(t *testing.T) {
	for in, want := range map[string]string{"a/orders.csv": "csv orders false", "x.JSONL.gz": "jsonl x true", "t.ndjson": "jsonl t false", "dump.sql": "sql dump false"} {
		f, gz, stem, ok := Detect(in)
		if !ok || string(f)+" "+stem+" "+map[bool]string{true: "true", false: "false"}[gz] != want {
			t.Errorf("Detect(%s) = %s %s %v %v", in, f, stem, gz, ok)
		}
	}
	if _, _, _, ok := Detect("orders.schema.sql"); ok {
		t.Error("a table definition is not a dump")
	}
	if FileName("a/b:c", CSV, true) != "a_b_c.csv.gz" || SchemaName("t") != "t.schema.sql" {
		t.Error("names")
	}
}

func TestCSVErrors(t *testing.T) {
	dec, err := NewDecoder(strings.NewReader("a,b\n1\n"), CSV)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dec.Next(); err == nil {
		t.Error("short row accepted")
	}
	if _, err := NewDecoder(strings.NewReader(""), CSV); err == nil {
		t.Error("empty file accepted")
	}
	if _, err := NewDecoder(strings.NewReader(`{"a": 1}`), JSON); err == nil {
		t.Error("an object is not an array")
	}
}

func FuzzCSV(f *testing.F) {
	f.Add("a", "", true)
	f.Add("x,\"y\"\n", "\r\n", false)
	f.Fuzz(func(t *testing.T, a, b string, null bool) {
		vals := []*string{&a, &b}
		if null {
			vals[1] = nil
		}
		var buf bytes.Buffer
		enc, _ := NewEncoder(&buf, CSV, []Column{{Name: "a"}, {Name: "b"}}, "", nil)
		_ = enc.Encode(vals)
		_ = enc.Close()
		dec, err := NewDecoder(&buf, CSV)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := dec.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(texts(rec.Values), texts(vals)) {
			t.Fatalf("got %q, want %q", texts(rec.Values), texts(vals))
		}
	})
}

package docapp

import (
	"slices"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/shell"
)

func mustDoc(t *testing.T, text string) bson.D {
	t.Helper()
	d, err := shell.EvalDoc(text, "doc")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestParseIndexSpec(t *testing.T) {
	spec, err := parseIndexSpec(`{ key: { a: 1, "b.c": -1, loc: "2dsphere" }, unique: true }`)
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := find(spec, "name"); name != "a_1_b.c_-1_loc_2dsphere" {
		t.Errorf("default name = %v", name)
	}
	spec, err = parseIndexSpec(`{ key: { a: 1 }, name: "custom" }`)
	if name, _ := find(spec, "name"); err != nil || name != "custom" {
		t.Errorf("explicit name = %v, %v", name, err)
	}
	for _, bad := range []string{`{}`, `{ key: {} }`, `{ key: 1 }`, `{ key: { "": 1 } }`, `{ key: `} {
		if _, err := parseIndexSpec(bad); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
}

func TestDiffIndex(t *testing.T) {
	current := mustDoc(t, `{ v: 2, key: { a: 1 }, name: "a_1", expireAfterSeconds: 3600 }`)
	tests := []struct {
		wanted  string
		changed []string
		inPlace bool
	}{
		{`{ key: { a: NumberLong(1) }, name: "a_1", expireAfterSeconds: Double(3600) }`, nil, true},
		{`{ key: { a: 1 }, name: "a_1", expireAfterSeconds: 60 }`, []string{"expireAfterSeconds"}, true},
		{`{ key: { a: 1 }, name: "a_1", expireAfterSeconds: 3600, hidden: true }`, []string{"hidden"}, true},
		{`{ key: { a: 1 }, name: "a_1", expireAfterSeconds: 3600, hidden: false }`, nil, true},
		{`{ key: { a: -1 }, name: "a_1", expireAfterSeconds: 3600 }`, []string{"key"}, false},
		{`{ key: { a: 1 }, name: "a_1", expireAfterSeconds: 3600, unique: true }`, []string{"unique"}, false},
		{`{ key: { a: 1 }, name: "renamed", expireAfterSeconds: 3600 }`, []string{"name"}, false},
		{`{ key: { a: 1 }, name: "a_1" }`, []string{"expireAfterSeconds"}, true},
	}
	for _, tt := range tests {
		changed, inPlace := diffIndex(current, mustDoc(t, tt.wanted))
		if !slices.Equal(changed, tt.changed) || (len(tt.changed) > 0 && inPlace != tt.inPlace) {
			t.Errorf("%s: changed=%v inPlace=%v; want %v %v", tt.wanted, changed, inPlace, tt.changed, tt.inPlace)
		}
	}
}

func TestTextKeyAsFields(t *testing.T) {
	listed := mustDoc(t, `{ v: 2, key: { tenant: 1, _fts: "text", _ftsx: 1 }, name: "t", weights: { title: 10, body: 1 }, textIndexVersion: 3 }`)
	got := textKeyAsFields(listed)
	key, _ := find(got, "key")
	if s := shell.Format(key, ""); s != `{ tenant: 1, title: "text", body: "text" }` {
		t.Errorf("key = %s", s)
	}
	// The listed spec and the one created from the editor compare equal.
	wanted := mustDoc(t, `{ key: { tenant: 1, title: "text", body: "text" }, name: "t", weights: { title: 10, body: 1 } }`)
	if changed, _ := diffIndex(listed, wanted); len(changed) != 0 {
		t.Errorf("text index reported as changed: %v", changed)
	}
	plain := mustDoc(t, `{ key: { a: 1 } }`)
	if shell.Format(textKeyAsFields(plain), "") != shell.Format(plain, "") {
		t.Error("non-text specs must be unchanged")
	}
}

func TestIntegrationIndexEditor(t *testing.T) {
	c, _ := newTestConns(t)
	ctx := t.Context()
	id := save(t, c, profile.Profile{Name: "root", User: "root", Password: "rootpw"})
	if _, err := c.Connect(ctx, id); err != nil {
		t.Fatal(err)
	}
	s, _ := c.get(id)
	coll := s.Client.Database("shop").Collection("idx")
	_ = coll.Drop(ctx)
	if _, err := coll.InsertMany(ctx, []any{bson.D{{Key: "a", Value: 1}, {Key: "t", Value: "x"}}, bson.D{{Key: "a", Value: 1}, {Key: "t", Value: "y"}}}); err != nil {
		t.Fatal(err)
	}

	res, err := c.SaveIndex(ctx, id, "shop", "idx", "", `{ key: { a: 1 }, expireAfterSeconds: 3600 }`)
	if err != nil || res.Name != "a_1" || res.Action != "created" {
		t.Fatalf("create = %+v, %v", res, err)
	}
	d, err := c.GetIndex(ctx, id, "shop", "idx", "a_1")
	if err != nil || !strings.Contains(d.Spec, `"expireAfterSeconds":{"$numberInt":"3600"}`) {
		t.Fatalf("GetIndex = %+v, %v", d, err)
	}
	if d.SizeBytes <= 0 || d.Ops < 0 {
		t.Errorf("stats = size %d ops %d", d.SizeBytes, d.Ops)
	}

	// TTL and hidden change in place.
	res, err = c.SaveIndex(ctx, id, "shop", "idx", "a_1", `{ key: { a: 1 }, name: "a_1", expireAfterSeconds: 60, hidden: true }`)
	if err != nil || res.Action != "modified" {
		t.Fatalf("modify = %+v, %v", res, err)
	}
	d, _ = c.GetIndex(ctx, id, "shop", "idx", "a_1")
	if !strings.Contains(d.Spec, `"expireAfterSeconds":{"$numberInt":"60"}`) || !strings.Contains(d.Spec, `"hidden":true`) {
		t.Errorf("after modify = %s", d.Spec)
	}
	res, err = c.SaveIndex(ctx, id, "shop", "idx", "a_1", `{ key: { a: 1 }, name: "a_1", expireAfterSeconds: 60, hidden: true }`)
	if err != nil || res.Action != "unchanged" {
		t.Errorf("no-op save = %+v, %v", res, err)
	}

	// A new key definition recreates the index under the new name.
	res, err = c.SaveIndex(ctx, id, "shop", "idx", "a_1", `{ key: { a: -1 } }`)
	if err != nil || res.Action != "recreated" || res.Name != "a_-1" {
		t.Fatalf("recreate = %+v, %v", res, err)
	}
	if _, err := c.GetIndex(ctx, id, "shop", "idx", "a_1"); err == nil {
		t.Error("the old index must be gone")
	}

	// An index that cannot be built leaves the previous one in place.
	_, err = c.SaveIndex(ctx, id, "shop", "idx", "a_-1", `{ key: { a: -1 }, unique: true }`)
	if err == nil || !strings.Contains(err.Error(), "previous index was restored") {
		t.Fatalf("unique on duplicates: %v", err)
	}
	if d, err := c.GetIndex(ctx, id, "shop", "idx", "a_-1"); err != nil || strings.Contains(d.Spec, "unique") {
		t.Errorf("restored index = %+v, %v", d, err)
	}

	// Text indexes round-trip without a rebuild.
	if _, err := c.SaveIndex(ctx, id, "shop", "idx", "", `{ key: { t: "text" }, name: "t_text", weights: { t: 5 }, default_language: "english" }`); err != nil {
		t.Fatal(err)
	}
	d, _ = c.GetIndex(ctx, id, "shop", "idx", "t_text")
	if !strings.Contains(d.Spec, `"key":{"t":"text"}`) {
		t.Errorf("text spec = %s", d.Spec)
	}
	res, err = c.SaveIndex(ctx, id, "shop", "idx", "t_text", `{ key: { t: "text" }, name: "t_text", weights: { t: 5 }, default_language: "english", language_override: "language" }`)
	if err != nil || res.Action != "unchanged" {
		t.Errorf("text no-op = %+v, %v", res, err)
	}

	if _, err := c.SaveIndex(ctx, id, "shop", "idx", "_id_", `{ key: { _id: 1 }, name: "_id_", unique: true }`); err == nil {
		t.Error("the _id index must not be editable")
	}
	if _, err := c.GetIndex(ctx, id, "shop", "idx", "missing"); err == nil {
		t.Error("expected an error for a missing index")
	}

	ro := save(t, c, profile.Profile{Name: "ro", User: "root", Password: "rootpw", ReadOnly: true})
	if _, err := c.Connect(ctx, ro); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SaveIndex(ctx, ro, "shop", "idx", "", `{ key: { b: 1 } }`); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("read-only: %v", err)
	}
}

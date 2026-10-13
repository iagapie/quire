package docapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/iagapie/quire/internal/docsession"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/shell"
)

func TestParseID(t *testing.T) {
	oid := bson.NewObjectID()
	tests := []struct {
		in   string
		want any
	}{
		{`{"$oid":"` + oid.Hex() + `"}`, oid},
		{`{"$numberInt":"7"}`, int32(7)},
		{`{"$numberLong":"7"}`, int64(7)},
		{`"abc"`, "abc"},
	}
	for _, tt := range tests {
		got, err := parseID(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("parseID(%s) = %#v, %v", tt.in, got, err)
		}
	}
	// Compound ids round-trip too.
	got, err := parseID(`{"a":{"$numberInt":"1"}}`)
	if c, _ := shell.CanonicalValue(got); err != nil || c != `{"a":{"$numberInt":"1"}}` {
		t.Errorf("compound id = %s, %v", c, err)
	}
	if _, err := parseID(`{"$oid":`); err == nil {
		t.Error("expected an error for malformed JSON")
	}
}

func TestToSpec(t *testing.T) {
	spec, err := toSpec(shell.Query{Kind: "find", Database: "d", Collection: "c", Filter: `{a: /x/}`, Sort: `{a: -1}`, Skip: 1, Limit: 2})
	if err != nil || len(spec.Filter) != 1 || len(spec.Sort) != 1 || len(spec.Projection) != 0 || spec.Skip != 1 || spec.Limit != 2 {
		t.Errorf("find spec = %+v, %v", spec, err)
	}
	spec, err = toSpec(shell.Query{Kind: "aggregate", Pipeline: `[{$match: {}}]`})
	if err != nil || len(spec.Pipeline) != 1 {
		t.Errorf("aggregate spec = %+v, %v", spec, err)
	}
	for _, q := range []shell.Query{
		{Kind: "find", Filter: `{a:`},
		{Kind: "find", Projection: `[1]`},
		{Kind: "find", Sort: `"x"`},
		{Kind: "find", Skip: -1},
		{Kind: "aggregate", Pipeline: `{}`},
	} {
		if _, err := toSpec(q); err == nil {
			t.Errorf("toSpec(%+v): expected an error", q)
		}
	}
}

func TestShellFormatting(t *testing.T) {
	sh := NewShell(nil)
	text, err := sh.FormatDocument(`{"_id":{"$oid":"6ac59d2ab231244641a263b2"},"n":{"$numberLong":"5"}}`)
	if err != nil || text != "{\n  _id: ObjectId(\"6ac59d2ab231244641a263b2\"),\n  n: NumberLong(5)\n}" {
		t.Errorf("FormatDocument = %q, %v", text, err)
	}
	if _, err := sh.FormatDocument("nope"); err == nil {
		t.Error("expected an error for invalid EJSON")
	}
	if msg := sh.ValidateDocument(`{a: 1}`); msg != "" {
		t.Errorf("valid document reported %q", msg)
	}
	if msg := sh.ValidateDocument(`{a: }`); msg == "" {
		t.Error("invalid document not reported")
	}
	q, err := sh.FormatQuery(shell.Query{Kind: "find", Filter: `{  a :1 }`})
	if err != nil || q.Filter != "{ a: 1 }" {
		t.Errorf("FormatQuery = %+v, %v", q, err)
	}
}

func TestFriendly(t *testing.T) {
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	tests := []struct {
		ctx  context.Context
		err  error
		want string
	}{
		{ctx, nil, ""},
		{ctx, context.DeadlineExceeded, "operation timed out"},
		{ctx, context.Canceled, "operation cancelled"},
		{cancelled, errors.New("socket closed"), "operation cancelled"},
		{ctx, errors.New("other"), "other"},
	}
	for _, tt := range tests {
		got := friendly(tt.ctx, tt.err)
		if (got == nil && tt.want != "") || (got != nil && got.Error() != tt.want) {
			t.Errorf("friendly(%v) = %v, want %q", tt.err, got, tt.want)
		}
	}
}

func TestServicesWithoutSession(t *testing.T) {
	store, err := profile.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := newConns(store)
	sh := c.sh
	ctx := t.Context()

	// Every session-bound call reports a closed connection instead of panicking.
	if _, err := c.ListDatabases(ctx, "nope"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("ListDatabases: %v", err)
	}
	if _, err := sh.ListCollections(ctx, "nope", "d"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("ListCollections: %v", err)
	}
	if _, err := sh.ListIndexes(ctx, "nope", "d", "c"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("ListIndexes: %v", err)
	}
	if err := sh.DropIndex(ctx, "nope", "d", "c", "i"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("DropIndex: %v", err)
	}
	if err := sh.DropCollection(ctx, "nope", "d", "c"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("DropCollection: %v", err)
	}
	if _, err := sh.Run(ctx, "nope", "d", "op", shell.RunRequest{}); !errors.Is(err, ErrNotConnected) {
		t.Errorf("Run: %v", err)
	}
	if _, err := sh.FetchPage(ctx, "nope", "op", shell.Query{Kind: "find"}, 0, 10); !errors.Is(err, ErrNotConnected) {
		t.Errorf("FetchPage: %v", err)
	}
	if _, err := sh.InsertDocument(ctx, "nope", "d", "c", "{}"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("InsertDocument: %v", err)
	}
	if sh.Cancel("nope", "op") {
		t.Error("Cancel on a closed session returned true")
	}
	if _, err := c.Connect(ctx, "missing"); !errors.Is(err, profile.ErrNotFound) {
		t.Errorf("Connect(missing) = %v", err)
	}
	if len(c.Sessions()) != 0 {
		t.Errorf("sessions = %+v", c.Sessions())
	}
	c.Disconnect("nope") // no-op
}

func TestDocumentEditValidation(t *testing.T) {
	sh := NewShell(docsession.NewRegistry())
	ctx := t.Context()
	id := `{"$numberInt":"1"}`
	if err := sh.SetField(ctx, "s", "d", "c", id, "_id", "2"); err == nil {
		t.Error("changing _id must be rejected")
	}
	if err := sh.SetField(ctx, "s", "d", "c", id, " ", "2"); err == nil {
		t.Error("an empty path must be rejected")
	}
	if err := sh.SetField(ctx, "s", "d", "c", id, "a", "{"); err == nil || !strings.Contains(err.Error(), "value") {
		t.Errorf("invalid value: %v", err)
	}
	if err := sh.UnsetField(ctx, "s", "d", "c", id, "_id"); err == nil {
		t.Error("removing _id must be rejected")
	}
	if err := sh.ReplaceDocument(ctx, "s", "d", "c", id, `{_id: 2}`); err == nil || !strings.Contains(err.Error(), "_id cannot be changed") {
		t.Errorf("replace with another _id: %v", err)
	}
	if _, err := sh.InsertDocument(ctx, "s", "d", "c", `[1]`); err == nil {
		t.Error("inserting an array must be rejected")
	}
	if _, err := sh.DeleteDocuments(ctx, "s", "d", "c", []string{"{bad"}); err == nil {
		t.Error("expected an error for a malformed id")
	}
}

// Package docapp contains the services of MongoDB and DocumentDB connections bound to
// the frontend: the mongo shell, documents, collections and indexes, and exports and
// imports of collections. The connections themselves are opened by app.Connections,
// which keeps them in a docsession.Registry shared with these services.
package docapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/iagapie/quire/internal/app"
	"github.com/iagapie/quire/internal/docsession"
	"github.com/iagapie/quire/internal/shell"
)

// Shell runs scripts, pages results, edits documents and reads the collections and
// indexes of MongoDB connections.
type Shell struct {
	sessions *docsession.Registry
}

// NewShell creates the service.
func NewShell(sessions *docsession.Registry) *Shell {
	return &Shell{sessions: sessions}
}

func (sh *Shell) begin(ctx context.Context, sessionID, opID string) (*docsession.Session, context.Context, func(), error) {
	s, err := sh.sessions.Get(sessionID)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, done := s.Begin(ctx, opID)
	return s, ctx, done, nil
}

func onRetry(opID string) func(int, error) {
	return func(attempt int, err error) {
		emit(app.EventRetry, app.RetryEvent{OpID: opID, Attempt: attempt, Error: err.Error()})
	}
}

// Run executes a script (or part of it) in database db. opID identifies the operation
// so it can be cancelled with Cancel.
func (sh *Shell) Run(ctx context.Context, sessionID, db, opID string, req shell.RunRequest) (shell.RunResult, error) {
	s, ctx, done, err := sh.begin(ctx, sessionID, opID)
	if err != nil {
		return shell.RunResult{}, err
	}
	defer done()
	return shell.Run(ctx, shell.Env{Session: s, Database: db, OnRetry: onRetry(opID)}, req), nil
}

// Cancel aborts a running operation.
func (sh *Shell) Cancel(sessionID, opID string) bool {
	s, err := sh.sessions.Get(sessionID)
	if err != nil {
		return false
	}
	return s.Cancel(opID)
}

func toSpec(q shell.Query) (shell.QuerySpec, error) {
	spec := shell.QuerySpec{Kind: q.Kind, Database: q.Database, Collection: q.Collection, Skip: q.Skip, Limit: q.Limit}
	var err error
	if q.Kind == "aggregate" {
		spec.Pipeline, err = shell.EvalArray(q.Pipeline, "pipeline")
		return spec, err
	}
	if spec.Filter, err = shell.EvalDoc(q.Filter, "filter"); err != nil {
		return spec, err
	}
	if spec.Projection, err = shell.EvalDoc(q.Projection, "projection"); err != nil {
		return spec, err
	}
	if spec.Sort, err = shell.EvalDoc(q.Sort, "sort"); err != nil {
		return spec, err
	}
	if spec.Skip < 0 || spec.Limit < 0 {
		return spec, errors.New("skip and limit must not be negative")
	}
	return spec, nil
}

// FetchPage re-runs a result query (possibly with an edited filter) for one page.
// No reconnect is needed: it reuses the live session and retries transient failures.
func (sh *Shell) FetchPage(ctx context.Context, sessionID, opID string, q shell.Query, offset, pageSize int64) (shell.Page, error) {
	spec, err := toSpec(q)
	if err != nil {
		return shell.Page{}, err
	}
	s, ctx, done, err := sh.begin(ctx, sessionID, opID)
	if err != nil {
		return shell.Page{}, err
	}
	defer done()
	if pageSize <= 0 {
		pageSize = 50
	}
	page, err := docsession.RetryRead(ctx, onRetry(opID), func(ctx context.Context) (shell.Page, error) {
		return shell.FetchPage(ctx, s.Client, spec, offset, pageSize)
	})
	return page, friendly(ctx, err)
}

// Count counts the documents matched by a result query.
func (sh *Shell) Count(ctx context.Context, sessionID, opID string, q shell.Query) (int64, error) {
	spec, err := toSpec(q)
	if err != nil {
		return 0, err
	}
	s, ctx, done, err := sh.begin(ctx, sessionID, opID)
	if err != nil {
		return 0, err
	}
	defer done()
	n, err := docsession.RetryRead(ctx, onRetry(opID), func(ctx context.Context) (int64, error) {
		return shell.Count(ctx, s.Client, spec)
	})
	return n, friendly(ctx, err)
}

// FormatQuery normalises user-edited query text (used to validate before running).
func (sh *Shell) FormatQuery(q shell.Query) (shell.Query, error) {
	spec, err := toSpec(q)
	if err != nil {
		return q, err
	}
	return *shell.TextQuery(spec), nil
}

func parseID(idJSON string) (any, error) {
	var w struct {
		V bson.RawValue `bson:"v"`
	}
	if err := bson.UnmarshalExtJSON([]byte(`{"v":`+idJSON+`}`), true, &w); err != nil {
		return nil, fmt.Errorf("invalid _id: %w", err)
	}
	var v any
	if err := w.V.Unmarshal(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func (sh *Shell) writable(ctx context.Context, sessionID string) (*mongo.Client, context.Context, func(), error) {
	s, ctx, done, err := sh.begin(ctx, sessionID, "")
	if err != nil {
		return nil, nil, nil, err
	}
	if s.Profile.ReadOnly {
		done()
		return nil, nil, nil, errReadOnly
	}
	return s.Client, ctx, done, nil
}

// InsertDocument inserts a document written in shell syntax and returns its _id as
// canonical Extended JSON.
func (sh *Shell) InsertDocument(ctx context.Context, sessionID, db, coll, text string) (string, error) {
	doc, err := shell.EvalDoc(text, "document")
	if err != nil {
		return "", err
	}
	client, ctx, done, err := sh.writable(ctx, sessionID)
	if err != nil {
		return "", err
	}
	defer done()
	if _, ok := find(doc, "_id"); !ok {
		doc = append(bson.D{{Key: "_id", Value: bson.NewObjectID()}}, doc...)
	}
	if _, err := client.Database(db).Collection(coll).InsertOne(ctx, doc); err != nil {
		return "", friendly(ctx, err)
	}
	id, _ := find(doc, "_id")
	return shell.CanonicalValue(id)
}

// ReplaceDocument replaces the document with the given _id.
func (sh *Shell) ReplaceDocument(ctx context.Context, sessionID, db, coll, idJSON, text string) error {
	id, err := parseID(idJSON)
	if err != nil {
		return err
	}
	doc, err := shell.EvalDoc(text, "document")
	if err != nil {
		return err
	}
	if newID, ok := find(doc, "_id"); ok {
		a, _ := shell.CanonicalValue(newID)
		b, _ := shell.CanonicalValue(id)
		if a != b {
			return errors.New("_id cannot be changed; insert a new document instead")
		}
	}
	client, ctx, done, err := sh.writable(ctx, sessionID)
	if err != nil {
		return err
	}
	defer done()
	res, err := client.Database(db).Collection(coll).ReplaceOne(ctx, bson.D{{Key: "_id", Value: id}}, doc)
	if err != nil {
		return friendly(ctx, err)
	}
	if res.MatchedCount == 0 {
		return errors.New("document no longer exists")
	}
	return nil
}

// SetField sets one field (dotted path) of a document to a value written in shell syntax.
func (sh *Shell) SetField(ctx context.Context, sessionID, db, coll, idJSON, path, valueText string) error {
	if strings.TrimSpace(path) == "" || path == "_id" {
		return errors.New("invalid field")
	}
	id, err := parseID(idJSON)
	if err != nil {
		return err
	}
	val, err := shell.Eval(valueText)
	if err != nil {
		return fmt.Errorf("value: %w", err)
	}
	return sh.update(ctx, sessionID, db, coll, id, bson.D{{Key: "$set", Value: bson.D{{Key: path, Value: val}}}})
}

// UnsetField removes one field (dotted path) of a document.
func (sh *Shell) UnsetField(ctx context.Context, sessionID, db, coll, idJSON, path string) error {
	if strings.TrimSpace(path) == "" || path == "_id" {
		return errors.New("invalid field")
	}
	id, err := parseID(idJSON)
	if err != nil {
		return err
	}
	return sh.update(ctx, sessionID, db, coll, id, bson.D{{Key: "$unset", Value: bson.D{{Key: path, Value: ""}}}})
}

func (sh *Shell) update(ctx context.Context, sessionID, db, coll string, id any, update bson.D) error {
	client, ctx, done, err := sh.writable(ctx, sessionID)
	if err != nil {
		return err
	}
	defer done()
	res, err := client.Database(db).Collection(coll).UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, update)
	if err != nil {
		return friendly(ctx, err)
	}
	if res.MatchedCount == 0 {
		return errors.New("document no longer exists")
	}
	return nil
}

// DeleteDocuments deletes documents by _id and returns how many were removed.
func (sh *Shell) DeleteDocuments(ctx context.Context, sessionID, db, coll string, idsJSON []string) (int64, error) {
	ids := make(bson.A, 0, len(idsJSON))
	for _, j := range idsJSON {
		id, err := parseID(j)
		if err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	client, ctx, done, err := sh.writable(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	defer done()
	res, err := client.Database(db).Collection(coll).DeleteMany(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}})
	if err != nil {
		return 0, friendly(ctx, err)
	}
	return res.DeletedCount, nil
}

// GetDocument returns a document by _id as canonical Extended JSON.
func (sh *Shell) GetDocument(ctx context.Context, sessionID, db, coll, idJSON string) (string, error) {
	id, err := parseID(idJSON)
	if err != nil {
		return "", err
	}
	s, ctx, done, err := sh.begin(ctx, sessionID, "")
	if err != nil {
		return "", err
	}
	defer done()
	raw, err := docsession.RetryRead(ctx, nil, func(ctx context.Context) (bson.Raw, error) {
		return s.Client.Database(db).Collection(coll).FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Raw()
	})
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return "", errors.New("document no longer exists")
		}
		return "", friendly(ctx, err)
	}
	out, err := bson.MarshalExtJSON(raw, true, false)
	return string(out), err
}

// FormatDocument renders canonical Extended JSON as editable shell syntax.
func (sh *Shell) FormatDocument(ejson string) (string, error) {
	var d bson.D
	if err := bson.UnmarshalExtJSON([]byte(ejson), true, &d); err != nil {
		return "", err
	}
	return shell.Format(d, "  "), nil
}

// ValidateDocument checks shell-syntax document text, returning an error message or "".
func (sh *Shell) ValidateDocument(text string) string {
	if _, err := shell.EvalDoc(text, "document"); err != nil {
		return err.Error()
	}
	return ""
}

func find(d bson.D, key string) (any, bool) {
	for _, e := range d {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

func friendly(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return errors.New("operation cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("operation timed out")
	}
	return err
}

// ListCollections lists collections and views of a database.
func (sh *Shell) ListCollections(ctx context.Context, sessionID, db string) ([]docsession.Collection, error) {
	s, ctx, done, err := sh.begin(ctx, sessionID, "")
	if err != nil {
		return nil, err
	}
	defer done()
	return docsession.RetryRead(ctx, nil, func(ctx context.Context) ([]docsession.Collection, error) {
		return s.ListCollections(ctx, db)
	})
}

// ListIndexes lists the indexes of a collection.
func (sh *Shell) ListIndexes(ctx context.Context, sessionID, db, coll string) ([]docsession.Index, error) {
	s, ctx, done, err := sh.begin(ctx, sessionID, "")
	if err != nil {
		return nil, err
	}
	defer done()
	return docsession.RetryRead(ctx, nil, func(ctx context.Context) ([]docsession.Index, error) {
		return s.ListIndexes(ctx, db, coll)
	})
}

// DropIndex drops an index by name.
func (sh *Shell) DropIndex(ctx context.Context, sessionID, db, coll, name string) error {
	client, ctx, done, err := sh.writable(ctx, sessionID)
	if err != nil {
		return err
	}
	defer done()
	return client.Database(db).Collection(coll).Indexes().DropOne(ctx, name)
}

// DropCollection drops a collection or view (the UI asks for confirmation first).
func (sh *Shell) DropCollection(ctx context.Context, sessionID, db, coll string) error {
	client, ctx, done, err := sh.writable(ctx, sessionID)
	if err != nil {
		return err
	}
	defer done()
	return friendly(ctx, client.Database(db).Collection(coll).Drop(ctx))
}

var errReadOnly = errors.New("this connection is read-only")

func emit(name string, data any) {
	if a := application.Get(); a != nil {
		a.Event.Emit(name, data)
	}
}

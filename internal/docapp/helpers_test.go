package docapp

import (
	"context"

	"github.com/iagapie/quire/internal/app"
	"github.com/iagapie/quire/internal/docsession"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/query"
)

// ErrNotConnected is what calls on a closed session return.
var ErrNotConnected = docsession.ErrNotConnected

// testConns is the connection service of the app with the services of this package,
// as main wires them.
type testConns struct {
	*app.Connections
	sh    *Shell
	docs  *docsession.Registry
	store *profile.Store
}

func newConns(store *profile.Store) *testConns {
	docs := docsession.NewRegistry()
	return &testConns{Connections: app.NewConnections(store, query.New(), docs), sh: NewShell(docs), docs: docs, store: store}
}

func (c *testConns) get(id string) (*docsession.Session, error) { return c.docs.Get(id) }

func (c *testConns) ListIndexes(ctx context.Context, sessionID, db, coll string) ([]docsession.Index, error) {
	return c.sh.ListIndexes(ctx, sessionID, db, coll)
}

func (c *testConns) DropIndex(ctx context.Context, sessionID, db, coll, name string) error {
	return c.sh.DropIndex(ctx, sessionID, db, coll, name)
}

func (c *testConns) GetIndex(ctx context.Context, sessionID, db, coll, name string) (IndexDetails, error) {
	return c.sh.GetIndex(ctx, sessionID, db, coll, name)
}

func (c *testConns) SaveIndex(ctx context.Context, sessionID, db, coll, original, spec string) (IndexSaveResult, error) {
	return c.sh.SaveIndex(ctx, sessionID, db, coll, original, spec)
}

func (c *testConns) ListCollections(ctx context.Context, sessionID, db string) ([]docsession.Collection, error) {
	return c.sh.ListCollections(ctx, sessionID, db)
}

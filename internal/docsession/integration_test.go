package docsession

import (
	"os"
	"slices"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/iagapie/quire/internal/profile"
)

// QUIRE_TEST_MONGODB=127.0.0.1:57017 go test ./internal/docsession (root user root/rootpw).
func openTestSession(t *testing.T, p profile.Profile) *Session {
	t.Helper()
	host := os.Getenv("QUIRE_TEST_MONGODB")
	if host == "" {
		t.Skip("QUIRE_TEST_MONGODB not set")
	}
	p.Driver, p.Host = "mongodb", host
	if p.Options == nil {
		p.Options = map[string]string{}
	}
	p.Options["directConnection"] = "true"
	p.Normalize()
	var health []Health
	s, err := Open(t.Context(), "test", p, Options{OnHealth: func(h Health) { health = append(health, h) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		if len(health) == 0 || health[len(health)-1] != HealthClosed {
			t.Errorf("health transitions = %v, want to end with closed", health)
		}
	})
	return s
}

func TestIntegrationSessionExplorer(t *testing.T) {
	s := openTestSession(t, profile.Profile{User: "root", Password: "rootpw", Options: map[string]string{"extraDatabases": "not_created_yet"}})
	ctx := t.Context()
	db := s.Client.Database("session_test")
	if err := db.Drop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Collection("items").InsertOne(ctx, bson.D{{Key: "sku", Value: "a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Collection("items").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "sku", Value: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateView(ctx, "items_view", "items", bson.A{}); err != nil {
		t.Fatal(err)
	}

	dbs, err := s.ListDatabases(ctx)
	if err != nil || !slices.Contains(dbs, "session_test") || !slices.Contains(dbs, "not_created_yet") || !slices.IsSorted(dbs) {
		t.Fatalf("databases = %v, %v", dbs, err)
	}

	colls, err := s.ListCollections(ctx, "session_test")
	if err != nil {
		t.Fatal(err)
	}
	want := []Collection{{Name: "items", Type: "collection"}, {Name: "items_view", Type: "view"}}
	got := slices.DeleteFunc(colls, func(c Collection) bool { return c.Name == "system.views" })
	if !slices.Equal(got, want) {
		t.Errorf("collections = %+v", colls)
	}

	idx, err := s.ListIndexes(ctx, "session_test", "items")
	if err != nil || len(idx) != 2 || idx[1].Name != "sku_1" || idx[1].Keys != `{"sku":1}` {
		t.Errorf("indexes = %+v, %v", idx, err)
	}
	if s.Health() != HealthOnline || s.Host == "" {
		t.Errorf("health=%v host=%q", s.Health(), s.Host)
	}
}

func TestIntegrationOpenFailures(t *testing.T) {
	host := os.Getenv("QUIRE_TEST_MONGODB")
	if host == "" {
		t.Skip("QUIRE_TEST_MONGODB not set")
	}
	p := profile.Profile{Driver: "mongodb", Host: host, Options: map[string]string{"directConnection": "true"}, User: "root", Password: "wrong"}
	p.Normalize()
	if _, err := Open(t.Context(), "x", p, Options{}); err == nil {
		t.Error("expected an authentication error")
	}
}

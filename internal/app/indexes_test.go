package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
)

func TestIndexEditor(t *testing.T) {
	for _, srv := range servers(t) {
		t.Run(srv.name, func(t *testing.T) {
			c, id := connect(t, srv.profile)
			s, _ := c.get(id)
			if !s.Info.Index.Editable {
				t.Skip("indexes are not editable on " + srv.name)
			}
			db, _ := s.DB(t.Context(), srv.database)
			for _, q := range srv.cleanup {
				_, _ = db.ExecContext(t.Context(), q)
			}
			for _, q := range srv.setup {
				if _, err := db.ExecContext(t.Context(), q); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				for _, q := range srv.cleanup {
					_, _ = db.ExecContext(t.Context(), q)
				}
			})
			ix := dialect.Index{Name: srv.id("t_src_name"), Table: srv.id("t_src"), Columns: []dialect.IndexColumn{{Name: srv.id("name")}, {Name: srv.id("price"), Desc: true}}}
			plan, err := c.PlanIndex(id, srv.database, srv.schema, "", ix, IndexOptions{})
			if err != nil || len(plan.Statements) != 1 || !strings.Contains(plan.Statements[0], srv.id("t_src_name")) {
				t.Fatalf("plan = %+v, %v", plan, err)
			}
			if _, err := c.SaveIndex(t.Context(), id, "", srv.database, srv.schema, "", ix, IndexOptions{}); err != nil {
				t.Fatal(err)
			}
			// Changing it drops and creates it, in one transaction where DDL is transactional.
			ix.Unique, ix.Columns = true, []dialect.IndexColumn{{Name: srv.id("id")}, {Name: srv.id("name")}}
			plan, err = c.SaveIndex(t.Context(), id, "", srv.database, srv.schema, srv.id("t_src_name"), ix, IndexOptions{})
			if err != nil || len(plan.Statements) != 2 || plan.Atomic != s.Info.TransactionalDDL {
				t.Fatalf("replace = %+v, %v", plan, err)
			}
			ixs, err := s.Driver.Indexes(t.Context(), db, srv.database, srv.schema, srv.id("t_src"))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, x := range ixs {
				if x.Name == srv.id("t_src_name") {
					found = x.Unique && len(x.Columns) == 2 && x.Columns[0].Name == srv.id("id")
				}
			}
			if !found {
				t.Fatalf("indexes after the change = %+v", ixs)
			}
			// A failing replacement leaves the old index where DDL is transactional.
			// (SQLite reads an unknown quoted column as a string, so the test uses a function.)
			bad := ix
			bad.Columns = []dialect.IndexColumn{{Name: "no_such_column"}}
			if s.Info.ID == "sqlite" {
				bad.Columns = []dialect.IndexColumn{{Expression: "no_such_function(name)"}}
			}
			if _, err := c.SaveIndex(t.Context(), id, "", srv.database, srv.schema, srv.id("t_src_name"), bad, IndexOptions{}); err == nil {
				t.Fatal("an invalid index was created")
			}
			if s.Info.TransactionalDDL {
				ixs, _ = s.Driver.Indexes(t.Context(), db, srv.database, srv.schema, srv.id("t_src"))
				kept := false
				for _, x := range ixs {
					kept = kept || x.Name == srv.id("t_src_name")
				}
				if !kept {
					t.Error("the old index was lost")
				}
			}
			if !s.Info.TransactionalDDL {
				return // the failed replacement dropped it
			}
			if err := c.DropIndex(t.Context(), id, srv.database, srv.schema, srv.id("t_src"), srv.id("t_src_name"), false); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIndexEditorRefusesReadOnly(t *testing.T) {
	c, id := connect(t, profile.Profile{Name: "ro", Driver: "sqlite", Database: filepath.Join(t.TempDir(), "x.db"), Options: map[string]string{"create": "true"}})
	s, _ := c.get(id)
	s.Profile.ReadOnly = true
	if _, err := c.SaveIndex(t.Context(), id, "", "main", "", "", dialect.Index{Name: "i", Table: "t", Columns: []dialect.IndexColumn{{Name: "a"}}}, IndexOptions{}); err == nil {
		t.Error("read-only connection changed an index")
	}
}

func TestTableQueryLimit(t *testing.T) {
	c, id := connect(t, profile.Profile{Name: "q", Driver: "sqlite", Database: filepath.Join(t.TempDir(), "q.db"), Options: map[string]string{"create": "true"}})
	q, err := c.TableQuery(t.Context(), id, "main", "", "items")
	if err != nil || !strings.Contains(q, "LIMIT 1000") {
		t.Errorf("sqlite = %q, %v", q, err)
	}
	ora, _ := dialect.Get("oracle")
	if l, ok := ora.(dialect.TableLimiter); !ok || l.TableRowLimit() != 100 {
		t.Error("Oracle opens tables with 100 rows")
	}
}

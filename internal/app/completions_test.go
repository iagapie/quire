package app

import (
	"context"
	"slices"
	"testing"
)

func TestCompletionsListColumnsAndJoins(t *testing.T) {
	for _, srv := range servers(t) {
		t.Run(srv.name, func(t *testing.T) {
			c, id := connect(t, srv.profile)
			s, _ := c.get(id)
			db, _ := s.DB(t.Context(), srv.database)
			drop := []string{"DROP TABLE IF EXISTS q_child", "DROP TABLE IF EXISTS q_parent"}
			for _, q := range drop {
				_, _ = db.ExecContext(t.Context(), q)
			}
			t.Cleanup(func() {
				for _, q := range drop {
					_, _ = db.ExecContext(context.Background(), q)
				}
			})
			create := srv.completion
			if create == nil {
				create = []string{
					"CREATE TABLE q_parent (id INT PRIMARY KEY, code VARCHAR(10))",
					"CREATE TABLE q_child (id INT PRIMARY KEY, parent_id INT, note VARCHAR(10), FOREIGN KEY (parent_id) REFERENCES q_parent (id))",
				}
			}
			for _, q := range create {
				if _, err := db.ExecContext(t.Context(), q); err != nil {
					t.Fatal(err)
				}
			}
			info, err := c.Completions(t.Context(), id, srv.database, srv.schema)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(info.Tables[srv.id("q_child")], srv.ids("id", "parent_id", "note")) {
				t.Errorf("columns = %v", info.Tables[srv.id("q_child")])
			}
			if srv.noKeys {
				return // no foreign keys
			}
			found := false
			for _, j := range info.Joins {
				if j.Table == srv.id("q_child") && j.RefTable == srv.id("q_parent") && slices.Equal(j.Columns, srv.ids("parent_id")) && slices.Equal(j.RefColumns, srv.ids("id")) {
					found = true
				}
			}
			if !found {
				t.Errorf("joins = %+v", info.Joins)
			}
		})
	}
}

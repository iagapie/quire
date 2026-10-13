package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/docsession"
	"github.com/iagapie/quire/internal/session"
)

// friendlyConnect turns driver connection errors into actionable messages.
func friendlyConnect(err error) error {
	if err == nil {
		return nil
	}
	if h, ok := err.(interface{ Hint() string }); ok && h.Hint() != "" {
		return fmt.Errorf("%w\n\n%s", err, h.Hint())
	}
	return err
}

// catalog runs a metadata read with the profile timeout and read retries, on the pool
// of database.
func catalog[T any](ctx context.Context, c *Connections, sessionID, database string, fn func(context.Context, *session.Session, dialect.Querier) (T, error)) (T, error) {
	var zero T
	s, err := c.get(sessionID)
	if err != nil {
		return zero, err
	}
	ctx, done := s.Begin(ctx, "")
	defer done()
	return session.RetryRead(ctx, s, nil, func(ctx context.Context) (T, error) {
		db, err := s.DB(ctx, database)
		if err != nil {
			return zero, err
		}
		return fn(ctx, s, db)
	})
}

// ListDatabases lists the databases of a connection.
func (c *Connections) ListDatabases(ctx context.Context, sessionID string) ([]string, error) {
	if s, err := c.docs.Get(sessionID); err == nil {
		ctx, done := s.Begin(ctx, "")
		defer done()
		return docsession.RetryRead(ctx, nil, func(ctx context.Context) ([]string, error) { return s.ListDatabases(ctx) })
	}
	return catalog(ctx, c, sessionID, "", func(ctx context.Context, s *session.Session, q dialect.Querier) ([]string, error) {
		dbs, err := s.Driver.Databases(ctx, q)
		if err != nil && s.Database != "" && !s.Driver.Transient(err) {
			// Without the right to list databases, show the one we are connected to.
			return []string{s.Database}, nil
		}
		return dbs, err
	})
}

// ListSchemas lists the schemas of a database.
func (c *Connections) ListSchemas(ctx context.Context, sessionID, database string) ([]string, error) {
	return catalog(ctx, c, sessionID, database, func(ctx context.Context, s *session.Session, q dialect.Querier) ([]string, error) {
		return s.Driver.Schemas(ctx, q, database)
	})
}

// ListObjects lists the tables, views, routines... of a schema.
func (c *Connections) ListObjects(ctx context.Context, sessionID, database, schema string) ([]dialect.Object, error) {
	return catalog(ctx, c, sessionID, database, func(ctx context.Context, s *session.Session, q dialect.Querier) ([]dialect.Object, error) {
		return s.Driver.Objects(ctx, q, database, schema)
	})
}

// TableDetails is what the tree shows under a table.
type TableDetails struct {
	Columns     []dialect.Column     `json:"columns"`
	Indexes     []dialect.Index      `json:"indexes"`
	Constraints []dialect.Constraint `json:"constraints"`
}

// GetTable returns the columns, indexes and constraints of a table (or the columns of
// a view).
func (c *Connections) GetTable(ctx context.Context, sessionID, database, schema, table string, view bool) (TableDetails, error) {
	return catalog(ctx, c, sessionID, database, func(ctx context.Context, s *session.Session, q dialect.Querier) (TableDetails, error) {
		var d TableDetails
		var err error
		if d.Columns, err = s.Driver.Columns(ctx, q, database, schema, table); err != nil {
			return d, err
		}
		if view {
			return d, nil
		}
		if d.Indexes, err = s.Driver.Indexes(ctx, q, database, schema, table); err != nil {
			return d, err
		}
		d.Constraints, err = s.Driver.Constraints(ctx, q, database, schema, table)
		return d, err
	})
}

// GetDDL returns the definition of an object.
func (c *Connections) GetDDL(ctx context.Context, sessionID, database, schema string, obj dialect.Object) (string, error) {
	return catalog(ctx, c, sessionID, database, func(ctx context.Context, s *session.Session, q dialect.Querier) (string, error) {
		return s.Driver.DDL(ctx, q, database, schema, obj)
	})
}

// Dropper is implemented by drivers that need special DROP statements.
type Dropper interface {
	DropObject(database, schema string, obj dialect.Object) string
}

var dropKinds = map[string]string{
	dialect.KindTable: "TABLE", dialect.KindView: "VIEW", dialect.KindMatView: "MATERIALIZED VIEW",
	dialect.KindFunction: "FUNCTION", dialect.KindProcedure: "PROCEDURE", dialect.KindSequence: "SEQUENCE",
	dialect.KindTrigger: "TRIGGER", dialect.KindPackage: "PACKAGE",
}

// DropStatement returns the statement dropping an object.
func (c *Connections) DropStatement(sessionID, database, schema string, obj dialect.Object) (string, error) {
	s, err := c.get(sessionID)
	if err != nil {
		return "", err
	}
	return dropStatement(s.Driver, database, schema, obj)
}

func dropStatement(drv dialect.SQLDriver, database, schema string, obj dialect.Object) (string, error) {
	if d, ok := drv.(Dropper); ok {
		if st := d.DropObject(database, schema, obj); st != "" {
			return st, nil
		}
	}
	kind, ok := dropKinds[obj.Kind]
	if !ok {
		return "", fmt.Errorf("cannot drop a %s", obj.Kind)
	}
	return "DROP " + kind + " " + drv.Qualify(database, schema, obj.Name), nil
}

// DropObject drops a table, view, routine... (the UI asks for confirmation first).
func (c *Connections) DropObject(ctx context.Context, sessionID, database, schema string, obj dialect.Object) error {
	s, err := c.get(sessionID)
	if err != nil {
		return err
	}
	if s.Profile.ReadOnly {
		return errReadOnly
	}
	stmt, err := dropStatement(s.Driver, database, schema, obj)
	if err != nil {
		return err
	}
	ctx, done := s.Begin(ctx, "")
	defer done()
	db, err := s.DB(ctx, database)
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if poolFor(s, database) == "" {
		for _, u := range s.Driver.Use(database, "") {
			if _, err := conn.ExecContext(ctx, u); err != nil {
				return err
			}
		}
	}
	_, err = conn.ExecContext(ctx, stmt)
	return err
}

func poolFor(s *session.Session, database string) string {
	if pd, ok := s.Driver.(dialect.PerDatabase); ok && pd.PerDatabasePools() && database != s.Database {
		return database
	}
	return ""
}

// GenerateSQL writes a SELECT, INSERT, UPDATE or DELETE template for a table.
func (c *Connections) GenerateSQL(ctx context.Context, sessionID, database, schema, table, kind string) (string, error) {
	cols, err := catalog(ctx, c, sessionID, database, func(ctx context.Context, s *session.Session, q dialect.Querier) ([]dialect.Column, error) {
		return s.Driver.Columns(ctx, q, database, schema, table)
	})
	if err != nil {
		return "", err
	}
	s, err := c.get(sessionID)
	if err != nil {
		return "", err
	}
	name := dialect.ReadableName(s.Driver, qualifier(s, database), schema, table)
	if g, ok := s.Driver.(dialect.Generator); ok {
		return g.GenerateSQL(name, cols, kind)
	}
	return generateSQL(s.Driver, name, cols, kind)
}

// qualifier returns the database part of qualified names: needed for drivers whose
// databases share a connection (SQLite attachments, MySQL), not for PostgreSQL.
func qualifier(s *session.Session, database string) string {
	if pd, ok := s.Driver.(dialect.PerDatabase); ok && pd.PerDatabasePools() {
		return ""
	}
	if database == s.Database {
		return ""
	}
	return database
}

func generateSQL(syn dialect.Syntax, table string, cols []dialect.Column, kind string) (string, error) {
	var names, sets, values, keys []string
	for _, c := range cols {
		q := dialect.Readable(syn, c.Name)
		names = append(names, q)
		if c.PK > 0 {
			keys = append(keys, q+" = ?")
		}
		if c.Generated {
			continue
		}
		values = append(values, "?")
		sets = append(sets, q+" = ?")
	}
	if len(keys) == 0 && len(names) > 0 {
		keys = []string{names[0] + " = ?"}
	}
	writable := []string{}
	for _, c := range cols {
		if !c.Generated {
			writable = append(writable, dialect.Readable(syn, c.Name))
		}
	}
	switch kind {
	case "select":
		return "SELECT " + strings.Join(names, ", ") + "\nFROM " + table + "\nWHERE " + strings.Join(keys, " AND ") + ";\n", nil
	case "insert":
		return "INSERT INTO " + table + " (" + strings.Join(writable, ", ") + ")\nVALUES (" + strings.Join(values, ", ") + ");\n", nil
	case "update":
		return "UPDATE " + table + "\nSET " + strings.Join(sets, ",\n    ") + "\nWHERE " + strings.Join(keys, " AND ") + ";\n", nil
	case "delete":
		return "DELETE FROM " + table + "\nWHERE " + strings.Join(keys, " AND ") + ";\n", nil
	}
	return "", fmt.Errorf("unknown statement %q", kind)
}

type CompletionInfo struct {
	Schemas []string `json:"schemas"`
	// Tables maps table and view names to their columns.
	Tables map[string][]string `json:"tables"`
	// Other maps the other schemas to their table names (without columns).
	Other map[string][]string `json:"other"`
	// Joins are the foreign keys of the schema, for JOIN … ON completion.
	Joins []dialect.ForeignKey `json:"joins"`
}

// completionSchemas bounds the other schemas whose tables are listed.
const completionSchemas = 30

// Completions returns schemas, tables and columns for the editor of database/schema.
func (c *Connections) Completions(ctx context.Context, sessionID, database, schema string) (CompletionInfo, error) {
	return catalog(ctx, c, sessionID, database, func(ctx context.Context, s *session.Session, q dialect.Querier) (CompletionInfo, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		out := CompletionInfo{Tables: map[string][]string{}}
		if s.Info.Schemas {
			out.Schemas, _ = s.Driver.Schemas(ctx, q, database)
		}
		if fk, ok := s.Driver.(dialect.ForeignKeys); ok {
			out.Joins, _ = fk.ForeignKeys(ctx, q, database, schema)
		}
		if s.Info.Schemas && s.Info.ID != "oracle" {
			out.Other = map[string][]string{}
			n := 0
			for _, other := range out.Schemas {
				if other == schema || n >= completionSchemas || systemSchema(other) {
					continue
				}
				n++
				if objs, err := s.Driver.Objects(ctx, q, database, other); err == nil {
					for _, o := range objs {
						if o.Kind == dialect.KindTable || o.Kind == dialect.KindView || o.Kind == dialect.KindMatView {
							out.Other[other] = append(out.Other[other], o.Name)
						}
					}
				}
			}
		}
		if sc, ok := s.Driver.(dialect.SchemaColumns); ok {
			cols, err := sc.SchemaColumns(ctx, q, database, schema)
			if err == nil {
				out.Tables = cols
				return out, nil
			}
		}
		objs, err := s.Driver.Objects(ctx, q, database, schema)
		if err != nil {
			return out, err
		}
		n := 0
		for _, o := range objs {
			if o.Kind != dialect.KindTable && o.Kind != dialect.KindView && o.Kind != dialect.KindMatView && o.Kind != dialect.KindContainer {
				continue
			}
			out.Tables[o.Name] = nil
			if n < 300 {
				if cols, err := s.Driver.Columns(ctx, q, database, schema, o.Name); err == nil {
					for _, c := range cols {
						out.Tables[o.Name] = append(out.Tables[o.Name], c.Name)
					}
				}
				n++
			}
		}
		return out, nil
	})
}

// systemSchema reports schemas of the database system, left out of completion.
func systemSchema(s string) bool {
	switch strings.ToLower(s) {
	case "pg_catalog", "information_schema", "pg_toast", "sys", "guest":
		return true
	}
	return strings.HasPrefix(s, "pg_temp") || strings.HasPrefix(s, "pg_toast")
}

// TableQuery is the statement that opens a table: its rows with the default limit.
func (c *Connections) TableQuery(ctx context.Context, sessionID, database, schema, table string) (string, error) {
	s, err := c.get(sessionID)
	if err != nil {
		return "", err
	}
	name := dialect.ReadableName(s.Driver, qualifier(s, database), schema, table)
	limit := int64(TableRowLimit)
	if l, ok := s.Driver.(dialect.TableLimiter); ok {
		limit = l.TableRowLimit()
	}
	if o, ok := s.Driver.(dialect.TableOpener); ok {
		cols, err := catalog(ctx, c, sessionID, database, func(ctx context.Context, s *session.Session, q dialect.Querier) ([]dialect.Column, error) {
			return s.Driver.Columns(ctx, q, database, schema, table)
		})
		if err != nil {
			cols = nil // the query works without the column list
		}
		return o.OpenTable(name, cols, limit) + "\n", nil
	}
	q := "SELECT * FROM " + name
	return s.Driver.LimitRows(q, limit) + ";\n", nil
}

// TableRowLimit is the row limit of the query opening a table, unless the driver has
// its own (dialect.TableLimiter).
const TableRowLimit = 1000

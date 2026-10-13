// Package cockroach is the CockroachDB driver: the PostgreSQL driver (pgx v5) with the
// catalog parts CockroachDB answers differently.
package cockroach

import (
	"context"
	"database/sql"
	"regexp"
	"strings"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/dialect/postgres"
)

func init() {
	dialect.Register(&Driver{postgres.New(info())})
}

// Driver is the CockroachDB driver.
type Driver struct {
	*postgres.Driver
}

func info() dialect.Info {
	i := postgres.Info()
	i.ID, i.Label = "cockroachdb", "CockroachDB"
	i.Description = "CockroachDB 23.1 and later, self-hosted or CockroachDB Cloud"
	i.DefaultPort, i.DefaultUser, i.DefaultDatabase = 26257, "root", "defaultdb"
	// Schema changes run as background jobs and commit on their own
	// (autocommit_before_ddl), so a failed CREATE INDEX cannot bring back the old one.
	i.TransactionalDDL = false
	i.Index = dialect.IndexFeatures{
		Methods: []string{"btree", "gin"},
		Desc:    true, Partial: true, Include: true, Invisible: true, Expressions: true, IfNotExists: true, Editable: true,
	}
	i.Order = 11
	return i
}

var versionRe = regexp.MustCompile(`v\d+\.\d+(\.\d+)?\S*`)

// Version returns the CockroachDB release (server_version is the PostgreSQL one it
// mimics).
func (d *Driver) Version(ctx context.Context, q dialect.Querier) (string, error) {
	var v string
	if err := q.QueryRowContext(ctx, "SELECT version()").Scan(&v); err != nil {
		return "", err
	}
	if m := versionRe.FindString(v); m != "" {
		return m, nil
	}
	return v, nil
}

// Schemas lists the schemas, CockroachDB's system schemas last.
func (d *Driver) Schemas(ctx context.Context, q dialect.Querier, _ string) ([]string, error) {
	return dialect.Strings(ctx, q, `SELECT nspname FROM pg_namespace
WHERE nspname NOT LIKE 'pg\_temp\_%'
ORDER BY nspname IN ('crdb_internal', 'pg_extension', 'pg_catalog', 'information_schema'), nspname`)
}

// hidden lists the hidden columns of a table (rowid of tables without a primary key,
// the columns of expression indexes).
func hidden(ctx context.Context, q dialect.Querier, schema, table string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT column_name FROM information_schema.columns
WHERE table_schema = $1 AND table_name = $2 AND is_hidden = 'YES'`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out[c] = true
	}
	return out, rows.Err()
}

// Columns lists the visible columns of a table.
func (d *Driver) Columns(ctx context.Context, q dialect.Querier, database, schema, table string) ([]dialect.Column, error) {
	cols, err := d.Driver.Columns(ctx, q, database, schema, table)
	if err != nil {
		return nil, err
	}
	skip, err := hidden(ctx, q, schema, table)
	if err != nil || len(skip) == 0 {
		return cols, err
	}
	out := cols[:0]
	for _, c := range cols {
		if !skip[c.Name] {
			out = append(out, c)
		}
	}
	return out, nil
}

// SchemaColumns lists the visible columns of every table and view of a schema.
func (d *Driver) SchemaColumns(ctx context.Context, q dialect.Querier, _, schema string) (map[string][]string, error) {
	return dialect.ColumnPairs(ctx, q, `SELECT table_name, column_name FROM information_schema.columns
WHERE table_schema = $1 AND is_hidden = 'NO' ORDER BY table_name, ordinal_position`, schema)
}

// Indexes lists the indexes with CockroachDB's names for the access methods; NULLS
// FIRST/LAST cannot be chosen, and NOT VISIBLE indexes are marked.
func (d *Driver) Indexes(ctx context.Context, q dialect.Querier, database, schema, table string) ([]dialect.Index, error) {
	ixs, err := d.Driver.Indexes(ctx, q, database, schema, table)
	if err != nil {
		return nil, err
	}
	invisible := map[string]bool{}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT index_name FROM [SHOW INDEXES FROM `+d.Qualify("", schema, table)+`] WHERE NOT visible`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		invisible[n] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range ixs {
		ix := &ixs[i]
		switch ix.Method {
		case "prefix":
			ix.Method = "btree"
		case "inverted":
			ix.Method = "gin"
		}
		ix.Invisible = invisible[ix.Name]
		for k := range ix.Columns {
			c := &ix.Columns[k]
			c.Nulls = ""
			if c.Expression != "" {
				c.Expression = trimParens(c.Expression)
			}
		}
	}
	return ixs, nil
}

func trimParens(s string) string {
	s = strings.TrimSpace(s)
	for len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' && balanced(s[1:len(s)-1]) {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

func balanced(s string) bool {
	depth := 0
	for _, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

// CreateIndex adds NOT VISIBLE to the PostgreSQL statement.
func (d *Driver) CreateIndex(database, schema string, ix dialect.Index, concurrently, ifNotExists bool) (string, error) {
	s, err := d.Driver.CreateIndex(database, schema, ix, concurrently, ifNotExists)
	if err == nil && ix.Invisible {
		s += " NOT VISIBLE"
	}
	return s, err
}

// DDL returns CockroachDB's own SHOW CREATE text, which keeps its index and column
// syntax (STORING, computed columns, hash sharding).
func (d *Driver) DDL(ctx context.Context, q dialect.Querier, database, schema string, obj dialect.Object) (string, error) {
	var stmt string
	switch obj.Kind {
	case dialect.KindFunction, dialect.KindProcedure:
		return d.Driver.DDL(ctx, q, database, schema, obj)
	case dialect.KindTrigger:
		stmt = "SHOW CREATE TRIGGER " + d.Quote(obj.Name) + " ON " + d.Qualify("", schema, obj.Table)
	default:
		stmt = "SHOW CREATE " + d.Qualify("", schema, obj.Name)
	}
	var name string
	var def sql.NullString
	if err := q.QueryRowContext(ctx, stmt).Scan(&name, &def); err != nil {
		return "", err
	}
	s := strings.TrimRight(def.String, "\n ")
	if !strings.HasSuffix(s, ";") {
		s += ";"
	}
	return s + "\n", nil
}

// TableStats reads the row estimates of SHOW TABLES (sizes are not available without
// the internal tables).
func (d *Driver) TableStats(ctx context.Context, q dialect.Querier, database, schema string) ([]dialect.TableStat, error) {
	// database.schema: a bare name would be read as a database first.
	from := dialect.Join(d.Quote, database, schema)
	rows, err := q.QueryContext(ctx, `SELECT table_name, type, COALESCE(estimated_row_count, -1)
FROM [SHOW TABLES FROM `+from+`] WHERE type IN ('table', 'view', 'materialized view') ORDER BY table_name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.TableStat
	for rows.Next() {
		var t dialect.TableStat
		if err := rows.Scan(&t.Name, &t.Kind, &t.Rows); err != nil {
			return nil, err
		}
		switch t.Kind {
		case "view":
			t.Rows = -1
		case "materialized view":
			t.Kind = dialect.KindMatView
		}
		t.Bytes = -1
		out = append(out, t)
	}
	return out, rows.Err()
}

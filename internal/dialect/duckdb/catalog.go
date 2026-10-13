package duckdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/iagapie/quire/internal/dialect"
)

// Databases lists the attached databases.
func (d *Driver) Databases(ctx context.Context, q dialect.Querier) ([]string, error) {
	return dialect.Strings(ctx, q, `SELECT database_name FROM duckdb_databases() WHERE NOT internal ORDER BY database_name`)
}

// Schemas lists the schemas of a database, system schemas last.
func (d *Driver) Schemas(ctx context.Context, q dialect.Querier, database string) ([]string, error) {
	return dialect.Strings(ctx, q, `SELECT schema_name FROM duckdb_schemas() WHERE database_name = ?
ORDER BY schema_name IN ('information_schema', 'pg_catalog'), schema_name`, database)
}

// Objects lists tables, views, macros and sequences.
func (d *Driver) Objects(ctx context.Context, q dialect.Querier, database, schema string) ([]dialect.Object, error) {
	rows, err := q.QueryContext(ctx, `
SELECT table_name, 'table', COALESCE(comment, ''), '' FROM duckdb_tables() WHERE database_name = $1 AND schema_name = $2
UNION ALL
SELECT view_name, 'view', COALESCE(comment, ''), '' FROM duckdb_views() WHERE database_name = $1 AND schema_name = $2 AND NOT internal
UNION ALL
SELECT function_name, 'function', COALESCE(comment, ''), array_to_string(parameters, ', ') FROM duckdb_functions()
WHERE database_name = $1 AND schema_name = $2 AND NOT internal AND function_type IN ('macro', 'table_macro')
UNION ALL
SELECT sequence_name, 'sequence', COALESCE(comment, ''), '' FROM duckdb_sequences() WHERE database_name = $1 AND schema_name = $2
ORDER BY 1`, database, schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Object
	for rows.Next() {
		var o dialect.Object
		if err := rows.Scan(&o.Name, &o.Kind, &o.Comment, &o.Detail); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Columns lists the columns of a table or view.
func (d *Driver) Columns(ctx context.Context, q dialect.Querier, database, schema, table string) ([]dialect.Column, error) {
	rows, err := q.QueryContext(ctx, `
SELECT c.column_name, c.data_type, c.is_nullable, COALESCE(c.column_default, ''), COALESCE(c.comment, ''),
       COALESCE(list_position(pk.constraint_column_names, c.column_name), 0), COALESCE(t.sql, '')
FROM duckdb_columns() c
LEFT JOIN duckdb_tables() t ON t.database_name = c.database_name AND t.schema_name = c.schema_name AND t.table_name = c.table_name
LEFT JOIN duckdb_constraints() pk ON pk.database_name = c.database_name AND pk.schema_name = c.schema_name
     AND pk.table_name = c.table_name AND pk.constraint_type = 'PRIMARY KEY'
WHERE c.database_name = $1 AND c.schema_name = $2 AND c.table_name = $3
ORDER BY c.column_index`, database, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Column
	for rows.Next() {
		var c dialect.Column
		var create string
		if err := rows.Scan(&c.Name, &c.Type, &c.Nullable, &c.Default, &c.Comment, &c.PK, &create); err != nil {
			return nil, err
		}
		c.Generated = c.Default != "" && generated(create, c.Name, c.Type)
		out = append(out, c)
	}
	return out, rows.Err()
}

// generated reports whether the CREATE TABLE text declares the column as computed
// (duckdb_columns shows its expression as the default).
func generated(create, name, typ string) bool {
	for _, n := range []string{name, dialect.QuoteDouble(name)} {
		for _, before := range []string{"(", ", "} {
			if strings.Contains(create, before+n+" "+typ+" GENERATED ALWAYS") {
				return true
			}
		}
	}
	return false
}

// SchemaColumns lists the columns of every table and view of a schema.
func (d *Driver) SchemaColumns(ctx context.Context, q dialect.Querier, database, schema string) (map[string][]string, error) {
	return dialect.ColumnPairs(ctx, q, `SELECT table_name, column_name FROM duckdb_columns()
WHERE database_name = $1 AND schema_name = $2 ORDER BY table_name, column_index`, database, schema)
}

// Indexes lists the ART indexes of a table, with the indexes behind primary keys and
// unique constraints (which DuckDB keeps but does not list).
func (d *Driver) Indexes(ctx context.Context, q dialect.Querier, database, schema, table string) ([]dialect.Index, error) {
	var out []dialect.Index
	cons, err := d.Constraints(ctx, q, database, schema, table)
	if err != nil {
		return nil, err
	}
	for _, c := range cons {
		if c.Type != "PRIMARY KEY" && c.Type != "UNIQUE" {
			continue
		}
		ix := dialect.Index{Name: c.Name, Table: table, Unique: true, Primary: c.Type == "PRIMARY KEY", Method: "art", SizeBytes: -1, Constraint: c.Name, Definition: c.Definition}
		for _, col := range c.Columns {
			ix.Columns = append(ix.Columns, dialect.IndexColumn{Name: col})
		}
		out = append(out, ix)
	}
	rows, err := q.QueryContext(ctx, `SELECT index_name, is_unique, expressions, COALESCE(sql, '') FROM duckdb_indexes()
WHERE database_name = $1 AND schema_name = $2 AND table_name = $3 ORDER BY index_name`, database, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var ix dialect.Index
		var exprs string
		if err := rows.Scan(&ix.Name, &ix.Unique, &exprs, &ix.Definition); err != nil {
			return nil, err
		}
		ix.Table, ix.Method, ix.SizeBytes = table, "art", -1
		ix.Definition = strings.TrimRight(ix.Definition, ";")
		for _, e := range parseList(exprs) {
			if name, ok := quotedName(e); ok {
				ix.Columns = append(ix.Columns, dialect.IndexColumn{Name: name})
			} else {
				ix.Columns = append(ix.Columns, dialect.IndexColumn{Expression: stripParens(e)})
			}
		}
		out = append(out, ix)
	}
	return out, rows.Err()
}

// parseList reads duckdb_indexes().expressions: a list whose items are SQL text,
// quoted as strings when they need it: [id, '"name"', '(lower("name"))'].
func parseList(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	var out []string
	var b strings.Builder
	depth := 0
	inString, inIdent := false, false
	flush := func() {
		item := strings.TrimSpace(b.String())
		b.Reset()
		if len(item) >= 2 && item[0] == '\'' && item[len(item)-1] == '\'' {
			item = strings.ReplaceAll(item[1:len(item)-1], "''", "'")
		}
		if item != "" {
			out = append(out, item)
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inString:
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					b.WriteString("''")
					i++
					continue
				}
				inString = false
			}
		case inIdent:
			inIdent = c != '"'
		case c == '\'':
			inString = true
		case c == '"':
			inIdent = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			flush()
			continue
		}
		b.WriteByte(c)
	}
	flush()
	return out
}

// quotedName reads "name" or a plain identifier.
func quotedName(e string) (string, bool) {
	e = strings.TrimSpace(e)
	if len(e) >= 2 && e[0] == '"' && e[len(e)-1] == '"' && !strings.Contains(strings.ReplaceAll(e[1:len(e)-1], `""`, ""), `"`) {
		return strings.ReplaceAll(e[1:len(e)-1], `""`, `"`), true
	}
	if plain.MatchString(e) {
		return e, true
	}
	return "", false
}

func stripParens(s string) string {
	s = strings.TrimSpace(s)
	for len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		depth := 0
		inner := true
		for i, c := range s {
			if c == '(' {
				depth++
			} else if c == ')' {
				depth--
				if depth == 0 && i != len(s)-1 {
					inner = false
					break
				}
			}
		}
		if !inner {
			break
		}
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// CreateIndex returns the CREATE INDEX statement.
func (d *Driver) CreateIndex(database, schema string, ix dialect.Index, _, ifNotExists bool) (string, error) {
	if len(ix.Columns) == 0 {
		return "", errors.New("an index needs at least one column")
	}
	if ix.Name == "" {
		return "", errors.New("DuckDB indexes need a name")
	}
	if ix.Primary || ix.Constraint != "" {
		return "", errors.New("primary keys and unique constraints are part of the table definition")
	}
	parts := make([]string, 0, len(ix.Columns))
	for _, c := range ix.Columns {
		switch {
		case strings.TrimSpace(c.Expression) != "":
			parts = append(parts, "("+strings.TrimSpace(c.Expression)+")")
		case c.Name != "":
			parts = append(parts, d.Quote(c.Name))
		default:
			return "", errors.New("every index key needs a column or an expression")
		}
	}
	var b strings.Builder
	b.WriteString("CREATE ")
	if ix.Unique {
		b.WriteString("UNIQUE ")
	}
	b.WriteString("INDEX ")
	if ifNotExists {
		b.WriteString("IF NOT EXISTS ")
	}
	b.WriteString(d.Quote(ix.Name) + " ON " + d.Qualify(database, schema, ix.Table) + " (" + strings.Join(parts, ", ") + ")")
	return b.String(), nil
}

// DropIndex returns the DROP INDEX statement.
func (d *Driver) DropIndex(database, schema, _, name string, _ bool) string {
	return "DROP INDEX " + d.Qualify(database, schema, name)
}

// Constraints lists primary key, unique, foreign key and check constraints.
func (d *Driver) Constraints(ctx context.Context, q dialect.Querier, database, schema, table string) ([]dialect.Constraint, error) {
	rows, err := q.QueryContext(ctx, `
SELECT COALESCE(constraint_name, ''), constraint_type, constraint_text, to_json(constraint_column_names)::VARCHAR,
       COALESCE(referenced_table, ''), COALESCE(to_json(referenced_column_names)::VARCHAR, '[]')
FROM duckdb_constraints()
WHERE database_name = $1 AND schema_name = $2 AND table_name = $3 AND constraint_type IN ('PRIMARY KEY', 'UNIQUE', 'FOREIGN KEY', 'CHECK')
ORDER BY position(left(constraint_type, 1) IN 'PUFC'), constraint_index`, database, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Constraint
	for rows.Next() {
		var c dialect.Constraint
		var cols, refCols string
		if err := rows.Scan(&c.Name, &c.Type, &c.Definition, &cols, &c.RefTable, &refCols); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(cols), &c.Columns)
		_ = json.Unmarshal([]byte(refCols), &c.RefColumns)
		if c.Name == "" {
			c.Name = table + "_" + strings.ToLower(strings.ReplaceAll(c.Type, " ", "_"))
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ForeignKeys lists the foreign keys of a schema.
func (d *Driver) ForeignKeys(ctx context.Context, q dialect.Querier, database, schema string) ([]dialect.ForeignKey, error) {
	rows, err := q.QueryContext(ctx, `
SELECT table_name, to_json(constraint_column_names)::VARCHAR, referenced_table, to_json(referenced_column_names)::VARCHAR
FROM duckdb_constraints()
WHERE database_name = $1 AND schema_name = $2 AND constraint_type = 'FOREIGN KEY'
ORDER BY table_name, constraint_index`, database, schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.ForeignKey
	for rows.Next() {
		var fk dialect.ForeignKey
		var cols, refCols string
		if err := rows.Scan(&fk.Table, &cols, &fk.RefTable, &refCols); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(cols), &fk.Columns)
		_ = json.Unmarshal([]byte(refCols), &fk.RefColumns)
		fk.RefSchema = schema
		out = append(out, fk)
	}
	return out, rows.Err()
}

// DDL returns the statement DuckDB keeps for the object (with the table's indexes).
func (d *Driver) DDL(ctx context.Context, q dialect.Querier, database, schema string, obj dialect.Object) (string, error) {
	var query string
	switch obj.Kind {
	case dialect.KindView:
		query = `SELECT sql FROM duckdb_views() WHERE database_name = $1 AND schema_name = $2 AND view_name = $3`
	case dialect.KindSequence:
		query = `SELECT sql FROM duckdb_sequences() WHERE database_name = $1 AND schema_name = $2 AND sequence_name = $3`
	case dialect.KindFunction:
		query = `SELECT 'CREATE OR REPLACE MACRO ' || $3 || '(' || array_to_string(parameters, ', ') || ') AS ' ||
       CASE function_type WHEN 'table_macro' THEN 'TABLE ' ELSE '' END || macro_definition || ';'
FROM duckdb_functions() WHERE database_name = $1 AND schema_name = $2 AND function_name = $3 LIMIT 1`
	default:
		query = `SELECT sql FROM duckdb_tables() WHERE database_name = $1 AND schema_name = $2 AND table_name = $3`
	}
	var s sql.NullString
	if err := q.QueryRowContext(ctx, query, database, schema, obj.Name).Scan(&s); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("%s %s not found", obj.Kind, obj.Name)
		}
		return "", err
	}
	var out strings.Builder
	out.WriteString(strings.TrimRight(s.String, "\n ;") + ";\n")
	if obj.Kind == dialect.KindTable {
		idx, err := dialect.Strings(ctx, q, `SELECT sql FROM duckdb_indexes()
WHERE database_name = $1 AND schema_name = $2 AND table_name = $3 AND sql IS NOT NULL ORDER BY index_name`, database, schema, obj.Name)
		if err != nil {
			return "", err
		}
		for _, x := range idx {
			out.WriteString(strings.TrimRight(x, "\n ;") + ";\n")
		}
	}
	return out.String(), nil
}

// TableStats reads the estimated rows of tables (views have none).
func (d *Driver) TableStats(ctx context.Context, q dialect.Querier, database, schema string) ([]dialect.TableStat, error) {
	rows, err := q.QueryContext(ctx, `
SELECT table_name, 'table', estimated_size FROM duckdb_tables() WHERE database_name = $1 AND schema_name = $2
UNION ALL
SELECT view_name, 'view', -1 FROM duckdb_views() WHERE database_name = $1 AND schema_name = $2 AND NOT internal
ORDER BY 1`, database, schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.TableStat
	for rows.Next() {
		t := dialect.TableStat{Bytes: -1}
		if err := rows.Scan(&t.Name, &t.Kind, &t.Rows); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

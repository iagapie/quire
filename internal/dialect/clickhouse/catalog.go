package clickhouse

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/iagapie/quire/internal/dialect"
)

// Databases lists the databases, system ones last.
func (d *Driver) Databases(ctx context.Context, q dialect.Querier) ([]string, error) {
	return dialect.Strings(ctx, q, `SELECT name FROM system.databases
ORDER BY name IN ('system', 'information_schema', 'INFORMATION_SCHEMA'), name`)
}

// Schemas returns nothing: a ClickHouse database holds the tables.
func (d *Driver) Schemas(context.Context, dialect.Querier, string) ([]string, error) { return nil, nil }

// Objects lists tables, views and materialized views; the detail is the engine.
func (d *Driver) Objects(ctx context.Context, q dialect.Querier, database, _ string) ([]dialect.Object, error) {
	rows, err := q.QueryContext(ctx, `SELECT name,
       multiIf(engine = 'View', 'view', engine = 'MaterializedView', 'matview', 'table'), comment, engine
FROM system.tables WHERE database = ? AND NOT is_temporary AND NOT startsWith(name, '.inner')
ORDER BY name`, database)
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
		if o.Kind != dialect.KindTable {
			o.Detail = ""
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Columns lists the columns of a table. Sorting keys are not unique, so no column is
// a primary key for editing; MATERIALIZED and ALIAS columns cannot be written.
func (d *Driver) Columns(ctx context.Context, q dialect.Querier, database, _, table string) ([]dialect.Column, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, type, default_kind, default_expression, comment
FROM system.columns WHERE database = ? AND table = ? ORDER BY position`, database, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Column
	for rows.Next() {
		var c dialect.Column
		var kind string
		if err := rows.Scan(&c.Name, &c.Type, &kind, &c.Default, &c.Comment); err != nil {
			return nil, err
		}
		c.Nullable = strings.HasPrefix(c.Type, "Nullable(") || strings.HasPrefix(c.Type, "LowCardinality(Nullable(")
		c.Generated = kind == "MATERIALIZED" || kind == "ALIAS" || kind == "EPHEMERAL"
		out = append(out, c)
	}
	return out, rows.Err()
}

// SchemaColumns lists the columns of every table of a database.
func (d *Driver) SchemaColumns(ctx context.Context, q dialect.Querier, database, _ string) (map[string][]string, error) {
	return dialect.ColumnPairs(ctx, q, `SELECT table, name FROM system.columns WHERE database = ? ORDER BY table, position`, database)
}

// Indexes lists the primary key (the sparse index of MergeTree tables) and the data
// skipping indexes.
func (d *Driver) Indexes(ctx context.Context, q dialect.Querier, database, _, table string) ([]dialect.Index, error) {
	var out []dialect.Index
	var key string
	err := q.QueryRowContext(ctx, `SELECT primary_key FROM system.tables WHERE database = ? AND name = ?`, database, table).Scan(&key)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if key != "" {
		ix := dialect.Index{Name: "PRIMARY KEY", Table: table, Primary: true, Method: "sparse", SizeBytes: -1, Definition: "PRIMARY KEY (" + key + ")"}
		for _, part := range splitTop(key) {
			if plain.MatchString(part) || strings.HasPrefix(part, "`") {
				ix.Columns = append(ix.Columns, dialect.IndexColumn{Name: strings.Trim(part, "`")})
			} else {
				ix.Columns = append(ix.Columns, dialect.IndexColumn{Expression: part})
			}
		}
		out = append(out, ix)
	}
	rows, err := q.QueryContext(ctx, `SELECT name, type_full, expr, granularity, data_compressed_bytes
FROM system.data_skipping_indices WHERE database = ? AND table = ? ORDER BY name`, database, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name, typ, expr string
		var gran, size uint64
		if err := rows.Scan(&name, &typ, &expr, &gran, &size); err != nil {
			return nil, err
		}
		ix := dialect.Index{Name: name, Table: table, Method: typ, SizeBytes: int64(size),
			Columns: []dialect.IndexColumn{{Expression: expr}}}
		ix.Definition = "ALTER TABLE " + d.Qualify(database, "", table) + " ADD INDEX " + d.Quote(name) + " " + expr + " TYPE " + typ + " GRANULARITY " + strconv.FormatUint(gran, 10)
		out = append(out, ix)
	}
	return out, rows.Err()
}

// splitTop splits "a, toDate(b, 'x'), c" at the top-level commas.
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			switch c {
			case '\\':
				i++
			case quote:
				quote = 0
			}
		case c == '\'' || c == '`' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	if rest := strings.TrimSpace(s[start:]); rest != "" {
		out = append(out, rest)
	}
	return out
}

// checks finds the CHECK constraints in the one-line create_table_query: each runs
// until the next constraint, index or projection, or the end of the column list.
func checks(create string) []dialect.Constraint {
	var out []dialect.Constraint
	rest := create
	for {
		i := strings.Index(rest, "CONSTRAINT ")
		if i < 0 {
			return out
		}
		rest = rest[i+len("CONSTRAINT "):]
		name, body, ok := strings.Cut(rest, " CHECK ")
		if !ok || strings.ContainsAny(strings.Trim(name, "`"), " ") && !strings.HasPrefix(name, "`") {
			continue
		}
		end := len(body)
		for _, stop := range []string{", CONSTRAINT ", ", INDEX ", ", PROJECTION ", ") ENGINE"} {
			if k := strings.Index(body, stop); k >= 0 && k < end {
				end = k
			}
		}
		out = append(out, dialect.Constraint{Name: strings.Trim(name, "`"), Type: "CHECK", Definition: "CHECK " + strings.TrimSpace(body[:end])})
		rest = body[end:]
	}
}

// Constraints lists the CHECK constraints (ClickHouse has no keys to reference).
func (d *Driver) Constraints(ctx context.Context, q dialect.Querier, database, _, table string) ([]dialect.Constraint, error) {
	var create string
	err := q.QueryRowContext(ctx, `SELECT create_table_query FROM system.tables WHERE database = ? AND name = ?`, database, table).Scan(&create)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return checks(create), nil
}

// DDL returns SHOW CREATE.
func (d *Driver) DDL(ctx context.Context, q dialect.Querier, database, _ string, obj dialect.Object) (string, error) {
	var s string
	if err := q.QueryRowContext(ctx, "SHOW CREATE TABLE "+d.Qualify(database, "", obj.Name)).Scan(&s); err != nil {
		return "", err
	}
	return strings.TrimRight(s, ";\n ") + ";\n", nil
}

// TableStats reads rows and sizes from system.tables.
func (d *Driver) TableStats(ctx context.Context, q dialect.Querier, database, _ string) ([]dialect.TableStat, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, multiIf(engine = 'View', 'view', engine = 'MaterializedView', 'matview', 'table'),
       ifNull(toInt64(total_rows), -1), ifNull(toInt64(total_bytes), -1)
FROM system.tables WHERE database = ? AND NOT is_temporary AND NOT startsWith(name, '.inner') ORDER BY name`, database)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.TableStat
	for rows.Next() {
		var t dialect.TableStat
		if err := rows.Scan(&t.Name, &t.Kind, &t.Rows, &t.Bytes); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

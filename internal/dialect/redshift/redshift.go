// Package redshift is the Amazon Redshift driver: the PostgreSQL driver (pgx v5) with a
// catalog written for Redshift's PostgreSQL 8.0 system tables. Redshift has no
// indexes, no COPY from the client and no read-only sessions.
package redshift

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/dialect/postgres"
)

func init() {
	dialect.Register(newDriver())
}

func newDriver() *Driver {
	return &Driver{postgres.New(info(), postgres.NoCopy(), postgres.ClientReadOnly())}
}

// Driver is the Redshift driver.
type Driver struct {
	*postgres.Driver
}

func info() dialect.Info {
	i := postgres.Info()
	i.ID, i.Label = "redshift", "Amazon Redshift"
	i.Description = "Provisioned clusters and Redshift Serverless, with a database user"
	i.DefaultPort, i.DefaultUser, i.DefaultDatabase = 5439, "awsuser", "dev"
	i.Folders = []string{dialect.KindTable, dialect.KindView, dialect.KindFunction, dialect.KindProcedure}
	i.Index = dialect.IndexFeatures{}
	i.Upsert = false
	i.TLS = []string{"disable", "prefer", "require", "verify-ca", "verify-full"}
	i.Note = "Redshift has no read-only sessions: a read-only connection is enforced by Quire, which runs only queries. Imports use INSERT statements; load large files with COPY from Amazon S3."
	i.Order = 13
	return i
}

var versionRe = regexp.MustCompile(`Redshift \S+`)

// Version returns the Redshift release.
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

// Databases lists the databases of the cluster.
func (d *Driver) Databases(ctx context.Context, q dialect.Querier) ([]string, error) {
	return dialect.Strings(ctx, q, `SELECT datname FROM pg_database
WHERE datname NOT IN ('template0', 'template1', 'padb_harvest') ORDER BY datname`)
}

// Schemas lists the schemas, system schemas last.
func (d *Driver) Schemas(ctx context.Context, q dialect.Querier, _ string) ([]string, error) {
	return dialect.Strings(ctx, q, `SELECT nspname FROM pg_namespace
WHERE nspname !~ '^pg_(temp_|toast)'
ORDER BY nspname IN ('pg_catalog', 'information_schema', 'pg_internal', 'pg_automv', 'catalog_history'), nspname`)
}

// Objects lists tables, views, functions and stored procedures. Procedures return
// nothing (prorettype is void) in Redshift's pg_proc.
func (d *Driver) Objects(ctx context.Context, q dialect.Querier, _, schema string) ([]dialect.Object, error) {
	rows, err := q.QueryContext(ctx, `
SELECT c.relname, CASE c.relkind WHEN 'v' THEN 'view' ELSE 'table' END, COALESCE(ds.description, ''), '', ''
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_description ds ON ds.objoid = c.oid AND ds.objsubid = 0
WHERE n.nspname = $1 AND c.relkind IN ('r', 'v')
UNION ALL
SELECT p.proname, CASE WHEN p.prorettype = 'pg_catalog.void'::regtype THEN 'procedure' ELSE 'function' END,
       '', oidvectortypes(p.proargtypes), p.oid::text
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1
ORDER BY 1, 5`, schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Object
	for rows.Next() {
		var o dialect.Object
		if err := rows.Scan(&o.Name, &o.Kind, &o.Comment, &o.Detail, &o.ID); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// relation finds a table or view of a schema.
func relation(ctx context.Context, q dialect.Querier, schema, table string) (int64, error) {
	var oid int64
	err := q.QueryRowContext(ctx, `SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, schema, table).Scan(&oid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("table %s.%s not found", schema, table)
	}
	return oid, err
}

// attributes maps the column numbers of a relation to their names.
func attributes(ctx context.Context, q dialect.Querier, oid int64) (map[int]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT attnum, attname FROM pg_attribute WHERE attrelid = $1 AND attnum > 0 AND NOT attisdropped`, oid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int]string{}
	for rows.Next() {
		var n int
		var name string
		if err := rows.Scan(&n, &name); err != nil {
			return nil, err
		}
		out[n] = name
	}
	return out, rows.Err()
}

// numbers parses an array_to_string of column numbers ("2,1").
func numbers(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func names(nums []int, attrs map[int]string) []string {
	out := make([]string, 0, len(nums))
	for _, n := range nums {
		out = append(out, attrs[n])
	}
	return out
}

// Columns lists the columns of a table or view. Identity columns have an "identity"
// default and cannot be written.
func (d *Driver) Columns(ctx context.Context, q dialect.Querier, _, schema, table string) ([]dialect.Column, error) {
	oid, err := relation(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	var pk sql.NullString
	err = q.QueryRowContext(ctx, `SELECT array_to_string(conkey, ',') FROM pg_constraint WHERE conrelid = $1 AND contype = 'p'`, oid).Scan(&pk)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	keys := numbers(pk.String)
	rows, err := q.QueryContext(ctx, `
SELECT a.attnum, a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
       COALESCE(pg_get_expr(ad.adbin, ad.adrelid), ''), COALESCE(ds.description, '')
FROM pg_attribute a
LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
LEFT JOIN pg_description ds ON ds.objoid = a.attrelid AND ds.objsubid = a.attnum
WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`, oid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Column
	for rows.Next() {
		var c dialect.Column
		var n int
		if err := rows.Scan(&n, &c.Name, &c.Type, &c.Nullable, &c.Default, &c.Comment); err != nil {
			return nil, err
		}
		c.PK = slices.Index(keys, n) + 1
		c.Generated = strings.HasPrefix(c.Default, `"identity"(`)
		out = append(out, c)
	}
	return out, rows.Err()
}

// SchemaColumns lists the columns of every table and view of a schema.
func (d *Driver) SchemaColumns(ctx context.Context, q dialect.Querier, _, schema string) (map[string][]string, error) {
	return dialect.ColumnPairs(ctx, q, `
SELECT c.relname, a.attname FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r', 'v') AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY c.relname, a.attnum`, schema)
}

// Indexes returns nothing: Redshift has sort and distribution keys, not indexes.
func (d *Driver) Indexes(context.Context, dialect.Querier, string, string, string) ([]dialect.Index, error) {
	return nil, nil
}

// CreateIndex is not supported.
func (d *Driver) CreateIndex(string, string, dialect.Index, bool, bool) (string, error) {
	return "", dialect.ErrUnsupported
}

var fkActions = map[string]string{"a": "NO ACTION", "r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}

type constraintRow struct {
	name, typ, keys, refSchema, refTable, refKeys, upd, del, def string
	refOID                                                       int64
}

func constraintRows(ctx context.Context, q dialect.Querier, where string, args ...any) ([]constraintRow, error) {
	rows, err := q.QueryContext(ctx, `
SELECT con.conname, con.contype, COALESCE(array_to_string(con.conkey, ','), ''),
       COALESCE(fn.nspname, ''), COALESCE(ft.relname, ''), COALESCE(array_to_string(con.confkey, ','), ''),
       COALESCE(con.confupdtype, ''), COALESCE(con.confdeltype, ''), pg_get_constraintdef(con.oid), COALESCE(ft.oid, 0)
FROM pg_constraint con
JOIN pg_class t ON t.oid = con.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
LEFT JOIN pg_class ft ON ft.oid = con.confrelid
LEFT JOIN pg_namespace fn ON fn.oid = ft.relnamespace
WHERE `+where+`
ORDER BY position(con.contype IN 'pufc'), con.conname`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []constraintRow
	for rows.Next() {
		var r constraintRow
		if err := rows.Scan(&r.name, &r.typ, &r.keys, &r.refSchema, &r.refTable, &r.refKeys, &r.upd, &r.del, &r.def, &r.refOID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Constraints lists primary key, unique, foreign key and check constraints (Redshift
// keeps the first three as information for the planner, without enforcing them).
func (d *Driver) Constraints(ctx context.Context, q dialect.Querier, _, schema, table string) ([]dialect.Constraint, error) {
	oid, err := relation(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	attrs, err := attributes(ctx, q, oid)
	if err != nil {
		return nil, err
	}
	recs, err := constraintRows(ctx, q, "con.conrelid = $1", oid)
	if err != nil {
		return nil, err
	}
	var out []dialect.Constraint
	for _, r := range recs {
		c := dialect.Constraint{Name: r.name, Columns: names(numbers(r.keys), attrs), Definition: r.def}
		switch r.typ {
		case "p":
			c.Type = "PRIMARY KEY"
		case "u":
			c.Type = "UNIQUE"
		case "f":
			c.Type = "FOREIGN KEY"
			c.RefSchema, c.RefTable = r.refSchema, r.refTable
			c.OnUpdate, c.OnDelete = fkActions[r.upd], fkActions[r.del]
			ref, err := attributes(ctx, q, r.refOID)
			if err != nil {
				return nil, err
			}
			c.RefColumns = names(numbers(r.refKeys), ref)
		case "c":
			c.Type = "CHECK"
		default:
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

type fkRow struct {
	table, keys, refSchema, refTable, refKeys string
	oid, refOID                               int64
}

func fkRows(ctx context.Context, q dialect.Querier, schema string) ([]fkRow, error) {
	rows, err := q.QueryContext(ctx, `
SELECT t.relname, t.oid, COALESCE(array_to_string(con.conkey, ','), ''), fn.nspname, ft.relname, ft.oid,
       COALESCE(array_to_string(con.confkey, ','), '')
FROM pg_constraint con
JOIN pg_class t ON t.oid = con.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace
JOIN pg_class ft ON ft.oid = con.confrelid JOIN pg_namespace fn ON fn.oid = ft.relnamespace
WHERE n.nspname = $1 AND con.contype = 'f'
ORDER BY t.relname, con.conname`, schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []fkRow
	for rows.Next() {
		var r fkRow
		if err := rows.Scan(&r.table, &r.oid, &r.keys, &r.refSchema, &r.refTable, &r.refOID, &r.refKeys); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ForeignKeys lists the foreign keys of a schema.
func (d *Driver) ForeignKeys(ctx context.Context, q dialect.Querier, _, schema string) ([]dialect.ForeignKey, error) {
	recs, err := fkRows(ctx, q, schema)
	if err != nil {
		return nil, err
	}
	cache := map[int64]map[int]string{}
	attrsOf := func(oid int64) (map[int]string, error) {
		if a, ok := cache[oid]; ok {
			return a, nil
		}
		a, err := attributes(ctx, q, oid)
		cache[oid] = a
		return a, err
	}
	out := make([]dialect.ForeignKey, 0, len(recs))
	for _, r := range recs {
		a, err := attrsOf(r.oid)
		if err != nil {
			return nil, err
		}
		ra, err := attrsOf(r.refOID)
		if err != nil {
			return nil, err
		}
		out = append(out, dialect.ForeignKey{Table: r.table, Columns: names(numbers(r.keys), a), RefSchema: r.refSchema, RefTable: r.refTable, RefColumns: names(numbers(r.refKeys), ra)})
	}
	return out, nil
}

// TableStats reads rows and sizes from SVV_TABLE_INFO (which lists only tables with
// data); elsewhere the planner estimate is used.
func (d *Driver) TableStats(ctx context.Context, q dialect.Querier, database, schema string) ([]dialect.TableStat, error) {
	objs, err := d.Objects(ctx, q, database, schema)
	if err != nil {
		return nil, err
	}
	type size struct{ rows, bytes int64 }
	sizes := map[string]size{}
	rows, err := q.QueryContext(ctx, `SELECT "table", COALESCE(tbl_rows, 0)::bigint, COALESCE(size, 0)::bigint * 1048576
FROM svv_table_info WHERE "schema" = $1`, schema)
	if err != nil {
		rows, err = q.QueryContext(ctx, `SELECT c.relname, c.reltuples::bigint, -1::bigint
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relkind = 'r'`, schema)
		if err != nil {
			return nil, err
		}
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		var s size
		if err := rows.Scan(&name, &s.rows, &s.bytes); err != nil {
			return nil, err
		}
		sizes[name] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []dialect.TableStat
	for _, o := range objs {
		switch o.Kind {
		case dialect.KindTable:
			s, ok := sizes[o.Name]
			if !ok {
				s = size{0, 0} // empty tables are missing from SVV_TABLE_INFO
			}
			out = append(out, dialect.TableStat{Name: o.Name, Kind: o.Kind, Rows: s.rows, Bytes: s.bytes})
		case dialect.KindView:
			out = append(out, dialect.TableStat{Name: o.Name, Kind: o.Kind, Rows: -1, Bytes: -1})
		}
	}
	return out, nil
}

// DDL returns Redshift's SHOW TABLE / SHOW VIEW / SHOW PROCEDURE text, or a definition
// rebuilt from the catalog when the server has no SHOW (older clusters).
func (d *Driver) DDL(ctx context.Context, q dialect.Querier, _, schema string, obj dialect.Object) (string, error) {
	name := d.Qualify("", schema, obj.Name)
	show := func(stmt string) (string, bool) {
		var s sql.NullString
		if err := q.QueryRowContext(ctx, stmt).Scan(&s); err != nil || s.String == "" {
			return "", false
		}
		return strings.TrimRight(s.String, "\n ;") + ";\n", true
	}
	switch obj.Kind {
	case dialect.KindView:
		if s, ok := show("SHOW VIEW " + name); ok {
			return s, nil
		}
		var def sql.NullString
		err := q.QueryRowContext(ctx, `SELECT pg_get_viewdef(c.oid) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, schema, obj.Name).Scan(&def)
		return "CREATE OR REPLACE VIEW " + name + " AS\n" + strings.TrimRight(def.String, ";\n ") + ";\n", err
	case dialect.KindFunction, dialect.KindProcedure:
		if obj.Kind == dialect.KindProcedure {
			if s, ok := show("SHOW PROCEDURE " + name + "(" + obj.Detail + ")"); ok {
				return s, nil
			}
		}
		return d.routineDDL(ctx, q, schema, obj)
	}
	if s, ok := show("SHOW TABLE " + name); ok {
		return s, nil
	}
	return d.tableDDL(ctx, q, schema, obj.Name)
}

func (d *Driver) routineDDL(ctx context.Context, q dialect.Querier, schema string, obj dialect.Object) (string, error) {
	var ret, lang, src string
	var volatile string
	err := q.QueryRowContext(ctx, `SELECT format_type(p.prorettype, NULL), l.lanname, p.prosrc, p.provolatile
FROM pg_proc p JOIN pg_language l ON l.oid = p.prolang WHERE p.oid = $1`, obj.ID).Scan(&ret, &lang, &src, &volatile)
	if err != nil {
		return "", err
	}
	name := d.Qualify("", schema, obj.Name) + "(" + obj.Detail + ")"
	if obj.Kind == dialect.KindProcedure {
		return "CREATE OR REPLACE PROCEDURE " + name + "\nAS $$" + src + "$$ LANGUAGE " + lang + ";\n", nil
	}
	vol := map[string]string{"i": "IMMUTABLE", "s": "STABLE", "v": "VOLATILE"}[volatile]
	return "CREATE OR REPLACE FUNCTION " + name + "\nRETURNS " + ret + "\n" + vol + "\nAS $$" + src + "$$ LANGUAGE " + lang + ";\n", nil
}

func (d *Driver) tableDDL(ctx context.Context, q dialect.Querier, schema, table string) (string, error) {
	cols, err := d.Columns(ctx, q, "", schema, table)
	if err != nil {
		return "", err
	}
	cons, err := d.Constraints(ctx, q, "", schema, table)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, c := range cols {
		l := "    " + d.Quote(c.Name) + " " + c.Type
		if c.Default != "" && !c.Generated {
			l += " DEFAULT " + c.Default
		}
		if !c.Nullable {
			l += " NOT NULL"
		}
		lines = append(lines, l)
	}
	for _, c := range cons {
		lines = append(lines, "    CONSTRAINT "+d.Quote(c.Name)+" "+c.Definition)
	}
	return "CREATE TABLE " + d.Qualify("", schema, table) + " (\n" + strings.Join(lines, ",\n") + "\n);\n", nil
}

// Upsert is not available: Redshift has MERGE but no INSERT … ON CONFLICT.
func (d *Driver) Upsert(string, []string, []string) string { return "" }

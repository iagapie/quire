package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/iagapie/quire/internal/dialect"
)

// Databases lists the databases that accept connections.
func (d *Driver) Databases(ctx context.Context, q dialect.Querier) ([]string, error) {
	return strings1(ctx, q, `SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname`)
}

// Schemas lists the schemas of the current database, user schemas first.
func (d *Driver) Schemas(ctx context.Context, q dialect.Querier, _ string) ([]string, error) {
	return strings1(ctx, q, `SELECT nspname FROM pg_namespace
WHERE nspname NOT LIKE 'pg\_toast%' AND nspname NOT LIKE 'pg\_temp\_%'
ORDER BY nspname IN ('pg_catalog', 'information_schema'), nspname`)
}

// Objects lists tables, views, routines, sequences and triggers of a schema.
func (d *Driver) Objects(ctx context.Context, q dialect.Querier, _, schema string) ([]dialect.Object, error) {
	rows, err := q.QueryContext(ctx, `
SELECT c.relname,
       CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'matview' WHEN 'S' THEN 'sequence' ELSE 'table' END,
       COALESCE(obj_description(c.oid, 'pg_class'), ''), '', '', c.oid::text
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S') AND c.relispartition IS NOT TRUE
UNION ALL
SELECT p.proname, CASE p.prokind WHEN 'p' THEN 'procedure' ELSE 'function' END,
       COALESCE(obj_description(p.oid, 'pg_proc'), ''), pg_get_function_identity_arguments(p.oid), '', p.oid::text
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.prokind IN ('f', 'p', 'w')
UNION ALL
SELECT t.tgname, 'trigger', COALESCE(obj_description(t.oid, 'pg_trigger'), ''), c.relname, c.relname, t.oid::text
FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND NOT t.tgisinternal
ORDER BY 1, 6`, schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Object
	for rows.Next() {
		var o dialect.Object
		if err := rows.Scan(&o.Name, &o.Kind, &o.Comment, &o.Detail, &o.Table, &o.ID); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Columns lists the columns of a table or view.
func (d *Driver) Columns(ctx context.Context, q dialect.Querier, _, schema, table string) ([]dialect.Column, error) {
	rows, err := q.QueryContext(ctx, `
SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
       COALESCE(pg_get_expr(d.adbin, d.adrelid), ''),
       COALESCE(array_position(pk.conkey, a.attnum), 0),
       COALESCE(col_description(a.attrelid, a.attnum), ''),
       (a.attgenerated <> '' OR a.attidentity = 'a')
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
LEFT JOIN pg_constraint pk ON pk.conrelid = c.oid AND pk.contype = 'p'
WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Column
	for rows.Next() {
		var c dialect.Column
		if err := rows.Scan(&c.Name, &c.Type, &c.Nullable, &c.Default, &c.PK, &c.Comment, &c.Generated); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

var fillFactorRe = regexp.MustCompile(`fillfactor=(\d+)`)

// Indexes lists the indexes of a table with their key parts.
func (d *Driver) Indexes(ctx context.Context, q dialect.Querier, _, schema, table string) ([]dialect.Index, error) {
	rows, err := q.QueryContext(ctx, `
SELECT i.relname, ix.indisunique, ix.indisprimary, am.amname, pg_get_indexdef(ix.indexrelid),
       COALESCE(pg_get_expr(ix.indpred, ix.indrelid), ''), COALESCE(ts.spcname, ''),
       pg_relation_size(i.oid), COALESCE(con.conname, ''), COALESCE(array_to_string(i.reloptions, ','), ''),
       k.n, pg_get_indexdef(ix.indexrelid, k.n, true), ix.indkey[k.n - 1] = 0,
       COALESCE(ix.indoption[k.n - 1], 0), k.n > ix.indnkeyatts,
       COALESCE((SELECT collname FROM pg_collation co WHERE co.oid = ix.indcollation[k.n - 1] AND co.collname <> 'default'), '')
FROM pg_index ix
JOIN pg_class i ON i.oid = ix.indexrelid
JOIN pg_class t ON t.oid = ix.indrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
JOIN pg_am am ON am.oid = i.relam
LEFT JOIN pg_tablespace ts ON ts.oid = i.reltablespace
LEFT JOIN pg_constraint con ON con.conindid = ix.indexrelid AND con.conrelid = ix.indrelid AND con.contype IN ('p', 'u', 'x')
CROSS JOIN LATERAL generate_series(1, ix.indnatts) AS k(n)
WHERE n.nspname = $1 AND t.relname = $2
ORDER BY ix.indisprimary DESC, i.relname, k.n`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Index
	for rows.Next() {
		var (
			ix              dialect.Index
			opts, part      string
			n, option       int
			isExpr, include bool
			collation       string
		)
		if err := rows.Scan(&ix.Name, &ix.Unique, &ix.Primary, &ix.Method, &ix.Definition, &ix.Where, &ix.Tablespace,
			&ix.SizeBytes, &ix.Constraint, &opts, &n, &part, &isExpr, &option, &include, &collation); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Name != ix.Name {
			ix.Table = table
			ix.Where = stripParens(ix.Where)
			if m := fillFactorRe.FindStringSubmatch(opts); m != nil {
				ix.FillFactor, _ = strconv.Atoi(m[1])
			}
			out = append(out, ix)
		}
		cur := &out[len(out)-1]
		if include {
			cur.Include = append(cur.Include, unquoteIdent(part))
			continue
		}
		col := dialect.IndexColumn{Desc: option&1 == 1, Collation: collation}
		nullsFirst := option&2 == 2
		switch {
		case col.Desc && !nullsFirst:
			col.Nulls = "last"
		case !col.Desc && nullsFirst:
			col.Nulls = "first"
		}
		if isExpr {
			col.Expression = part
		} else {
			col.Name, col.OpClass = splitOpClass(part)
		}
		cur.Columns = append(cur.Columns, col)
	}
	return out, rows.Err()
}

// splitOpClass separates a column from a non-default operator class
// ("name text_pattern_ops").
func splitOpClass(s string) (string, string) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) {
		end := 1
		for end < len(s) {
			if s[end] == '"' {
				if end+1 < len(s) && s[end+1] == '"' {
					end += 2
					continue
				}
				break
			}
			end++
		}
		return unquoteIdent(s[:min(end+1, len(s))]), strings.TrimSpace(s[min(end+1, len(s)):])
	}
	name, rest, _ := strings.Cut(s, " ")
	return name, strings.TrimSpace(rest)
}

func unquoteIdent(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}

var fkActions = map[string]string{"a": "NO ACTION", "r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}

// Constraints lists primary keys, unique, foreign key, check and exclusion constraints.
func (d *Driver) Constraints(ctx context.Context, q dialect.Querier, _, schema, table string) ([]dialect.Constraint, error) {
	rows, err := q.QueryContext(ctx, `
SELECT con.conname, con.contype,
       COALESCE((SELECT json_agg(a.attname ORDER BY k.o) FROM unnest(con.conkey) WITH ORDINALITY k(n, o)
                 JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.n), '[]')::text,
       COALESCE(fn.nspname, ''), COALESCE(ft.relname, ''),
       COALESCE((SELECT json_agg(a.attname ORDER BY k.o) FROM unnest(con.confkey) WITH ORDINALITY k(n, o)
                 JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.n), '[]')::text,
       COALESCE(con.confupdtype::text, ''), COALESCE(con.confdeltype::text, ''), pg_get_constraintdef(con.oid, true)
FROM pg_constraint con
JOIN pg_class t ON t.oid = con.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
LEFT JOIN pg_class ft ON ft.oid = con.confrelid
LEFT JOIN pg_namespace fn ON fn.oid = ft.relnamespace
WHERE n.nspname = $1 AND t.relname = $2 AND con.contype <> 'n'
ORDER BY position(con.contype IN 'pufcx'), con.conname`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Constraint
	for rows.Next() {
		var (
			c                            dialect.Constraint
			typ, cols, refCols, upd, del string
		)
		if err := rows.Scan(&c.Name, &typ, &cols, &c.RefSchema, &c.RefTable, &refCols, &upd, &del, &c.Definition); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(cols), &c.Columns)
		_ = json.Unmarshal([]byte(refCols), &c.RefColumns)
		switch typ {
		case "p":
			c.Type = "PRIMARY KEY"
		case "u":
			c.Type = "UNIQUE"
		case "f":
			c.Type = "FOREIGN KEY"
			c.OnUpdate, c.OnDelete = fkActions[upd], fkActions[del]
		case "c":
			c.Type = "CHECK"
		case "x":
			c.Type = "EXCLUDE"
		default:
			c.Type = typ
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// TableStats estimates rows and sizes from the planner statistics.
func (d *Driver) TableStats(ctx context.Context, q dialect.Querier, _, schema string) ([]dialect.TableStat, error) {
	rows, err := q.QueryContext(ctx, `
SELECT c.relname, CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'matview' ELSE 'table' END,
       CASE WHEN c.relkind IN ('r', 'p', 'm') THEN COALESCE(c.reltuples::bigint, -1) ELSE -1 END,
       CASE WHEN c.relkind IN ('r', 'p', 'm') THEN COALESCE(pg_total_relation_size(c.oid), -1) ELSE -1 END
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND c.relispartition IS NOT TRUE
ORDER BY c.relname`, schema)
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
		if t.Rows < 0 {
			t.Rows = -1
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func strings1(ctx context.Context, q dialect.Querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DDL returns the definition of an object.
func (d *Driver) DDL(ctx context.Context, q dialect.Querier, _, schema string, obj dialect.Object) (string, error) {
	name := d.Qualify("", schema, obj.Name)
	one := func(query string, args ...any) (string, error) {
		var s sql.NullString
		err := q.QueryRowContext(ctx, query, args...).Scan(&s)
		return s.String, err
	}
	switch obj.Kind {
	case dialect.KindView:
		def, err := one(`SELECT pg_get_viewdef(to_regclass($1), true)`, name)
		return "CREATE OR REPLACE VIEW " + name + " AS\n" + strings.TrimRight(def, ";\n ") + ";\n", err
	case dialect.KindMatView:
		def, err := one(`SELECT pg_get_viewdef(to_regclass($1), true)`, name)
		if err != nil {
			return "", err
		}
		idx, err := d.indexDDL(ctx, q, schema, obj.Name)
		return "CREATE MATERIALIZED VIEW " + name + " AS\n" + strings.TrimRight(def, ";\n ") + ";\n" + idx, err
	case dialect.KindFunction, dialect.KindProcedure:
		def, err := one(`SELECT pg_get_functiondef($1::oid)`, obj.ID)
		return strings.TrimRight(def, "\n ") + ";\n", err
	case dialect.KindTrigger:
		def, err := one(`SELECT pg_get_triggerdef($1::oid, true)`, obj.ID)
		return def + ";\n", err
	case dialect.KindSequence:
		return d.sequenceDDL(ctx, q, schema, obj.Name)
	}
	return d.tableDDL(ctx, q, schema, obj.Name)
}

func (d *Driver) tableDDL(ctx context.Context, q dialect.Querier, schema, table string) (string, error) {
	cols, err := d.Columns(ctx, q, "", schema, table)
	if err != nil {
		return "", err
	}
	if len(cols) == 0 {
		return "", fmt.Errorf("table %s.%s not found", schema, table)
	}
	cons, err := d.Constraints(ctx, q, "", schema, table)
	if err != nil {
		return "", err
	}
	name := d.Qualify("", schema, table)
	var b strings.Builder
	b.WriteString("CREATE TABLE " + name + " (\n")
	// Generated and identity columns need their own syntax, so read them again.
	extras, err := columnExtras(ctx, q, schema, table)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, c := range cols {
		x := extras[c.Name]
		l := "    " + d.Quote(c.Name) + " " + c.Type
		if x.collation != "" {
			l += " COLLATE " + d.Quote(x.collation)
		}
		switch {
		case x.generated == "s":
			l += " GENERATED ALWAYS AS (" + stripParens(c.Default) + ") STORED"
		case x.generated == "v":
			l += " GENERATED ALWAYS AS (" + stripParens(c.Default) + ") VIRTUAL"
		case x.identity == "a":
			l += " GENERATED ALWAYS AS IDENTITY"
		case x.identity == "d":
			l += " GENERATED BY DEFAULT AS IDENTITY"
		case c.Default != "":
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
	b.WriteString(strings.Join(lines, ",\n"))
	b.WriteString("\n);\n")
	idx, err := d.indexDDL(ctx, q, schema, table)
	if err != nil {
		return "", err
	}
	b.WriteString(idx)
	var comment sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT obj_description(to_regclass($1), 'pg_class')`, name).Scan(&comment); err == nil && comment.String != "" {
		b.WriteString("\nCOMMENT ON TABLE " + name + " IS " + literal(comment.String) + ";\n")
	}
	for _, c := range cols {
		if c.Comment != "" {
			b.WriteString("COMMENT ON COLUMN " + name + "." + d.Quote(c.Name) + " IS " + literal(c.Comment) + ";\n")
		}
	}
	return b.String(), nil
}

// indexDDL lists the CREATE INDEX statements of indexes not backing a constraint.
func (d *Driver) indexDDL(ctx context.Context, q dialect.Querier, schema, table string) (string, error) {
	ixs, err := d.Indexes(ctx, q, "", schema, table)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, ix := range ixs {
		if ix.Constraint == "" {
			b.WriteString("\n" + ix.Definition + ";")
		}
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	return b.String(), nil
}

func (d *Driver) sequenceDDL(ctx context.Context, q dialect.Querier, schema, name string) (string, error) {
	var typ string
	var start, inc, minV, maxV, cache int64
	var cycle bool
	err := q.QueryRowContext(ctx, `
SELECT format_type(s.seqtypid, NULL), s.seqstart, s.seqincrement, s.seqmin, s.seqmax, s.seqcache, s.seqcycle
FROM pg_sequence s JOIN pg_class c ON c.oid = s.seqrelid JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, schema, name).Scan(&typ, &start, &inc, &minV, &maxV, &cache, &cycle)
	if err != nil {
		return "", err
	}
	s := fmt.Sprintf("CREATE SEQUENCE %s\n    AS %s\n    INCREMENT BY %d\n    MINVALUE %d\n    MAXVALUE %d\n    START WITH %d\n    CACHE %d",
		d.Qualify("", schema, name), typ, inc, minV, maxV, start, cache)
	if cycle {
		s += "\n    CYCLE"
	}
	return s + ";\n", nil
}

func literal(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// stripParens removes one pair of parentheses around the whole expression.
func stripParens(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return s
	}
	depth := 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && i != len(s)-1 {
				return s // "(a) AND (b)"
			}
		}
	}
	return strings.TrimSpace(s[1 : len(s)-1])
}

type columnExtra struct{ generated, identity, collation string }

// columnExtras reads what Columns leaves out: generated and identity columns, and
// collations that differ from the type's.
func columnExtras(ctx context.Context, q dialect.Querier, schema, table string) (map[string]columnExtra, error) {
	rows, err := q.QueryContext(ctx, `
SELECT a.attname, a.attgenerated::text, a.attidentity::text, COALESCE(co.collname, '')
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_collation co ON co.oid = a.attcollation AND a.attcollation <> (SELECT typcollation FROM pg_type WHERE oid = a.atttypid)
WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]columnExtra{}
	for rows.Next() {
		var name string
		var x columnExtra
		if err := rows.Scan(&name, &x.generated, &x.identity, &x.collation); err != nil {
			return nil, err
		}
		out[name] = x
	}
	return out, rows.Err()
}

// SchemaColumns lists the columns of every table and view of a schema.
func (d *Driver) SchemaColumns(ctx context.Context, q dialect.Querier, _, schema string) (map[string][]string, error) {
	return dialect.ColumnPairs(ctx, q, `
SELECT c.relname, a.attname FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY c.relname, a.attnum`, schema)
}

// ForeignKeys lists the foreign keys of a schema.
func (d *Driver) ForeignKeys(ctx context.Context, q dialect.Querier, _, schema string) ([]dialect.ForeignKey, error) {
	return dialect.KeyRows(ctx, q, `
SELECT con.conname, t.relname, a.attname, fn.nspname, ft.relname, fa.attname
FROM pg_constraint con
JOIN pg_class t ON t.oid = con.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace
JOIN pg_class ft ON ft.oid = con.confrelid JOIN pg_namespace fn ON fn.oid = ft.relnamespace
CROSS JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY k(col, ref, pos)
JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.col
JOIN pg_attribute fa ON fa.attrelid = con.confrelid AND fa.attnum = k.ref
WHERE n.nspname = $1 AND con.contype = 'f'
ORDER BY t.relname, con.conname, k.pos`, schema)
}

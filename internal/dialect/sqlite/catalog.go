package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/iagapie/quire/internal/dialect"
)

// Databases lists main and the attached databases.
func (d *Driver) Databases(ctx context.Context, q dialect.Querier) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT name FROM pragma_database_list WHERE name <> 'temp' ORDER BY seq")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Schemas returns nothing: SQLite has no schemas.
func (d *Driver) Schemas(context.Context, dialect.Querier, string) ([]string, error) { return nil, nil }

func master(database string) string {
	if database == "" {
		database = "main"
	}
	return dialect.QuoteDouble(database) + ".sqlite_master"
}

// Objects lists tables, views and triggers.
func (d *Driver) Objects(ctx context.Context, q dialect.Querier, database, _ string) ([]dialect.Object, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, type, tbl_name FROM `+master(database)+`
WHERE type IN ('table', 'view', 'trigger') AND name NOT LIKE 'sqlite\_%' ESCAPE '\'
ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Object
	for rows.Next() {
		var o dialect.Object
		var tbl string
		if err := rows.Scan(&o.Name, &o.Kind, &tbl); err != nil {
			return nil, err
		}
		if o.Kind == dialect.KindTrigger {
			o.Table, o.Detail = tbl, tbl
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func schemaArg(database string) string {
	if database == "" {
		return "main"
	}
	return database
}

// Columns lists the columns of a table or view.
func (d *Driver) Columns(ctx context.Context, q dialect.Querier, database, _, table string) ([]dialect.Column, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, type, "notnull", dflt_value, pk, hidden FROM pragma_table_xinfo(?, ?) ORDER BY cid`,
		table, schemaArg(database))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Column
	for rows.Next() {
		var (
			c       dialect.Column
			notNull bool
			def     sql.NullString
			hidden  int
		)
		if err := rows.Scan(&c.Name, &c.Type, &notNull, &def, &c.PK, &hidden); err != nil {
			return nil, err
		}
		if hidden == 1 {
			continue // hidden columns of virtual tables
		}
		c.Nullable = !notNull && c.PK == 0
		c.Default = def.String
		c.Generated = hidden == 2 || hidden == 3
		out = append(out, c)
	}
	return out, rows.Err()
}

// Indexes lists the indexes of a table.
func (d *Driver) Indexes(ctx context.Context, q dialect.Querier, database, _, table string) ([]dialect.Index, error) {
	schema := schemaArg(database)
	out, err := indexList(ctx, q, database, schema, table)
	if err != nil {
		return nil, err
	}
	for i := range out {
		cols, err := d.indexColumns(ctx, q, schema, out[i].Name, out[i].Definition)
		if err != nil {
			return nil, err
		}
		out[i].Columns = cols
	}
	// A rowid table's INTEGER PRIMARY KEY has no index of its own; show it as one.
	if !hasPrimary(out) {
		cols, err := d.Columns(ctx, q, database, "", table)
		if err == nil {
			var pk []dialect.IndexColumn
			for _, c := range cols {
				if c.PK > 0 {
					pk = append(pk, dialect.IndexColumn{Name: c.Name})
				}
			}
			if len(pk) > 0 {
				out = append([]dialect.Index{{Name: "PRIMARY KEY", Table: table, Columns: pk, Unique: true, Primary: true, SizeBytes: -1, Constraint: "PRIMARY KEY"}}, out...)
			}
		}
	}
	return out, nil
}

func hasPrimary(ixs []dialect.Index) bool {
	for _, ix := range ixs {
		if ix.Primary {
			return true
		}
	}
	return false
}

func (d *Driver) indexColumns(ctx context.Context, q dialect.Querier, schema, index, def string) ([]dialect.IndexColumn, error) {
	rows, err := q.QueryContext(ctx, `SELECT cid, name, "desc", coll FROM pragma_index_xinfo(?, ?) WHERE key = 1 ORDER BY seqno`, index, schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	exprs := keyExpressions(def)
	var out []dialect.IndexColumn
	k := 0
	for rows.Next() {
		var (
			cid  int
			name sql.NullString
			desc bool
			coll sql.NullString
		)
		if err := rows.Scan(&cid, &name, &desc, &coll); err != nil {
			return nil, err
		}
		c := dialect.IndexColumn{Name: name.String, Desc: desc}
		if coll.String != "" && coll.String != "BINARY" {
			c.Collation = coll.String
		}
		if cid == -2 && k < len(exprs) {
			c.Name, c.Expression = "", exprs[k]
		}
		out = append(out, c)
		k++
	}
	return out, rows.Err()
}

// keyExpressions returns the key parts written in a CREATE INDEX statement, without
// ordering and collation, for expression keys.
func keyExpressions(def string) []string {
	open := strings.Index(def, "(")
	if open < 0 {
		return nil
	}
	depth := 0
	var parts []string
	start := open + 1
	for i := open; i < len(def); i++ {
		switch def[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				parts = append(parts, def[start:i])
				return cleanKeys(parts)
			}
		case ',':
			if depth == 1 {
				parts = append(parts, def[start:i])
				start = i + 1
			}
		case '\'', '"', '`', '[':
			closer := def[i]
			if closer == '[' {
				closer = ']'
			}
			for i++; i < len(def) && def[i] != closer; i++ {
			}
		}
	}
	return cleanKeys(parts)
}

func cleanKeys(parts []string) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		up := strings.ToUpper(p)
		for _, suffix := range []string{" DESC", " ASC"} {
			if strings.HasSuffix(up, suffix) {
				p, up = strings.TrimSpace(p[:len(p)-len(suffix)]), up[:len(up)-len(suffix)]
			}
		}
		if n := strings.LastIndex(up, " COLLATE "); n >= 0 {
			p = strings.TrimSpace(p[:n])
		}
		out[i] = p
	}
	return out
}

func partialWhere(def string) string {
	toks := dialect.Lex(def, (&Driver{}).Split())
	depth := 0
	for _, t := range toks {
		switch t.Value {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth == 0 && t.Is("WHERE") {
			return strings.TrimSpace(def[t.End:])
		}
	}
	return ""
}

// Constraints lists the primary key, unique constraints and foreign keys.
func (d *Driver) Constraints(ctx context.Context, q dialect.Querier, database, _, table string) ([]dialect.Constraint, error) {
	var out []dialect.Constraint
	cols, err := d.Columns(ctx, q, database, "", table)
	if err != nil {
		return nil, err
	}
	var pk []string
	for k := 1; ; k++ {
		found := false
		for _, c := range cols {
			if c.PK == k {
				pk = append(pk, c.Name)
				found = true
			}
		}
		if !found {
			break
		}
	}
	if len(pk) > 0 {
		out = append(out, dialect.Constraint{Name: "PRIMARY KEY", Type: "PRIMARY KEY", Columns: pk})
	}
	ixs, err := d.Indexes(ctx, q, database, "", table)
	if err != nil {
		return nil, err
	}
	for _, ix := range ixs {
		if ix.Constraint != "" && !ix.Primary {
			c := dialect.Constraint{Name: ix.Name, Type: "UNIQUE"}
			for _, k := range ix.Columns {
				c.Columns = append(c.Columns, k.Name)
			}
			out = append(out, c)
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT id, seq, "table", "from", COALESCE("to", ''), on_update, on_delete FROM pragma_foreign_key_list(?, ?) ORDER BY id, seq`,
		table, schemaArg(database))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	last := -1
	for rows.Next() {
		var (
			id, seq                     int
			ref, from, to, onUpd, onDel string
		)
		if err := rows.Scan(&id, &seq, &ref, &from, &to, &onUpd, &onDel); err != nil {
			return nil, err
		}
		if id != last {
			out = append(out, dialect.Constraint{Name: fmt.Sprintf("fk_%s_%d", table, id), Type: "FOREIGN KEY", RefTable: ref, OnUpdate: onUpd, OnDelete: onDel})
			last = id
		}
		c := &out[len(out)-1]
		c.Columns = append(c.Columns, from)
		if to != "" {
			c.RefColumns = append(c.RefColumns, to)
		}
	}
	return out, rows.Err()
}

// DDL returns the CREATE statement stored by SQLite, followed by the indexes and
// triggers of a table.
func (d *Driver) DDL(ctx context.Context, q dialect.Querier, database, _ string, obj dialect.Object) (string, error) {
	var def sql.NullString
	err := q.QueryRowContext(ctx, `SELECT sql FROM `+master(database)+` WHERE name = ? AND type = ?`, obj.Name, obj.Kind).Scan(&def)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%s %s not found", obj.Kind, obj.Name)
	}
	if err != nil {
		return "", err
	}
	out := strings.TrimRight(def.String, "; \n") + ";\n"
	if obj.Kind != dialect.KindTable {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT sql FROM `+master(database)+` WHERE tbl_name = ? AND type IN ('index', 'trigger') AND sql IS NOT NULL ORDER BY type, name`, obj.Name)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var b strings.Builder
	b.WriteString(out)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return "", err
		}
		b.WriteString("\n" + strings.TrimRight(s, "; \n") + ";\n")
	}
	return b.String(), rows.Err()
}

// TableStats counts the rows of each table (SQLite keeps no estimates).
func (d *Driver) TableStats(ctx context.Context, q dialect.Querier, database, _ string) ([]dialect.TableStat, error) {
	objs, err := d.Objects(ctx, q, database, "")
	if err != nil {
		return nil, err
	}
	var out []dialect.TableStat
	for _, o := range objs {
		if o.Kind != dialect.KindTable && o.Kind != dialect.KindView {
			continue
		}
		st := dialect.TableStat{Name: o.Name, Kind: o.Kind, Rows: -1, Bytes: -1}
		if o.Kind == dialect.KindTable {
			_ = q.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+d.Qualify(database, "", o.Name)).Scan(&st.Rows)
			_ = q.QueryRowContext(ctx, "SELECT SUM(pgsize) FROM dbstat(?) WHERE name = ?", schemaArg(database), o.Name).Scan(&st.Bytes)
		}
		out = append(out, st)
	}
	return out, nil
}

// CreateIndex returns the CREATE INDEX statement for ix.
func (d *Driver) CreateIndex(database, _ string, ix dialect.Index, _, ifNotExists bool) (string, error) {
	if len(ix.Columns) == 0 {
		return "", errors.New("an index needs at least one column")
	}
	if ix.Name == "" {
		return "", errors.New("SQLite indexes need a name")
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
	b.WriteString(d.Qualify(database, "", ix.Name) + " ON " + d.Quote(ix.Table) + " (")
	for i, c := range ix.Columns {
		if i > 0 {
			b.WriteString(", ")
		}
		switch {
		case strings.TrimSpace(c.Expression) != "":
			b.WriteString(strings.TrimSpace(c.Expression))
		case c.Name != "":
			b.WriteString(d.Quote(c.Name))
		default:
			return "", errors.New("every index key needs a column or an expression")
		}
		if c.Collation != "" {
			b.WriteString(" COLLATE " + c.Collation)
		}
		if c.Desc {
			b.WriteString(" DESC")
		}
	}
	b.WriteString(")")
	if w := strings.TrimSpace(ix.Where); w != "" {
		b.WriteString(" WHERE " + w)
	}
	return b.String(), nil
}

// DropIndex returns the DROP INDEX statement.
func (d *Driver) DropIndex(database, _, _, name string, _ bool) string {
	return "DROP INDEX " + d.Qualify(database, "", name)
}

var functions = []string{
	"abs", "avg", "changes", "char", "coalesce", "count", "date", "datetime", "glob", "group_concat", "hex", "ifnull",
	"iif", "instr", "json", "json_array", "json_each", "json_extract", "json_group_array", "json_group_object",
	"json_object", "json_set", "julianday", "last_insert_rowid", "length", "like", "lower", "ltrim", "max", "min",
	"nullif", "printf", "quote", "random", "randomblob", "replace", "round", "rtrim", "sqlite_version", "strftime",
	"string_agg", "substr", "sum", "time", "total", "trim", "typeof", "unicode", "unixepoch", "upper", "zeroblob",
	"row_number", "rank", "dense_rank", "lag", "lead", "first_value", "last_value", "ntile",
}

var snippets = []dialect.Snippet{
	{Label: "sel", Detail: "SELECT … FROM … WHERE", Body: "SELECT ${*}\nFROM ${table}\nWHERE ${condition}\nLIMIT 100;"},
	{Label: "ins", Detail: "INSERT INTO … VALUES", Body: "INSERT INTO ${table} (${columns})\nVALUES (${values});"},
	{Label: "upd", Detail: "UPDATE … SET … WHERE", Body: "UPDATE ${table}\nSET ${column} = ${value}\nWHERE ${condition};"},
	{Label: "del", Detail: "DELETE FROM … WHERE", Body: "DELETE FROM ${table}\nWHERE ${condition};"},
	{Label: "ct", Detail: "CREATE TABLE", Body: "CREATE TABLE ${name} (\n    id INTEGER PRIMARY KEY,\n    ${column} TEXT NOT NULL\n);"},
	{Label: "trg", Detail: "CREATE TRIGGER", Body: "CREATE TRIGGER ${name}\nAFTER INSERT ON ${table}\nBEGIN\n    ${};\nEND;"},
	{Label: "tx", Detail: "BEGIN … COMMIT", Body: "BEGIN;\n${}\nCOMMIT;"},
}

func indexList(ctx context.Context, q dialect.Querier, database, schema, table string) ([]dialect.Index, error) {
	rows, err := q.QueryContext(ctx, `SELECT il.name, il."unique", il.origin, COALESCE(m.sql, '')
FROM pragma_index_list(?, ?) il LEFT JOIN `+master(database)+` m ON m.type = 'index' AND m.name = il.name
ORDER BY il.origin = 'pk' DESC, il.name`, table, schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Index
	for rows.Next() {
		var ix dialect.Index
		var origin string
		if err := rows.Scan(&ix.Name, &ix.Unique, &origin, &ix.Definition); err != nil {
			return nil, err
		}
		ix.Table = table
		ix.Primary = origin == "pk"
		ix.SizeBytes = -1
		if origin == "u" || origin == "pk" {
			ix.Constraint = ix.Name
		}
		ix.Where = partialWhere(ix.Definition)
		out = append(out, ix)
	}
	return out, rows.Err()
}

// SchemaColumns lists the columns of every table and view of a database.
func (d *Driver) SchemaColumns(ctx context.Context, q dialect.Querier, database, _ string) (map[string][]string, error) {
	if database == "" {
		database = "main"
	}
	return dialect.ColumnPairs(ctx, q, `SELECT m.name, p.name FROM `+d.Quote(database)+`.sqlite_schema m
JOIN pragma_table_info(m.name, ?) p
WHERE m.type IN ('table', 'view') AND m.name NOT LIKE 'sqlite\_%' ESCAPE '\'
ORDER BY m.name, p.cid`, database)
}

// ForeignKeys lists the foreign keys of a database.
func (d *Driver) ForeignKeys(ctx context.Context, q dialect.Querier, database, _ string) ([]dialect.ForeignKey, error) {
	if database == "" {
		database = "main"
	}
	return dialect.KeyRows(ctx, q, `SELECT CAST(f.id AS TEXT), m.name, f."from", NULL, f."table", COALESCE(f."to", '')
FROM `+d.Quote(database)+`.sqlite_schema m
JOIN pragma_foreign_key_list(m.name, ?) f
WHERE m.type = 'table' ORDER BY m.name, f.id, f.seq`, database)
}

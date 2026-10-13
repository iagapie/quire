package oracle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/iagapie/quire/internal/dialect"
)

// Databases returns nothing: a connection reaches one database (or PDB).
func (d *Driver) Databases(context.Context, dialect.Querier) ([]string, error) { return nil, nil }

// Schemas lists the users, the current schema first and Oracle's own last.
func (d *Driver) Schemas(ctx context.Context, q dialect.Querier, _ string) ([]string, error) {
	return dialect.Strings(ctx, q, `SELECT username FROM all_users
ORDER BY CASE WHEN username = SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') THEN 0 WHEN oracle_maintained = 'Y' THEN 2 ELSE 1 END, username`)
}

var kinds = map[string]string{
	"TABLE": dialect.KindTable, "VIEW": dialect.KindView, "MATERIALIZED VIEW": dialect.KindMatView,
	"FUNCTION": dialect.KindFunction, "PROCEDURE": dialect.KindProcedure, "PACKAGE": dialect.KindPackage,
	"SEQUENCE": dialect.KindSequence, "TRIGGER": dialect.KindTrigger,
}

// Objects lists the tables, views, routines, packages, sequences and triggers of a
// schema. Invalid objects say so in their detail.
func (d *Driver) Objects(ctx context.Context, q dialect.Querier, _, schema string) ([]dialect.Object, error) {
	rows, err := q.QueryContext(ctx, `
SELECT o.object_name, o.object_type, c.comments, t.table_name, o.status
FROM all_objects o
LEFT JOIN all_tab_comments c ON c.owner = o.owner AND c.table_name = o.object_name AND o.object_type IN ('TABLE', 'VIEW')
LEFT JOIN all_triggers t ON t.owner = o.owner AND t.trigger_name = o.object_name AND o.object_type = 'TRIGGER'
WHERE o.owner = :owner AND o.secondary = 'N' AND o.object_name NOT LIKE 'BIN$%'
  AND o.object_type IN ('TABLE', 'VIEW', 'MATERIALIZED VIEW', 'FUNCTION', 'PROCEDURE', 'PACKAGE', 'SEQUENCE', 'TRIGGER')
  AND NOT (o.object_type = 'TABLE' AND EXISTS (SELECT 1 FROM all_mviews m WHERE m.owner = o.owner AND m.mview_name = o.object_name))
ORDER BY o.object_name`, sql.Named("owner", schema))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Object
	for rows.Next() {
		var name, typ, status string
		var comment, table sql.NullString
		if err := rows.Scan(&name, &typ, &comment, &table, &status); err != nil {
			return nil, err
		}
		o := dialect.Object{Name: name, Kind: kinds[typ], Comment: comment.String, Table: table.String, Detail: table.String}
		if status == "INVALID" {
			o.Detail = strings.TrimSpace(o.Detail + " invalid")
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

const typeExpr = `CASE
  WHEN c.data_type IN ('VARCHAR2', 'CHAR') THEN c.data_type || '(' || c.char_length || CASE c.char_used WHEN 'C' THEN ' CHAR' ELSE '' END || ')'
  WHEN c.data_type IN ('NVARCHAR2', 'NCHAR') THEN c.data_type || '(' || c.char_length || ')'
  WHEN c.data_type = 'RAW' THEN 'RAW(' || c.data_length || ')'
  WHEN c.data_type = 'NUMBER' AND c.data_precision IS NOT NULL THEN
    'NUMBER(' || c.data_precision || CASE WHEN c.data_scale > 0 THEN ', ' || c.data_scale END || ')'
  WHEN c.data_type = 'NUMBER' AND c.data_scale = 0 THEN 'INTEGER'
  WHEN c.data_type = 'FLOAT' THEN 'FLOAT(' || c.data_precision || ')'
  ELSE c.data_type END`

// Columns lists the columns of a table or view.
func (d *Driver) Columns(ctx context.Context, q dialect.Querier, _, schema, table string) ([]dialect.Column, error) {
	rows, err := q.QueryContext(ctx, `
SELECT c.column_name, `+typeExpr+`, c.nullable, c.data_default, NVL(pk.position, 0), cc.comments,
       CASE WHEN c.virtual_column = 'YES' OR c.identity_column = 'YES' THEN 1 ELSE 0 END
FROM all_tab_cols c
LEFT JOIN (SELECT acc.column_name, acc.position FROM all_constraints ac
           JOIN all_cons_columns acc ON acc.owner = ac.owner AND acc.constraint_name = ac.constraint_name
           WHERE ac.owner = :owner AND ac.table_name = :tbl AND ac.constraint_type = 'P') pk ON pk.column_name = c.column_name
LEFT JOIN all_col_comments cc ON cc.owner = c.owner AND cc.table_name = c.table_name AND cc.column_name = c.column_name
WHERE c.owner = :owner AND c.table_name = :tbl AND c.hidden_column = 'NO'
ORDER BY c.column_id`, sql.Named("owner", schema), sql.Named("tbl", table))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Column
	for rows.Next() {
		var (
			c            dialect.Column
			nullable     string
			def, comment sql.NullString
			generated    int
		)
		if err := rows.Scan(&c.Name, &c.Type, &nullable, &def, &c.PK, &comment, &generated); err != nil {
			return nil, err
		}
		c.Nullable, c.Default, c.Comment, c.Generated = nullable == "Y", strings.TrimSpace(def.String), comment.String, generated == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

var quotedColumn = regexp.MustCompile(`^"([^"]+)"$`)

// Indexes lists the indexes of a table with their key parts. Descending keys are
// stored as expressions of the column, and read back as columns.
func (d *Driver) Indexes(ctx context.Context, q dialect.Querier, _, schema, table string) ([]dialect.Index, error) {
	rows, err := q.QueryContext(ctx, `
SELECT i.index_name, i.uniqueness, i.index_type, i.tablespace_name, i.visibility, c.column_name, c.descend,
       e.column_expression, con.constraint_name, con.constraint_type
FROM all_indexes i
JOIN all_ind_columns c ON c.index_owner = i.owner AND c.index_name = i.index_name
LEFT JOIN all_ind_expressions e ON e.index_owner = i.owner AND e.index_name = i.index_name AND e.column_position = c.column_position
LEFT JOIN all_constraints con ON con.owner = i.table_owner AND con.table_name = i.table_name AND con.index_name = i.index_name
                             AND con.constraint_type IN ('P', 'U')
WHERE i.table_owner = :owner AND i.table_name = :tbl AND i.index_type <> 'LOB'
ORDER BY CASE con.constraint_type WHEN 'P' THEN 0 ELSE 1 END, i.index_name, c.column_position`, sql.Named("owner", schema), sql.Named("tbl", table))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Index
	for rows.Next() {
		var (
			name, uniq, typ, col, desc                 string
			tablespace, visibility, expr, cname, ctype sql.NullString
		)
		if err := rows.Scan(&name, &uniq, &typ, &tablespace, &visibility, &col, &desc, &expr, &cname, &ctype); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Name != name {
			ix := dialect.Index{
				Name: name, Table: table, Unique: uniq == "UNIQUE", Primary: ctype.String == "P", Tablespace: tablespace.String,
				Invisible: visibility.String == "INVISIBLE", Constraint: cname.String, SizeBytes: -1, Method: "normal",
			}
			if strings.HasPrefix(typ, "BITMAP") {
				ix.Method = "bitmap"
			}
			out = append(out, ix)
		}
		cur := &out[len(out)-1]
		ic := dialect.IndexColumn{Name: col, Desc: desc == "DESC"}
		if e := strings.TrimSpace(expr.String); e != "" {
			if m := quotedColumn.FindStringSubmatch(e); m != nil {
				ic.Name = m[1]
			} else {
				ic.Name, ic.Expression = "", e
			}
		}
		cur.Columns = append(cur.Columns, ic)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if def, err := d.CreateIndex("", schema, out[i], false, false); err == nil {
			out[i].Definition = def
		}
	}
	return out, nil
}

var notNullCheck = regexp.MustCompile(`^"[^"]+" IS NOT NULL$`)

var constraintTypes = map[string]string{"P": "PRIMARY KEY", "U": "UNIQUE", "R": "FOREIGN KEY", "C": "CHECK"}

// Constraints lists primary keys, unique, foreign key and check constraints; the
// NOT NULL checks Oracle creates for columns are left out.
func (d *Driver) Constraints(ctx context.Context, q dialect.Querier, _, schema, table string) ([]dialect.Constraint, error) {
	rows, err := q.QueryContext(ctx, `
SELECT c.constraint_name, c.constraint_type, cc.column_name, c.r_owner, r.table_name, rc.column_name, c.delete_rule,
       c.search_condition_vc, c.generated
FROM all_constraints c
LEFT JOIN all_cons_columns cc ON cc.owner = c.owner AND cc.constraint_name = c.constraint_name
LEFT JOIN all_constraints r ON r.owner = c.r_owner AND r.constraint_name = c.r_constraint_name
LEFT JOIN all_cons_columns rc ON rc.owner = c.r_owner AND rc.constraint_name = c.r_constraint_name AND rc.position = cc.position
WHERE c.owner = :owner AND c.table_name = :tbl AND c.constraint_type IN ('P', 'U', 'R', 'C')
ORDER BY DECODE(c.constraint_type, 'P', 0, 'U', 1, 'R', 2, 3), c.constraint_name, cc.position`, sql.Named("owner", schema), sql.Named("tbl", table))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Constraint
	for rows.Next() {
		var (
			name, typ, generated                                   string
			col, refOwner, refTable, refCol, deleteRule, condition sql.NullString
		)
		if err := rows.Scan(&name, &typ, &col, &refOwner, &refTable, &refCol, &deleteRule, &condition, &generated); err != nil {
			return nil, err
		}
		if typ == "C" && generated == "GENERATED NAME" && notNullCheck.MatchString(strings.TrimSpace(condition.String)) {
			continue
		}
		if len(out) == 0 || out[len(out)-1].Name != name {
			c := dialect.Constraint{Name: name, Type: constraintTypes[typ], RefTable: refTable.String}
			if refOwner.String != schema {
				c.RefSchema = refOwner.String
			}
			if typ == "R" && deleteRule.String != "NO ACTION" {
				c.OnDelete = deleteRule.String
			}
			if typ == "C" {
				c.Definition = "CHECK (" + strings.TrimSpace(condition.String) + ")"
			}
			out = append(out, c)
		}
		cur := &out[len(out)-1]
		if col.Valid {
			cur.Columns = append(cur.Columns, col.String)
		}
		if refCol.Valid {
			cur.RefColumns = append(cur.RefColumns, refCol.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		c := &out[i]
		switch c.Type {
		case "PRIMARY KEY", "UNIQUE":
			c.Definition = c.Type + " (" + d.quoteList(c.Columns) + ")"
		case "FOREIGN KEY":
			c.Definition = "FOREIGN KEY (" + d.quoteList(c.Columns) + ") REFERENCES " + d.Qualify("", c.RefSchema, c.RefTable) +
				" (" + d.quoteList(c.RefColumns) + ")"
			if c.OnDelete != "" {
				c.Definition += " ON DELETE " + c.OnDelete
			}
		}
	}
	return out, nil
}

func (d *Driver) quoteList(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = d.Quote(c)
	}
	return strings.Join(q, ", ")
}

// TableStats estimates rows and sizes from the optimizer statistics.
func (d *Driver) TableStats(ctx context.Context, q dialect.Querier, _, schema string) ([]dialect.TableStat, error) {
	rows, err := q.QueryContext(ctx, `
SELECT table_name, 'table', NVL(num_rows, -1), NVL(num_rows * avg_row_len, -1)
FROM all_tables WHERE owner = :owner AND nested = 'NO' AND secondary = 'N'
UNION ALL
SELECT view_name, 'view', -1, -1 FROM all_views WHERE owner = :owner
ORDER BY 1`, sql.Named("owner", schema))
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

var metadataTypes = map[string]string{
	dialect.KindTable: "TABLE", dialect.KindView: "VIEW", dialect.KindMatView: "MATERIALIZED_VIEW",
	dialect.KindFunction: "FUNCTION", dialect.KindProcedure: "PROCEDURE", dialect.KindPackage: "PACKAGE",
	dialect.KindSequence: "SEQUENCE", dialect.KindTrigger: "TRIGGER",
}

// DDL returns the definition of an object from DBMS_METADATA, with statement
// terminators, so the script runs as it is. A table comes with its indexes and
// comments.
func (d *Driver) DDL(ctx context.Context, q dialect.Querier, _, schema string, obj dialect.Object) (string, error) {
	typ, ok := metadataTypes[obj.Kind]
	if !ok {
		return "", dialect.ErrUnsupported
	}
	// The transform settings belong to the session: use one connection throughout.
	if db, ok := q.(*sql.DB); ok {
		c, err := db.Conn(ctx)
		if err != nil {
			return "", err
		}
		defer func() { _ = c.Close() }()
		q = c
	}
	if _, err := q.ExecContext(ctx, `BEGIN
  DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'SQLTERMINATOR', TRUE);
  DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'PRETTY', TRUE);
  DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'SEGMENT_ATTRIBUTES', FALSE);
  DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'STORAGE', FALSE);
END;`); err != nil {
		return "", err
	}
	get := func(query string, args ...any) (string, error) {
		var s sql.NullString
		err := q.QueryRowContext(ctx, query, args...).Scan(&s)
		return strings.TrimSpace(s.String), err
	}
	ddl, err := get(`SELECT DBMS_METADATA.GET_DDL(:1, :2, :3) FROM dual`, typ, obj.Name, schema)
	if err != nil {
		return "", err
	}
	if obj.Kind != dialect.KindTable {
		return ddl + "\n", nil
	}
	var b strings.Builder
	b.WriteString(ddl + "\n")
	ixs, err := d.Indexes(ctx, q, "", schema, obj.Name)
	if err != nil {
		return "", err
	}
	for _, ix := range ixs {
		if ix.Constraint != "" {
			continue
		}
		s, err := get(`SELECT DBMS_METADATA.GET_DDL('INDEX', :1, :2) FROM dual`, ix.Name, schema)
		if err != nil {
			return "", err
		}
		b.WriteString("\n" + s + "\n")
	}
	comments, err := get(`SELECT DBMS_METADATA.GET_DEPENDENT_DDL('COMMENT', :1, :2) FROM dual`, obj.Name, schema)
	if err != nil && !strings.Contains(err.Error(), "ORA-31608") {
		return "", err
	}
	if comments != "" {
		b.WriteString("\n" + comments + "\n")
	}
	return b.String(), nil
}

// CreateIndex returns the CREATE INDEX statement for ix, or ALTER TABLE … ADD
// PRIMARY KEY.
func (d *Driver) CreateIndex(_, schema string, ix dialect.Index, _, _ bool) (string, error) {
	if len(ix.Columns) == 0 {
		return "", errors.New("an index needs at least one column")
	}
	parts := make([]string, 0, len(ix.Columns))
	for _, c := range ix.Columns {
		var p string
		switch {
		case strings.TrimSpace(c.Expression) != "":
			p = strings.TrimSpace(c.Expression)
		case c.Name != "":
			p = d.Quote(c.Name)
		default:
			return "", errors.New("every index key needs a column or an expression")
		}
		if c.Desc {
			p += " DESC"
		}
		parts = append(parts, p)
	}
	table := d.Qualify("", schema, ix.Table)
	if ix.Primary {
		s := "ALTER TABLE " + table + " ADD "
		if ix.Name != "" {
			s += "CONSTRAINT " + d.Quote(ix.Name) + " "
		}
		return s + "PRIMARY KEY (" + strings.Join(parts, ", ") + ")", nil
	}
	if ix.Name == "" {
		return "", errors.New("an Oracle index needs a name")
	}
	var b strings.Builder
	b.WriteString("CREATE ")
	switch {
	case ix.Method == "bitmap":
		b.WriteString("BITMAP ")
	case ix.Unique:
		b.WriteString("UNIQUE ")
	}
	fmt.Fprintf(&b, "INDEX %s ON %s (%s)", d.Qualify("", schema, ix.Name), table, strings.Join(parts, ", "))
	if ix.Tablespace != "" {
		b.WriteString(" TABLESPACE " + d.Quote(ix.Tablespace))
	}
	if ix.Invisible {
		b.WriteString(" INVISIBLE")
	}
	return b.String(), nil
}

// DropIndex returns the DROP INDEX statement.
func (d *Driver) DropIndex(_, schema, _, name string, _ bool) string {
	return "DROP INDEX " + d.Qualify("", schema, name)
}

var functions = []string{
	"abs", "add_months", "avg", "cast", "ceil", "coalesce", "concat", "count", "current_date", "current_timestamp",
	"decode", "dense_rank", "extract", "first_value", "floor", "greatest", "initcap", "instr", "json_object",
	"json_query", "json_table", "json_value", "lag", "last_day", "last_value", "lead", "least", "length", "listagg",
	"lower", "lpad", "ltrim", "max", "min", "mod", "months_between", "next_day", "nullif", "nvl", "nvl2", "rank",
	"regexp_instr", "regexp_like", "regexp_replace", "regexp_substr", "replace", "round", "row_number", "rpad", "rtrim",
	"substr", "sum", "sys_context", "sys_guid", "sysdate", "systimestamp", "to_char", "to_date", "to_number",
	"to_timestamp", "translate", "trim", "trunc", "upper",
}

var snippets = []dialect.Snippet{
	{Label: "sel", Detail: "SELECT … FROM … WHERE", Body: "SELECT ${*}\nFROM ${table}\nWHERE ${condition}\nFETCH FIRST 100 ROWS ONLY;"},
	{Label: "ins", Detail: "INSERT INTO … VALUES", Body: "INSERT INTO ${table} (${columns})\nVALUES (${values});"},
	{Label: "upd", Detail: "UPDATE … SET … WHERE", Body: "UPDATE ${table}\nSET ${column} = ${value}\nWHERE ${condition};"},
	{Label: "del", Detail: "DELETE FROM … WHERE", Body: "DELETE FROM ${table}\nWHERE ${condition};"},
	{Label: "plsql", Detail: "DECLARE … BEGIN … END", Body: "DECLARE\n    ${n} NUMBER;\nBEGIN\n    ${DBMS_OUTPUT.PUT_LINE('hello');}\nEND;\n/"},
	{Label: "ct", Detail: "CREATE TABLE", Body: "CREATE TABLE ${name} (\n    id NUMBER GENERATED ALWAYS AS IDENTITY PRIMARY KEY,\n    ${column} VARCHAR2(255 CHAR) NOT NULL,\n    created_at TIMESTAMP DEFAULT SYSTIMESTAMP NOT NULL\n);"},
	{Label: "proc", Detail: "CREATE PROCEDURE", Body: "CREATE OR REPLACE PROCEDURE ${name} (${p_id} IN NUMBER) AS\nBEGIN\n    ${NULL;}\nEND;\n/"},
}

// SchemaColumns lists the columns of every table and view of a schema.
func (d *Driver) SchemaColumns(ctx context.Context, q dialect.Querier, _, schema string) (map[string][]string, error) {
	return dialect.ColumnPairs(ctx, q, `SELECT table_name, column_name FROM all_tab_columns
WHERE owner = :owner ORDER BY table_name, column_id`, sql.Named("owner", schema))
}

// ForeignKeys lists the foreign keys of a schema.
func (d *Driver) ForeignKeys(ctx context.Context, q dialect.Querier, _, schema string) ([]dialect.ForeignKey, error) {
	return dialect.KeyRows(ctx, q, `SELECT c.constraint_name, c.table_name, cc.column_name, r.owner, r.table_name, rc.column_name
FROM all_constraints c
JOIN all_cons_columns cc ON cc.owner = c.owner AND cc.constraint_name = c.constraint_name
JOIN all_constraints r ON r.owner = c.r_owner AND r.constraint_name = c.r_constraint_name
JOIN all_cons_columns rc ON rc.owner = r.owner AND rc.constraint_name = r.constraint_name AND rc.position = cc.position
WHERE c.owner = :owner AND c.constraint_type = 'R'
ORDER BY c.table_name, c.constraint_name, cc.position`, sql.Named("owner", schema))
}

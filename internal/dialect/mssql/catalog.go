package mssql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/iagapie/quire/internal/dialect"
)

// Databases lists the online databases the login can use, user databases first.
func (d *Driver) Databases(ctx context.Context, q dialect.Querier) ([]string, error) {
	return dialect.Strings(ctx, q, `SELECT name FROM sys.databases
WHERE state = 0 AND HAS_DBACCESS(name) = 1
ORDER BY CASE WHEN database_id <= 4 THEN 1 ELSE 0 END, name`)
}

// Schemas lists the schemas of the database; the fixed role schemas are left out.
func (d *Driver) Schemas(ctx context.Context, q dialect.Querier, _ string) ([]string, error) {
	return dialect.Strings(ctx, q, `SELECT s.name FROM sys.schemas s
WHERE s.name <> 'guest' AND NOT (s.schema_id >= 16384 AND s.schema_id < 16400)
ORDER BY CASE WHEN s.name IN ('sys', 'INFORMATION_SCHEMA') THEN 1 ELSE 0 END, s.name`)
}

// Objects lists tables, views, routines, sequences and triggers of a schema.
func (d *Driver) Objects(ctx context.Context, q dialect.Querier, _, schema string) ([]dialect.Object, error) {
	rows, err := q.QueryContext(ctx, `
SELECT o.name,
       CASE o.type WHEN 'U' THEN 'table' WHEN 'V' THEN 'view' WHEN 'P' THEN 'procedure' WHEN 'PC' THEN 'procedure'
                   WHEN 'SO' THEN 'sequence' WHEN 'TR' THEN 'trigger' ELSE 'function' END,
       CAST(COALESCE(ep.value, '') AS nvarchar(4000)),
       CASE WHEN o.type = 'TR' THEN COALESCE(OBJECT_NAME(o.parent_object_id), '') ELSE '' END,
       CAST(o.object_id AS varchar(20))
FROM sys.objects o
JOIN sys.schemas s ON s.schema_id = o.schema_id
LEFT JOIN sys.extended_properties ep ON ep.class = 1 AND ep.major_id = o.object_id AND ep.minor_id = 0 AND ep.name = 'MS_Description'
WHERE s.name = @p1 AND o.type IN ('U', 'V', 'P', 'PC', 'FN', 'IF', 'TF', 'FS', 'FT', 'SO', 'TR') AND o.is_ms_shipped = 0
ORDER BY o.name`, schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Object
	for rows.Next() {
		var o dialect.Object
		if err := rows.Scan(&o.Name, &o.Kind, &o.Comment, &o.Table, &o.ID); err != nil {
			return nil, err
		}
		o.Detail = o.Table
		out = append(out, o)
	}
	return out, rows.Err()
}

// typeExpr renders the declared type of a sys.columns row c joined to sys.types t.
const typeExpr = `CASE
  WHEN t.name IN ('varchar', 'char', 'varbinary', 'binary')
    THEN t.name + '(' + CASE WHEN c.max_length = -1 THEN 'max' ELSE CAST(c.max_length AS varchar(10)) END + ')'
  WHEN t.name IN ('nvarchar', 'nchar')
    THEN t.name + '(' + CASE WHEN c.max_length = -1 THEN 'max' ELSE CAST(c.max_length / 2 AS varchar(10)) END + ')'
  WHEN t.name IN ('decimal', 'numeric') THEN t.name + '(' + CAST(c.precision AS varchar(10)) + ', ' + CAST(c.scale AS varchar(10)) + ')'
  WHEN t.name IN ('datetime2', 'time', 'datetimeoffset') THEN t.name + '(' + CAST(c.scale AS varchar(10)) + ')'
  ELSE t.name END`

// Columns lists the columns of a table or view.
func (d *Driver) Columns(ctx context.Context, q dialect.Querier, _, schema, table string) ([]dialect.Column, error) {
	rows, err := q.QueryContext(ctx, `
SELECT c.name, `+typeExpr+`, c.is_nullable, COALESCE(dc.definition, ''), COALESCE(ic.key_ordinal, 0),
       CAST(COALESCE(ep.value, '') AS nvarchar(4000)),
       CAST(CASE WHEN c.is_computed = 1 OR c.is_identity = 1 OR t.name IN ('timestamp', 'rowversion') THEN 1 ELSE 0 END AS bit)
FROM sys.columns c
JOIN sys.types t ON t.user_type_id = c.user_type_id
LEFT JOIN sys.default_constraints dc ON dc.object_id = c.default_object_id
LEFT JOIN sys.indexes pk ON pk.object_id = c.object_id AND pk.is_primary_key = 1
LEFT JOIN sys.index_columns ic ON ic.object_id = c.object_id AND ic.index_id = pk.index_id AND ic.column_id = c.column_id
LEFT JOIN sys.extended_properties ep ON ep.class = 1 AND ep.major_id = c.object_id AND ep.minor_id = c.column_id AND ep.name = 'MS_Description'
WHERE c.object_id = OBJECT_ID(@p1)
ORDER BY c.column_id`, d.Qualify("", schema, table))
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
		c.Default = stripParens(c.Default)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Indexes lists the indexes of a table with their key and included columns.
func (d *Driver) Indexes(ctx context.Context, q dialect.Querier, _, schema, table string) ([]dialect.Index, error) {
	rows, err := q.QueryContext(ctx, `
SELECT i.name, i.is_unique, i.is_primary_key, i.type, COALESCE(i.filter_definition, ''), i.fill_factor,
       CASE WHEN i.is_primary_key = 1 OR i.is_unique_constraint = 1 THEN i.name ELSE '' END,
       COALESCE((SELECT SUM(a.total_pages) * 8192 FROM sys.partitions p
                 JOIN sys.allocation_units a ON a.container_id = p.partition_id
                 WHERE p.object_id = i.object_id AND p.index_id = i.index_id), -1),
       COALESCE(c.name, ''), COALESCE(ic.is_descending_key, 0), COALESCE(ic.is_included_column, 0)
FROM sys.indexes i
LEFT JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
LEFT JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
WHERE i.object_id = OBJECT_ID(@p1) AND i.type > 0 AND i.is_hypothetical = 0
ORDER BY i.is_primary_key DESC, i.name, ic.is_included_column, ic.key_ordinal, ic.index_column_id`, d.Qualify("", schema, table))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Index
	for rows.Next() {
		var (
			ix            dialect.Index
			typ, fill     int
			col           string
			desc, include bool
		)
		if err := rows.Scan(&ix.Name, &ix.Unique, &ix.Primary, &typ, &ix.Where, &fill, &ix.Constraint, &ix.SizeBytes, &col, &desc, &include); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Name != ix.Name {
			ix.Table, ix.FillFactor = table, fill
			ix.Where = stripParens(ix.Where)
			ix.Clustered = typ == 1 || typ == 5
			if typ == 5 || typ == 6 {
				ix.Method = "columnstore"
			}
			out = append(out, ix)
		}
		cur := &out[len(out)-1]
		switch {
		case col == "":
		case include:
			cur.Include = append(cur.Include, col)
		default:
			cur.Columns = append(cur.Columns, dialect.IndexColumn{Name: col, Desc: desc})
		}
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

// Constraints lists primary keys, unique, foreign key and check constraints.
func (d *Driver) Constraints(ctx context.Context, q dialect.Querier, _, schema, table string) ([]dialect.Constraint, error) {
	name := d.Qualify("", schema, table)
	var out []dialect.Constraint
	// Primary keys and unique constraints.
	rows, err := q.QueryContext(ctx, `
SELECT kc.name, kc.type, c.name
FROM sys.key_constraints kc
JOIN sys.index_columns ic ON ic.object_id = kc.parent_object_id AND ic.index_id = kc.unique_index_id
JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
WHERE kc.parent_object_id = OBJECT_ID(@p1) AND ic.is_included_column = 0
ORDER BY CASE kc.type WHEN 'PK' THEN 0 ELSE 1 END, kc.name, ic.key_ordinal`, name)
	if err != nil {
		return nil, err
	}
	err = scanGroups(rows, func(cname, typ, col string) {
		if len(out) == 0 || out[len(out)-1].Name != cname {
			t := "UNIQUE"
			if typ == "PK" {
				t = "PRIMARY KEY"
			}
			out = append(out, dialect.Constraint{Name: cname, Type: t})
		}
		out[len(out)-1].Columns = append(out[len(out)-1].Columns, col)
	})
	if err != nil {
		return nil, err
	}
	// Foreign keys.
	rows, err = q.QueryContext(ctx, `
SELECT fk.name, c.name, rs.name, rt.name, rc.name,
       REPLACE(fk.update_referential_action_desc, '_', ' '), REPLACE(fk.delete_referential_action_desc, '_', ' ')
FROM sys.foreign_keys fk
JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
JOIN sys.columns c ON c.object_id = fkc.parent_object_id AND c.column_id = fkc.parent_column_id
JOIN sys.tables rt ON rt.object_id = fk.referenced_object_id
JOIN sys.schemas rs ON rs.schema_id = rt.schema_id
JOIN sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
WHERE fk.parent_object_id = OBJECT_ID(@p1)
ORDER BY fk.name, fkc.constraint_column_id`, name)
	if err != nil {
		return nil, err
	}
	func() {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c dialect.Constraint
			var col, refCol string
			if err = rows.Scan(&c.Name, &col, &c.RefSchema, &c.RefTable, &refCol, &c.OnUpdate, &c.OnDelete); err != nil {
				return
			}
			if len(out) == 0 || out[len(out)-1].Name != c.Name {
				c.Type = "FOREIGN KEY"
				out = append(out, c)
			}
			cur := &out[len(out)-1]
			cur.Columns = append(cur.Columns, col)
			cur.RefColumns = append(cur.RefColumns, refCol)
		}
		err = rows.Err()
	}()
	if err != nil {
		return nil, err
	}
	// Checks.
	rows, err = q.QueryContext(ctx, `SELECT cc.name, cc.definition, COALESCE(c.name, '')
FROM sys.check_constraints cc
LEFT JOIN sys.columns c ON c.object_id = cc.parent_object_id AND c.column_id = cc.parent_column_id
WHERE cc.parent_object_id = OBJECT_ID(@p1) ORDER BY cc.name`, name)
	if err != nil {
		return nil, err
	}
	err = scanGroups(rows, func(cname, def, col string) {
		c := dialect.Constraint{Name: cname, Type: "CHECK", Definition: "CHECK " + def}
		if col != "" {
			c.Columns = []string{col}
		}
		out = append(out, c)
	})
	if err != nil {
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
			if c.OnDelete != "NO ACTION" {
				c.Definition += " ON DELETE " + c.OnDelete
			}
			if c.OnUpdate != "NO ACTION" {
				c.Definition += " ON UPDATE " + c.OnUpdate
			}
			if c.RefSchema == schema {
				c.RefSchema = ""
			}
		}
	}
	return out, nil
}

// scanGroups reads rows of three strings.
func scanGroups(rows *sql.Rows, f func(a, b, c string)) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var a, b, c string
		if err := rows.Scan(&a, &b, &c); err != nil {
			return err
		}
		f(a, b, c)
	}
	return rows.Err()
}

func (d *Driver) quoteList(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = d.Quote(c)
	}
	return strings.Join(q, ", ")
}

// TableStats reads row counts and sizes from the partition metadata.
func (d *Driver) TableStats(ctx context.Context, q dialect.Querier, _, schema string) ([]dialect.TableStat, error) {
	rows, err := q.QueryContext(ctx, `
SELECT o.name, CASE o.type WHEN 'V' THEN 'view' ELSE 'table' END,
       CASE WHEN o.type = 'U' THEN COALESCE((SELECT SUM(p.rows) FROM sys.partitions p WHERE p.object_id = o.object_id AND p.index_id IN (0, 1)), -1) ELSE -1 END,
       CASE WHEN o.type = 'U' THEN COALESCE((SELECT SUM(a.total_pages) * 8192 FROM sys.partitions p
            JOIN sys.allocation_units a ON a.container_id = p.partition_id WHERE p.object_id = o.object_id), -1) ELSE -1 END
FROM sys.objects o JOIN sys.schemas s ON s.schema_id = o.schema_id
WHERE s.name = @p1 AND o.type IN ('U', 'V') AND o.is_ms_shipped = 0
ORDER BY o.name`, schema)
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

// DDL returns the definition of an object. Statements are separated by GO, so the
// script runs as it is.
func (d *Driver) DDL(ctx context.Context, q dialect.Querier, _, schema string, obj dialect.Object) (string, error) {
	name := d.Qualify("", schema, obj.Name)
	switch obj.Kind {
	case dialect.KindTable:
		return d.tableDDL(ctx, q, schema, obj.Name)
	case dialect.KindSequence:
		return d.sequenceDDL(ctx, q, name)
	}
	var def sql.NullString
	if err := q.QueryRowContext(ctx, "SELECT OBJECT_DEFINITION(OBJECT_ID(@p1))", name).Scan(&def); err != nil {
		return "", err
	}
	if !def.Valid {
		return "", errors.New("the definition is not available: the object is encrypted, or the login lacks VIEW DEFINITION")
	}
	return strings.TrimSpace(def.String) + "\nGO\n", nil
}

func (d *Driver) tableDDL(ctx context.Context, q dialect.Querier, schema, table string) (string, error) {
	name := d.Qualify("", schema, table)
	//nolint:sqlclosecheck // the reader below closes the rows
	rows, err := q.QueryContext(ctx, `
SELECT c.name, `+typeExpr+`, c.is_nullable, COALESCE(dc.definition, ''), COALESCE(dc.name, ''),
       COALESCE(cc.definition, ''), COALESCE(cc.is_persisted, 0),
       COALESCE(CAST(idc.seed_value AS varchar(40)), ''), COALESCE(CAST(idc.increment_value AS varchar(40)), ''),
       CASE WHEN c.collation_name <> CAST(DATABASEPROPERTYEX(DB_NAME(), 'Collation') AS sysname) THEN c.collation_name ELSE '' END
FROM sys.columns c
JOIN sys.types t ON t.user_type_id = c.user_type_id
LEFT JOIN sys.default_constraints dc ON dc.object_id = c.default_object_id
LEFT JOIN sys.computed_columns cc ON cc.object_id = c.object_id AND cc.column_id = c.column_id
LEFT JOIN sys.identity_columns idc ON idc.object_id = c.object_id AND idc.column_id = c.column_id
WHERE c.object_id = OBJECT_ID(@p1)
ORDER BY c.column_id`, name)
	if err != nil {
		return "", err
	}
	var lines []string
	err = func() error {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				col, typ, def, defName, computed, seed, incr, collation string
				nullable, persisted                                     bool
			)
			if err := rows.Scan(&col, &typ, &nullable, &def, &defName, &computed, &persisted, &seed, &incr, &collation); err != nil {
				return err
			}
			l := "    " + d.Quote(col) + " "
			if computed != "" {
				l += "AS " + computed
				if persisted {
					l += " PERSISTED"
				}
				lines = append(lines, l)
				continue
			}
			l += typ
			if collation != "" {
				l += " COLLATE " + collation
			}
			if seed != "" {
				l += " IDENTITY(" + seed + ", " + incr + ")"
			}
			if nullable {
				l += " NULL"
			} else {
				l += " NOT NULL"
			}
			if def != "" {
				l += " CONSTRAINT " + d.Quote(defName) + " DEFAULT " + def
			}
			lines = append(lines, l)
		}
		return rows.Err()
	}()
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("table %s not found", name)
	}
	cons, err := d.Constraints(ctx, q, "", schema, table)
	if err != nil {
		return "", err
	}
	for _, c := range cons {
		lines = append(lines, "    CONSTRAINT "+d.Quote(c.Name)+" "+c.Definition)
	}
	var b strings.Builder
	b.WriteString("CREATE TABLE " + name + " (\n" + strings.Join(lines, ",\n") + "\n);\nGO\n")
	ixs, err := d.Indexes(ctx, q, "", schema, table)
	if err != nil {
		return "", err
	}
	for _, ix := range ixs {
		if ix.Constraint == "" && ix.Definition != "" {
			b.WriteString("\n" + ix.Definition + ";\nGO\n")
		}
	}
	return b.String(), nil
}

func (d *Driver) sequenceDDL(ctx context.Context, q dialect.Querier, name string) (string, error) {
	var typ, start, incr, minV, maxV string
	var cycle bool
	err := q.QueryRowContext(ctx, `
SELECT TYPE_NAME(s.user_type_id), CAST(s.start_value AS varchar(40)), CAST(s.increment AS varchar(40)),
       CAST(s.minimum_value AS varchar(40)), CAST(s.maximum_value AS varchar(40)), s.is_cycling
FROM sys.sequences s WHERE s.object_id = OBJECT_ID(@p1)`, name).Scan(&typ, &start, &incr, &minV, &maxV, &cycle)
	if err != nil {
		return "", err
	}
	s := fmt.Sprintf("CREATE SEQUENCE %s\n    AS %s\n    START WITH %s\n    INCREMENT BY %s\n    MINVALUE %s\n    MAXVALUE %s\n    ", name, typ, start, incr, minV, maxV)
	if cycle {
		s += "CYCLE"
	} else {
		s += "NO CYCLE"
	}
	return s + ";\nGO\n", nil
}

// stripParens removes the parentheses SQL Server puts around stored expressions:
// ((0)) becomes 0, ([a]>(10)) becomes [a]>(10).
func stripParens(s string) string {
	for {
		s = strings.TrimSpace(s)
		if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
			return s
		}
		depth := 0
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 && i != len(s)-1 {
					return s
				}
			case '\'':
				for i++; i < len(s) && s[i] != '\''; i++ {
				}
			}
		}
		s = s[1 : len(s)-1]
	}
}

// CreateIndex returns the CREATE INDEX statement for ix, or ALTER TABLE … ADD
// CONSTRAINT for a primary key.
func (d *Driver) CreateIndex(_, schema string, ix dialect.Index, _, _ bool) (string, error) {
	if len(ix.Columns) == 0 && ix.Method != "columnstore" {
		return "", errors.New("an index needs at least one column")
	}
	parts := make([]string, 0, len(ix.Columns))
	for _, c := range ix.Columns {
		if c.Name == "" {
			return "", errors.New("SQL Server indexes take columns, not expressions; index a computed column instead")
		}
		p := d.Quote(c.Name)
		if c.Desc {
			p += " DESC"
		}
		parts = append(parts, p)
	}
	table := d.Qualify("", schema, ix.Table)
	kind := "NONCLUSTERED"
	if ix.Clustered {
		kind = "CLUSTERED"
	}
	if ix.Primary {
		s := "ALTER TABLE " + table + " ADD "
		if ix.Name != "" {
			s += "CONSTRAINT " + d.Quote(ix.Name) + " "
		}
		return s + "PRIMARY KEY " + kind + " (" + strings.Join(parts, ", ") + ")", nil
	}
	if ix.Name == "" {
		return "", errors.New("SQL Server indexes need a name")
	}
	var b strings.Builder
	b.WriteString("CREATE ")
	if ix.Unique {
		b.WriteString("UNIQUE ")
	}
	b.WriteString(kind + " ")
	if ix.Method == "columnstore" {
		b.WriteString("COLUMNSTORE ")
	}
	b.WriteString("INDEX " + d.Quote(ix.Name) + " ON " + table)
	if len(parts) > 0 && (ix.Method != "columnstore" || !ix.Clustered) {
		b.WriteString(" (" + strings.Join(parts, ", ") + ")")
	}
	if len(ix.Include) > 0 {
		b.WriteString(" INCLUDE (" + d.quoteList(ix.Include) + ")")
	}
	if w := strings.TrimSpace(ix.Where); w != "" {
		b.WriteString(" WHERE " + w)
	}
	if ix.FillFactor > 0 {
		b.WriteString(" WITH (FILLFACTOR = " + strconv.Itoa(ix.FillFactor) + ")")
	}
	return b.String(), nil
}

// DropIndex returns the statement dropping an index (or the primary key constraint).
func (d *Driver) DropIndex(_, schema, table, name string, _ bool) string {
	return "DROP INDEX " + d.Quote(name) + " ON " + d.Qualify("", schema, table)
}

// DropObject drops a trigger by its schema-qualified name.
func (d *Driver) DropObject(_, schema string, obj dialect.Object) string {
	if obj.Kind == dialect.KindTrigger {
		return "DROP TRIGGER " + d.Qualify("", schema, obj.Name)
	}
	return ""
}

var functions = []string{
	"abs", "avg", "cast", "ceiling", "charindex", "choose", "coalesce", "concat", "concat_ws", "convert", "count",
	"count_big", "cume_dist", "datalength", "dateadd", "datediff", "datefromparts", "datename", "datepart", "datetrunc",
	"day", "dense_rank", "eomonth", "first_value", "floor", "format", "getdate", "getutcdate", "greatest", "iif",
	"isjson", "isnull", "json_modify", "json_query", "json_value", "lag", "last_value", "lead", "least", "left", "len",
	"lower", "ltrim", "max", "min", "month", "newid", "ntile", "nullif", "object_id", "openjson", "patindex",
	"percent_rank", "quotename", "rank", "replace", "replicate", "reverse", "right", "round", "row_number", "rtrim",
	"scope_identity", "string_agg", "string_split", "stuff", "substring", "sum", "switchoffset", "sysdatetime",
	"sysdatetimeoffset", "translate", "trim", "try_cast", "try_convert", "upper", "year",
}

var snippets = []dialect.Snippet{
	{Label: "sel", Detail: "SELECT TOP … FROM … WHERE", Body: "SELECT TOP (100) ${*}\nFROM ${table}\nWHERE ${condition};"},
	{Label: "ins", Detail: "INSERT INTO … VALUES", Body: "INSERT INTO ${table} (${columns})\nVALUES (${values});"},
	{Label: "upd", Detail: "UPDATE … SET … WHERE", Body: "UPDATE ${table}\nSET ${column} = ${value}\nWHERE ${condition};"},
	{Label: "del", Detail: "DELETE FROM … WHERE", Body: "DELETE FROM ${table}\nWHERE ${condition};"},
	{Label: "cte", Detail: "WITH … AS (…) SELECT", Body: "WITH ${name} AS (\n    SELECT ${*}\n    FROM ${table}\n)\nSELECT *\nFROM ${name};"},
	{Label: "decl", Detail: "DECLARE @variable", Body: "DECLARE @${name} ${int} = ${0};"},
	{Label: "ct", Detail: "CREATE TABLE", Body: "CREATE TABLE ${name} (\n    id bigint IDENTITY(1, 1) NOT NULL PRIMARY KEY,\n    ${column} nvarchar(255) NOT NULL,\n    created_at datetime2 NOT NULL DEFAULT SYSUTCDATETIME()\n);"},
	{Label: "proc", Detail: "CREATE PROCEDURE", Body: "CREATE OR ALTER PROCEDURE ${name}\n    @${id} int\nAS\nBEGIN\n    SET NOCOUNT ON;\n    ${SELECT 1;}\nEND\nGO"},
	{Label: "tx", Detail: "BEGIN TRAN … COMMIT", Body: "BEGIN TRANSACTION;\n${}\nCOMMIT;"},
}

// SchemaColumns lists the columns of every table and view of a schema.
func (d *Driver) SchemaColumns(ctx context.Context, q dialect.Querier, _, schema string) (map[string][]string, error) {
	return dialect.ColumnPairs(ctx, q, `SELECT o.name, c.name FROM sys.columns c
JOIN sys.objects o ON o.object_id = c.object_id JOIN sys.schemas s ON s.schema_id = o.schema_id
WHERE s.name = @p1 AND o.type IN ('U', 'V') ORDER BY o.name, c.column_id`, schema)
}

// ForeignKeys lists the foreign keys of a schema.
func (d *Driver) ForeignKeys(ctx context.Context, q dialect.Querier, _, schema string) ([]dialect.ForeignKey, error) {
	return dialect.KeyRows(ctx, q, `SELECT fk.name, t.name, c.name, rs.name, rt.name, rc.name
FROM sys.foreign_keys fk
JOIN sys.tables t ON t.object_id = fk.parent_object_id JOIN sys.schemas s ON s.schema_id = t.schema_id
JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
JOIN sys.columns c ON c.object_id = fkc.parent_object_id AND c.column_id = fkc.parent_column_id
JOIN sys.tables rt ON rt.object_id = fk.referenced_object_id JOIN sys.schemas rs ON rs.schema_id = rt.schema_id
JOIN sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
WHERE s.name = @p1 ORDER BY t.name, fk.name, fkc.constraint_column_id`, schema)
}

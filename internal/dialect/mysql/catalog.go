package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/iagapie/quire/internal/dialect"
)

// systemDatabases are listed last (metrics_schema is TiDB's).
var systemDatabases = map[string]bool{"information_schema": true, "mysql": true, "performance_schema": true, "sys": true, "metrics_schema": true}

// Databases lists the databases, user databases first.
func (d *Driver) Databases(ctx context.Context, q dialect.Querier) ([]string, error) {
	all, err := dialect.Strings(ctx, q, `SELECT SCHEMA_NAME FROM information_schema.SCHEMATA ORDER BY SCHEMA_NAME`)
	if err != nil {
		return nil, err
	}
	var user, system []string
	for _, db := range all {
		if systemDatabases[strings.ToLower(db)] {
			system = append(system, db)
		} else {
			user = append(user, db)
		}
	}
	return append(user, system...), nil
}

// Schemas returns nothing: MySQL has databases only.
func (d *Driver) Schemas(context.Context, dialect.Querier, string) ([]string, error) { return nil, nil }

// Objects lists the tables, views, routines and triggers of a database. The catalog
// views are read one by one: their collations differ, so a UNION may fail.
func (d *Driver) Objects(ctx context.Context, q dialect.Querier, database, _ string) ([]dialect.Object, error) {
	var out []dialect.Object
	read := func(query string) error {
		rows, err := q.QueryContext(ctx, query, database)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var o dialect.Object
			var comment, table sql.NullString
			if err := rows.Scan(&o.Name, &o.Kind, &comment, &table); err != nil {
				return err
			}
			o.Comment, o.Table, o.Detail = comment.String, table.String, table.String
			if o.Kind == dialect.KindView {
				o.Comment = "" // always "VIEW"
			}
			out = append(out, o)
		}
		return rows.Err()
	}
	if err := read(`SELECT TABLE_NAME, CASE WHEN TABLE_TYPE LIKE '%VIEW' THEN 'view' ELSE 'table' END, TABLE_COMMENT, NULL
FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`); err != nil {
		return nil, err
	}
	if err := read(`SELECT ROUTINE_NAME, LOWER(ROUTINE_TYPE), ROUTINE_COMMENT, NULL
FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ? AND ROUTINE_TYPE IN ('FUNCTION', 'PROCEDURE') ORDER BY ROUTINE_NAME`); err != nil {
		return nil, err
	}
	if err := read(`SELECT TRIGGER_NAME, 'trigger', NULL, EVENT_OBJECT_TABLE
FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ? ORDER BY TRIGGER_NAME`); err != nil {
		return nil, err
	}
	return out, nil
}

var generatedRe = regexp.MustCompile(`(?i)(VIRTUAL|STORED|PERSISTENT) GENERATED`)

// Columns lists the columns of a table or view.
func (d *Driver) Columns(ctx context.Context, q dialect.Querier, database, _, table string) ([]dialect.Column, error) {
	rows, err := q.QueryContext(ctx, `
SELECT c.COLUMN_NAME, c.COLUMN_TYPE, c.IS_NULLABLE, c.COLUMN_DEFAULT, COALESCE(k.ORDINAL_POSITION, 0), c.COLUMN_COMMENT, c.EXTRA
FROM information_schema.COLUMNS c
LEFT JOIN information_schema.KEY_COLUMN_USAGE k
       ON k.TABLE_SCHEMA = c.TABLE_SCHEMA AND k.TABLE_NAME = c.TABLE_NAME AND k.COLUMN_NAME = c.COLUMN_NAME AND k.CONSTRAINT_NAME = 'PRIMARY'
WHERE c.TABLE_SCHEMA = ? AND c.TABLE_NAME = ?
ORDER BY c.ORDINAL_POSITION`, database, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Column
	for rows.Next() {
		var (
			c               dialect.Column
			nullable, extra string
			def, comment    sql.NullString
		)
		if err := rows.Scan(&c.Name, &c.Type, &nullable, &def, &c.PK, &comment, &extra); err != nil {
			return nil, err
		}
		c.Nullable, c.Default, c.Comment = nullable == "YES", def.String, comment.String
		c.Generated = generatedRe.MatchString(extra)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Indexes lists the indexes of a table with their key parts. The STATISTICS columns
// differ between MySQL and MariaDB versions, so they are read by name.
func (d *Driver) Indexes(ctx context.Context, q dialect.Querier, database, _, table string) ([]dialect.Index, error) {
	recs, err := dialect.Records(ctx, q, `SELECT * FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
ORDER BY INDEX_NAME <> 'PRIMARY', INDEX_NAME, SEQ_IN_INDEX`, database, table)
	if err != nil {
		return nil, err
	}
	var out []dialect.Index
	for _, r := range recs {
		name := r["index_name"]
		if len(out) == 0 || out[len(out)-1].Name != name {
			ix := dialect.Index{
				Name: name, Table: table, Unique: r["non_unique"] == "0", Primary: name == "PRIMARY",
				Method: strings.ToLower(r["index_type"]), SizeBytes: -1,
				Invisible: r["is_visible"] == "NO" || r["ignored"] == "YES",
			}
			if ix.Unique {
				ix.Constraint = name
			}
			out = append(out, ix)
		}
		cur := &out[len(out)-1]
		col := dialect.IndexColumn{Name: r["column_name"], Desc: r["collation"] == "D"}
		// TiDB reports the column of an expression key as the text NULL.
		if col.Name == "" || col.Name == "NULL" && r["expression"] != "" {
			col.Name, col.Expression = "", r["expression"]
		}
		if n, err := strconv.Atoi(r["sub_part"]); err == nil {
			col.Length = n
		}
		cur.Columns = append(cur.Columns, col)
	}
	for i := range out {
		if def, err := d.CreateIndex(database, "", out[i], false, false); err == nil {
			out[i].Definition = def
		}
	}
	return out, nil
}

// Constraints lists primary keys, unique, foreign key and check constraints.
func (d *Driver) Constraints(ctx context.Context, q dialect.Querier, database, _, table string) ([]dialect.Constraint, error) {
	rows, err := q.QueryContext(ctx, `
SELECT tc.CONSTRAINT_NAME, tc.CONSTRAINT_TYPE, k.COLUMN_NAME, k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME,
       k.REFERENCED_COLUMN_NAME, rc.UPDATE_RULE, rc.DELETE_RULE
FROM information_schema.TABLE_CONSTRAINTS tc
LEFT JOIN information_schema.KEY_COLUMN_USAGE k
       ON k.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND k.TABLE_NAME = tc.TABLE_NAME AND k.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
LEFT JOIN information_schema.REFERENTIAL_CONSTRAINTS rc
       ON rc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND rc.TABLE_NAME = tc.TABLE_NAME AND rc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
WHERE tc.TABLE_SCHEMA = ? AND tc.TABLE_NAME = ?
ORDER BY FIELD(tc.CONSTRAINT_TYPE, 'PRIMARY KEY', 'UNIQUE', 'FOREIGN KEY', 'CHECK'), tc.CONSTRAINT_NAME, k.ORDINAL_POSITION`, database, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Constraint
	for rows.Next() {
		var name, typ string
		var col, refSchema, refTable, refCol, upd, del sql.NullString
		if err := rows.Scan(&name, &typ, &col, &refSchema, &refTable, &refCol, &upd, &del); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Name != name || out[len(out)-1].Type != typ {
			c := dialect.Constraint{Name: name, Type: typ, RefTable: refTable.String, OnUpdate: upd.String, OnDelete: del.String}
			if refSchema.String != database {
				c.RefSchema = refSchema.String
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
	checks := d.checks(ctx, q, database, table)
	for i := range out {
		c := &out[i]
		switch c.Type {
		case "PRIMARY KEY", "UNIQUE":
			c.Definition = c.Type + " (" + d.quoteList(c.Columns) + ")"
		case "FOREIGN KEY":
			c.Definition = "FOREIGN KEY (" + d.quoteList(c.Columns) + ") REFERENCES " + d.Qualify(c.RefSchema, "", c.RefTable) +
				" (" + d.quoteList(c.RefColumns) + ")"
			if c.OnDelete != "" && c.OnDelete != "RESTRICT" && c.OnDelete != "NO ACTION" {
				c.Definition += " ON DELETE " + c.OnDelete
			}
			if c.OnUpdate != "" && c.OnUpdate != "RESTRICT" && c.OnUpdate != "NO ACTION" {
				c.Definition += " ON UPDATE " + c.OnUpdate
			}
		case "CHECK":
			if clause, ok := checks[c.Name]; ok {
				c.Definition = "CHECK (" + clause + ")"
			}
		}
	}
	return out, nil
}

// checks reads the CHECK clauses (MySQL 8.0.16+, MariaDB 10.2+); older servers have none.
func (d *Driver) checks(ctx context.Context, q dialect.Querier, database, table string) map[string]string {
	out := map[string]string{}
	query := `SELECT cc.CONSTRAINT_NAME, cc.CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS cc
JOIN information_schema.TABLE_CONSTRAINTS tc ON tc.CONSTRAINT_SCHEMA = cc.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME = cc.CONSTRAINT_NAME
WHERE cc.CONSTRAINT_SCHEMA = ? AND tc.TABLE_NAME = ? AND tc.CONSTRAINT_TYPE = 'CHECK'`
	rows, err := q.QueryContext(ctx, query, database, table)
	if err != nil {
		return out
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name, clause string
		if rows.Scan(&name, &clause) == nil {
			out[name] = clause
		}
	}
	_ = rows.Err() // a partial list only loses CHECK texts
	return out
}

func (d *Driver) quoteList(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = d.Quote(c)
	}
	return strings.Join(q, ", ")
}

// TableStats estimates rows and sizes from the table statistics.
func (d *Driver) TableStats(ctx context.Context, q dialect.Querier, database, _ string) ([]dialect.TableStat, error) {
	rows, err := q.QueryContext(ctx, `
SELECT TABLE_NAME, CASE WHEN TABLE_TYPE LIKE '%VIEW' THEN 'view' ELSE 'table' END,
       COALESCE(TABLE_ROWS, -1), COALESCE(DATA_LENGTH + INDEX_LENGTH, -1)
FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`, database)
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
		if t.Kind == dialect.KindView {
			t.Rows, t.Bytes = -1, -1
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DDL returns the definition of an object, as the server shows it. Routines and
// triggers are wrapped in DELIMITER so the script runs as it is.
func (d *Driver) DDL(ctx context.Context, q dialect.Querier, database, _ string, obj dialect.Object) (string, error) {
	name := d.Qualify(database, "", obj.Name)
	show := map[string][2]string{
		dialect.KindTable:     {"TABLE", "create table"},
		dialect.KindView:      {"VIEW", "create view"},
		dialect.KindFunction:  {"FUNCTION", "create function"},
		dialect.KindProcedure: {"PROCEDURE", "create procedure"},
		dialect.KindTrigger:   {"TRIGGER", "sql original statement"},
	}[obj.Kind]
	if show[0] == "" {
		return "", dialect.ErrUnsupported
	}
	recs, err := dialect.Records(ctx, q, "SHOW CREATE "+show[0]+" "+name)
	if err != nil {
		return "", err
	}
	if len(recs) == 0 {
		return "", fmt.Errorf("%s not found", name)
	}
	def := recs[0][show[1]]
	if def == "" {
		return "", errors.New("the server did not return the definition: the user may lack the privileges to see it")
	}
	switch obj.Kind {
	case dialect.KindTable, dialect.KindView:
		return def + ";\n", nil
	}
	return "DELIMITER ;;\n" + def + ";;\nDELIMITER ;\n", nil
}

var functions = []string{
	"abs", "avg", "bin_to_uuid", "cast", "ceil", "char_length", "coalesce", "concat", "concat_ws", "convert", "count",
	"curdate", "current_timestamp", "curtime", "date", "date_add", "date_format", "date_sub", "datediff", "day",
	"dense_rank", "first_value", "floor", "format", "from_unixtime", "group_concat", "greatest", "hex", "if", "ifnull",
	"instr", "json_arrayagg", "json_contains", "json_extract", "json_object", "json_objectagg", "json_set",
	"json_table", "json_unquote", "lag", "last_insert_id", "lead", "least", "left", "length", "locate", "lower", "lpad",
	"ltrim", "max", "md5", "min", "month", "now", "nullif", "rank", "regexp_like", "regexp_replace", "replace",
	"right", "round", "row_number", "rpad", "rtrim", "sha2", "str_to_date", "substring", "substring_index", "sum",
	"sysdate", "timestampdiff", "trim", "truncate", "unix_timestamp", "upper", "utc_timestamp", "uuid", "uuid_to_bin",
	"year",
}

var snippets = []dialect.Snippet{
	{Label: "sel", Detail: "SELECT … FROM … WHERE", Body: "SELECT ${*}\nFROM ${table}\nWHERE ${condition}\nLIMIT 100;"},
	{Label: "ins", Detail: "INSERT INTO … VALUES", Body: "INSERT INTO ${table} (${columns})\nVALUES (${values});"},
	{Label: "upd", Detail: "UPDATE … SET … WHERE", Body: "UPDATE ${table}\nSET ${column} = ${value}\nWHERE ${condition};"},
	{Label: "del", Detail: "DELETE FROM … WHERE", Body: "DELETE FROM ${table}\nWHERE ${condition};"},
	{Label: "cte", Detail: "WITH … AS (…) SELECT", Body: "WITH ${name} AS (\n    SELECT ${*}\n    FROM ${table}\n)\nSELECT *\nFROM ${name};"},
	{Label: "upsert", Detail: "INSERT … ON DUPLICATE KEY UPDATE", Body: "INSERT INTO ${table} (${id}, ${column})\nVALUES (${1}, ${value})\nON DUPLICATE KEY UPDATE ${column} = VALUES(${column});"},
	{Label: "ct", Detail: "CREATE TABLE", Body: "CREATE TABLE ${name} (\n    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,\n    ${column} VARCHAR(255) NOT NULL,\n    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP\n);"},
	{Label: "proc", Detail: "CREATE PROCEDURE", Body: "DELIMITER ;;\nCREATE PROCEDURE ${name}(${IN id INT})\nBEGIN\n    ${SELECT 1;}\nEND;;\nDELIMITER ;"},
	{Label: "tx", Detail: "START TRANSACTION … COMMIT", Body: "START TRANSACTION;\n${}\nCOMMIT;"},
}

// SchemaColumns lists the columns of every table and view of a database.
func (d *Driver) SchemaColumns(ctx context.Context, q dialect.Querier, database, _ string) (map[string][]string, error) {
	return dialect.ColumnPairs(ctx, q, `SELECT TABLE_NAME, COLUMN_NAME FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME, ORDINAL_POSITION`, database)
}

// ForeignKeys lists the foreign keys of a database.
func (d *Driver) ForeignKeys(ctx context.Context, q dialect.Querier, database, _ string) ([]dialect.ForeignKey, error) {
	return dialect.KeyRows(ctx, q, `SELECT CONSTRAINT_NAME, TABLE_NAME, COLUMN_NAME, REFERENCED_TABLE_SCHEMA, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME
FROM information_schema.KEY_COLUMN_USAGE
WHERE TABLE_SCHEMA = ? AND REFERENCED_TABLE_NAME IS NOT NULL
ORDER BY TABLE_NAME, CONSTRAINT_NAME, ORDINAL_POSITION`, database)
}

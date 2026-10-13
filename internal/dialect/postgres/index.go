package postgres

import (
	"errors"
	"strconv"
	"strings"

	"github.com/iagapie/quire/internal/dialect"
)

// CreateIndex returns the CREATE INDEX statement for ix.
func (d *Driver) CreateIndex(_, schema string, ix dialect.Index, concurrently, ifNotExists bool) (string, error) {
	if len(ix.Columns) == 0 {
		return "", errors.New("an index needs at least one column")
	}
	var b strings.Builder
	b.WriteString("CREATE ")
	if ix.Unique {
		b.WriteString("UNIQUE ")
	}
	b.WriteString("INDEX ")
	if concurrently {
		b.WriteString("CONCURRENTLY ")
	}
	if ifNotExists && ix.Name != "" {
		b.WriteString("IF NOT EXISTS ")
	}
	if ix.Name != "" {
		b.WriteString(d.Quote(ix.Name) + " ")
	}
	b.WriteString("ON " + d.Qualify("", schema, ix.Table))
	if ix.Method != "" && ix.Method != "btree" {
		b.WriteString(" USING " + ix.Method)
	}
	parts := make([]string, 0, len(ix.Columns))
	for _, c := range ix.Columns {
		var p string
		switch {
		case strings.TrimSpace(c.Expression) != "":
			p = "(" + strings.TrimSpace(c.Expression) + ")"
		case c.Name != "":
			p = d.Quote(c.Name)
		default:
			return "", errors.New("every index key needs a column or an expression")
		}
		if c.Collation != "" {
			p += " COLLATE " + d.Quote(c.Collation)
		}
		if c.OpClass != "" {
			p += " " + c.OpClass
		}
		if c.Desc {
			p += " DESC"
		}
		switch c.Nulls {
		case "first":
			p += " NULLS FIRST"
		case "last":
			p += " NULLS LAST"
		}
		parts = append(parts, p)
	}
	b.WriteString(" (" + strings.Join(parts, ", ") + ")")
	if len(ix.Include) > 0 {
		q := make([]string, len(ix.Include))
		for i, c := range ix.Include {
			q[i] = d.Quote(c)
		}
		b.WriteString(" INCLUDE (" + strings.Join(q, ", ") + ")")
	}
	if ix.FillFactor > 0 {
		b.WriteString(" WITH (fillfactor = " + strconv.Itoa(ix.FillFactor) + ")")
	}
	if ix.Tablespace != "" {
		b.WriteString(" TABLESPACE " + d.Quote(ix.Tablespace))
	}
	if w := strings.TrimSpace(ix.Where); w != "" {
		b.WriteString(" WHERE " + w)
	}
	return b.String(), nil
}

// DropIndex returns the DROP INDEX statement.
func (d *Driver) DropIndex(_, schema, _, name string, concurrently bool) string {
	s := "DROP INDEX "
	if concurrently {
		s += "CONCURRENTLY "
	}
	return s + d.Qualify("", schema, name)
}

var functions = []string{
	"abs", "age", "array_agg", "array_length", "array_position", "array_to_string", "avg", "bool_and", "bool_or",
	"btrim", "cardinality", "ceil", "char_length", "clock_timestamp", "coalesce", "concat", "concat_ws", "count",
	"current_date", "current_setting", "current_timestamp", "current_user", "date_part", "date_trunc", "dense_rank",
	"encode", "decode", "extract", "first_value", "floor", "format", "gen_random_uuid", "generate_series",
	"greatest", "jsonb_agg", "jsonb_build_object", "jsonb_each", "jsonb_object_agg", "jsonb_path_query",
	"jsonb_set", "json_agg", "json_build_object", "lag", "last_value", "lead", "least", "left", "length", "lower",
	"lpad", "ltrim", "make_date", "make_interval", "max", "md5", "min", "now", "nullif", "ntile", "pg_size_pretty",
	"pg_total_relation_size", "position", "random", "rank", "regexp_match", "regexp_matches", "regexp_replace",
	"regexp_split_to_table", "repeat", "replace", "reverse", "right", "round", "row_number", "row_to_json", "rpad",
	"rtrim", "split_part", "sqrt", "string_agg", "strpos", "substr", "substring", "sum", "to_char", "to_date",
	"to_json", "to_jsonb", "to_number", "to_timestamp", "translate", "trim", "trunc", "unnest", "upper",
}

var snippets = []dialect.Snippet{
	{Label: "sel", Detail: "SELECT … FROM … WHERE", Body: "SELECT ${*}\nFROM ${table}\nWHERE ${condition}\nLIMIT 100;"},
	{Label: "ins", Detail: "INSERT INTO … VALUES", Body: "INSERT INTO ${table} (${columns})\nVALUES (${values});"},
	{Label: "upd", Detail: "UPDATE … SET … WHERE", Body: "UPDATE ${table}\nSET ${column} = ${value}\nWHERE ${condition};"},
	{Label: "del", Detail: "DELETE FROM … WHERE", Body: "DELETE FROM ${table}\nWHERE ${condition};"},
	{Label: "cte", Detail: "WITH … AS (…) SELECT", Body: "WITH ${name} AS (\n    SELECT ${*}\n    FROM ${table}\n)\nSELECT *\nFROM ${name};"},
	{Label: "upsert", Detail: "INSERT … ON CONFLICT", Body: "INSERT INTO ${table} (${id}, ${column})\nVALUES (${1}, ${value})\nON CONFLICT (${id}) DO UPDATE SET ${column} = EXCLUDED.${column};"},
	{Label: "ct", Detail: "CREATE TABLE", Body: "CREATE TABLE ${name} (\n    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,\n    ${column} text NOT NULL,\n    created_at timestamptz NOT NULL DEFAULT now()\n);"},
	{Label: "fn", Detail: "CREATE FUNCTION", Body: "CREATE OR REPLACE FUNCTION ${name}(${args})\nRETURNS ${int} LANGUAGE sql AS $$\n    ${SELECT 1}\n$$;"},
	{Label: "tx", Detail: "BEGIN … COMMIT", Body: "BEGIN;\n${}\nCOMMIT;"},
}

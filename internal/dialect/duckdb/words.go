package duckdb

import "github.com/iagapie/quire/internal/dialect"

var functions = []string{
	"abs", "any_value", "approx_count_distinct", "arg_max", "arg_min", "array_agg", "avg", "coalesce", "concat",
	"concat_ws", "count", "current_date", "date_diff", "date_part", "date_trunc", "epoch_ms", "first", "generate_series",
	"greatest", "histogram", "json_extract", "json_extract_string", "last", "least", "len", "length", "list_aggregate",
	"list_contains", "list_distinct", "list_filter", "list_transform", "lower", "max", "max_by", "median", "min",
	"min_by", "mode", "now", "nullif", "quantile_cont", "range", "read_csv", "read_json", "read_parquet", "regexp_extract",
	"regexp_matches", "regexp_replace", "replace", "round", "row_number", "split_part", "starts_with", "strftime",
	"string_agg", "string_split", "strptime", "struct_pack", "substring", "sum", "today", "trim", "unnest", "upper",
}

var snippets = []dialect.Snippet{
	{Label: "sel", Detail: "SELECT … FROM … WHERE", Body: "SELECT ${*}\nFROM ${table}\nWHERE ${condition}\nLIMIT 100;"},
	{Label: "ins", Detail: "INSERT INTO … VALUES", Body: "INSERT INTO ${table} (${columns})\nVALUES (${values});"},
	{Label: "upd", Detail: "UPDATE … SET … WHERE", Body: "UPDATE ${table}\nSET ${column} = ${value}\nWHERE ${condition};"},
	{Label: "del", Detail: "DELETE FROM … WHERE", Body: "DELETE FROM ${table}\nWHERE ${condition};"},
	{Label: "csv", Detail: "SELECT … FROM read_csv", Body: "SELECT *\nFROM read_csv('${path}.csv')\nLIMIT 100;"},
	{Label: "parquet", Detail: "SELECT … FROM read_parquet", Body: "SELECT *\nFROM read_parquet('${path}/*.parquet')\nLIMIT 100;"},
	{Label: "ctas", Detail: "CREATE TABLE … AS SELECT", Body: "CREATE TABLE ${name} AS\nSELECT *\nFROM ${source};"},
	{Label: "upsert", Detail: "INSERT … ON CONFLICT", Body: "INSERT INTO ${table} (${id}, ${column})\nVALUES (${1}, ${value})\nON CONFLICT (${id}) DO UPDATE SET ${column} = EXCLUDED.${column};"},
	{Label: "copy", Detail: "COPY … TO parquet", Body: "COPY ${table} TO '${path}.parquet' (FORMAT parquet);"},
}

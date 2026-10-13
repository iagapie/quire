package clickhouse

import "github.com/iagapie/quire/internal/dialect"

var functions = []string{
	"any", "anyLast", "argMax", "argMin", "arrayJoin", "arrayMap", "arrayFilter", "avg", "count", "countIf", "dateDiff",
	"empty", "formatDateTime", "groupArray", "groupUniqArray", "has", "if", "ifNull", "isNull", "JSONExtract",
	"JSONExtractString", "length", "lower", "max", "maxIf", "median", "min", "minIf", "multiIf", "now", "now64",
	"parseDateTimeBestEffort", "quantile", "quantiles", "replaceAll", "round", "splitByChar", "substring", "sum", "sumIf",
	"toDate", "toDateTime", "toDateTime64", "toFloat64", "toInt32", "toInt64", "toStartOfDay", "toStartOfHour",
	"toStartOfMonth", "toStartOfWeek", "toString", "toUInt32", "toUInt64", "toYYYYMM", "today", "topK", "uniq",
	"uniqExact", "upper", "yesterday",
}

var snippets = []dialect.Snippet{
	{Label: "sel", Detail: "SELECT … FROM … WHERE", Body: "SELECT ${*}\nFROM ${table}\nWHERE ${condition}\nLIMIT 100;"},
	{Label: "ins", Detail: "INSERT INTO … VALUES", Body: "INSERT INTO ${table} (${columns})\nVALUES (${values});"},
	{Label: "ct", Detail: "CREATE TABLE … MergeTree", Body: "CREATE TABLE ${name}\n(\n    id UInt64,\n    ${column} String,\n    created_at DateTime DEFAULT now()\n)\nENGINE = MergeTree\nORDER BY id;"},
	{Label: "mv", Detail: "CREATE MATERIALIZED VIEW", Body: "CREATE MATERIALIZED VIEW ${name}\nENGINE = SummingMergeTree\nORDER BY ${key}\nAS SELECT ${key}, count() AS n\nFROM ${table}\nGROUP BY ${key};"},
	{Label: "del", Detail: "DELETE FROM … WHERE", Body: "DELETE FROM ${table}\nWHERE ${condition};"},
	{Label: "upd", Detail: "ALTER TABLE … UPDATE", Body: "ALTER TABLE ${table}\nUPDATE ${column} = ${value}\nWHERE ${condition};"},
}

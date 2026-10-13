package query

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Column kinds, which tell the UI how to show and edit values.
const (
	KindText     = "text"
	KindNumber   = "number"
	KindBool     = "bool"
	KindBinary   = "binary"
	KindDateTime = "datetime"
	KindJSON     = "json"
)

// Column describes a result column.
type Column struct {
	Name string `json:"name"`
	// Type is the database type name (e.g. INT4, VARCHAR).
	Type string `json:"type"`
	Kind string `json:"kind"`
}

// maxCellBytes caps how much of a binary or text value is sent to the UI.
const maxCellBytes = 256 << 10

// ValueFormatter lets a driver render its own value types (optional).
type ValueFormatter interface {
	FormatValue(dbType string, v any) (string, bool)
}

// baseType removes the ClickHouse wrappers that do not change how a value reads:
// Nullable(T), LowCardinality(T).
func baseType(dbType string) string {
	t := strings.ToUpper(strings.TrimSpace(dbType))
	for {
		inner, ok := unwrap(t, "NULLABLE(")
		if !ok {
			inner, ok = unwrap(t, "LOWCARDINALITY(")
		}
		if !ok {
			return t
		}
		t = inner
	}
}

func unwrap(t, prefix string) (string, bool) {
	if strings.HasPrefix(t, prefix) && strings.HasSuffix(t, ")") {
		return t[len(prefix) : len(t)-1], true
	}
	return t, false
}

func columnKind(dbType string) string {
	t := baseType(dbType)
	switch {
	case t == "":
		return KindText
	case strings.HasPrefix(t, "ARRAY(") || strings.HasPrefix(t, "MAP(") || strings.HasPrefix(t, "TUPLE(") || strings.HasPrefix(t, "NESTED(") ||
		strings.HasPrefix(t, "STRUCT(") || strings.HasPrefix(t, "UNION(") || strings.HasSuffix(t, "]"):
		return KindJSON // ClickHouse and DuckDB nested types (DuckDB lists are T[] or T[n])
	case strings.Contains(t, "BOOL") || t == "BIT":
		return KindBool
	case t == "JSON" || t == "JSONB":
		return KindJSON
	case strings.Contains(t, "BLOB") || strings.Contains(t, "BINARY") || t == "BYTEA" || t == "RAW" || strings.HasPrefix(t, "RAW(") || t == "LONG RAW" || t == "IMAGE" || t == "GEOMETRY":
		return KindBinary
	case strings.HasPrefix(t, "INTERVAL"):
		return KindText
	case strings.Contains(t, "INT") || strings.Contains(t, "NUMERIC") || strings.Contains(t, "DECIMAL") || strings.Contains(t, "FLOAT") ||
		strings.Contains(t, "DOUBLE") || t == "REAL" || t == "NUMBER" || t == "MONEY" || t == "SMALLMONEY" || strings.HasPrefix(t, "UINT") ||
		t == "OID" || t == "SERIAL" || t == "BIGSERIAL" || t == "BINARY_FLOAT" || t == "BINARY_DOUBLE":
		return KindNumber
	case strings.Contains(t, "DATE") || strings.Contains(t, "TIME"):
		if strings.Contains(t, "INTERVAL") {
			return KindText
		}
		return KindDateTime
	}
	return KindText
}

func columns(cts []*sql.ColumnType) []Column {
	out := make([]Column, len(cts))
	seen := map[string]int{}
	for i, ct := range cts {
		name := ct.Name()
		if name == "" {
			name = "?column?"
		}
		out[i] = Column{Name: name, Type: strings.ToUpper(ct.DatabaseTypeName()), Kind: columnKind(ct.DatabaseTypeName())}
		seen[name]++
	}
	return out
}

// formatValue renders a scanned value as text for the grid; nil means SQL NULL. Long
// values are cut.
func formatValue(col Column, v any, f ValueFormatter) *string {
	return renderValue(col, v, f, maxCellBytes)
}

// renderValue renders a value, cutting text and binary values longer than limit bytes
// (0: never).
var bigIntType = reflect.TypeOf((*big.Int)(nil))

func renderValue(col Column, v any, f ValueFormatter, limit int) *string {
	if v == nil {
		return nil
	}
	// Nullable values may come as pointers (ClickHouse).
	for rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer && rv.Type() != bigIntType; rv = reflect.ValueOf(v) {
		if rv.IsNil() {
			return nil
		}
		v = rv.Elem().Interface()
	}
	if f != nil {
		if s, ok := f.FormatValue(col.Type, v); ok {
			return &s
		}
	}
	var s string
	switch x := v.(type) {
	case []byte:
		s = formatBytes(col, x, limit)
	case string:
		s = x
		if limit > 0 && len(s) > limit {
			s = truncate(s, limit)
		}
	case time.Time:
		s = formatTime(col.Type, x)
	case float64:
		s = strconv.FormatFloat(x, 'g', -1, 64)
	case float32:
		s = strconv.FormatFloat(float64(x), 'g', -1, 32)
	case int64:
		s = strconv.FormatInt(x, 10)
	case bool:
		s = strconv.FormatBool(x)
	case *big.Int:
		s = x.String()
	case fmt.Stringer:
		s = x.String()
	default:
		s = fmt.Sprint(x)
	}
	return &s
}

func formatBytes(col Column, b []byte, limit int) string {
	if col.Kind != KindBinary && utf8.Valid(b) {
		s := string(b)
		if limit > 0 && len(s) > limit {
			s = truncate(s, limit)
		}
		return s
	}
	if limit > 0 && len(b) > limit/2 {
		return `\x` + hex.EncodeToString(b[:limit/2]) + fmt.Sprintf("… (%d bytes)", len(b))
	}
	return `\x` + hex.EncodeToString(b)
}

func truncate(s string, limit int) string {
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("… (%d bytes)", len(s))
}

func formatTime(dbType string, t time.Time) string {
	u := baseType(dbType)
	switch {
	case (u == "DATE" || u == "DATE32") && t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0:
		// An Oracle DATE has a time of day too: shown when there is one.
		return t.Format("2006-01-02")
	case u == "TIME" || u == "TIME WITHOUT TIME ZONE":
		return t.Format("15:04:05.999999")
	case strings.Contains(u, "TZ") || strings.Contains(u, "OFFSET") || strings.Contains(u, "WITH TIME ZONE") || strings.Contains(u, "LOCAL"):
		return t.Format("2006-01-02 15:04:05.999999Z07:00")
	}
	return t.Format("2006-01-02 15:04:05.999999")
}

// scanRow reads the current row of rows.
func scanRow(rows *sql.Rows, cols []Column, f ValueFormatter) ([]*string, error) {
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	out := make([]*string, len(cols))
	for i, v := range vals {
		out[i] = formatValue(cols[i], v, f)
	}
	return out, nil
}

// rowsSize estimates the memory held by cached rows.
func rowsSize(rows [][]*string) int {
	n := 0
	for _, r := range rows {
		n += 24 * len(r)
		for _, c := range r {
			if c != nil {
				n += len(*c)
			}
		}
	}
	return n
}

// Columns describes the columns of rows.
func Columns(rows *sql.Rows) ([]Column, error) {
	cts, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	return columns(cts), nil
}

// FullRow reads the current row of rows with values in full, as text (nil is NULL),
// for exports. drv may format its own value types (see ValueFormatter).
func FullRow(rows *sql.Rows, cols []Column, drv any) ([]*string, error) {
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	f, _ := drv.(ValueFormatter)
	out := make([]*string, len(cols))
	for i, v := range vals {
		out[i] = renderValue(cols[i], v, f, 0)
	}
	return out, nil
}

// KindOf returns the value kind of a database type (number, bool, json, binary,
// datetime or text).
func KindOf(dbType string) string { return columnKind(dbType) }

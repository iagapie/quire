package dialect

import (
	"context"
	"database/sql"
	"encoding/hex"
	"io"
	"strconv"
	"strings"
)

// Value kinds, as the query engine names them.
const (
	ValueNumber   = "number"
	ValueBool     = "bool"
	ValueBinary   = "binary"
	ValueDateTime = "datetime"
)

// Literals is implemented by drivers that write some literals their own way (binary
// values, booleans, string escapes). ok is false to use the standard form.
type Literals interface {
	Literal(kind, text string) (lit string, ok bool)
}

// Literal writes a value (as text, nil for NULL) as an SQL literal for driver d.
func Literal(d any, kind string, v *string) string {
	if v == nil {
		return "NULL"
	}
	if l, ok := d.(Literals); ok {
		if lit, ok := l.Literal(kind, *v); ok {
			return lit
		}
	}
	s := *v
	switch kind {
	case ValueNumber:
		if IsNumber(s) {
			return s
		}
	case ValueBool:
		if b, ok := ParseBool(s); ok {
			if b {
				return "TRUE"
			}
			return "FALSE"
		}
	}
	return QuoteString(s)
}

// QuoteString writes 'text', doubling quotes.
func QuoteString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// IsNumber reports whether s is a plain decimal number.
func IsNumber(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\n_xXpP") {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil && !strings.EqualFold(s, "inf") && !strings.EqualFold(s, "nan") && !strings.EqualFold(s, "infinity")
}

// ParseBool reads the usual spellings of a boolean.
func ParseBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "t", "1", "yes", "y", "on":
		return true, true
	case "false", "f", "0", "no", "n", "off":
		return false, true
	}
	return false, false
}

// HexBytes decodes a binary value written as \xABCD (as the editor shows them).
func HexBytes(s string) ([]byte, bool) {
	if !strings.HasPrefix(s, `\x`) {
		return nil, false
	}
	b, err := hex.DecodeString(s[2:])
	return b, err == nil
}

// Params is implemented by drivers that need typed parameters for some text values
// (Oracle dates). ok is false to pass the text.
type Params interface {
	Param(kind, dbType, text string) (v any, ok bool)
}

// BatchLimits is implemented by drivers that limit the parameters of a statement.
// MaxParams 0 means one row per INSERT (no multi-row VALUES).
type BatchLimits interface {
	MaxParams() int
}

// Copier is implemented by drivers that bulk load CSV (PostgreSQL COPY). The CSV has a
// header row; an unquoted empty field is NULL.
type Copier interface {
	CopyCSV(ctx context.Context, c *sql.Conn, table string, cols []string, r io.Reader) (int64, error)
}

// CopierOf returns the bulk loader of d, when it has one that works on its server
// (drivers built on another one can turn it off with CopySupported).
func CopierOf(d any) (Copier, bool) {
	c, ok := d.(Copier)
	if !ok {
		return nil, false
	}
	if s, ok := d.(interface{ CopySupported() bool }); ok && !s.CopySupported() {
		return nil, false
	}
	return c, true
}

// Emptier is implemented by drivers that delete every row of a table with another
// statement than DELETE FROM (ClickHouse wants a WHERE, or TRUNCATE).
type Emptier interface {
	DeleteAll(table string) string
}

// DeleteAll returns the statement deleting every row of table.
func DeleteAll(d any, table string) string {
	if e, ok := d.(Emptier); ok {
		return e.DeleteAll(table)
	}
	return "DELETE FROM " + table
}

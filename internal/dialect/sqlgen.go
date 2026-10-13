package dialect

import (
	"fmt"
	"strconv"
	"strings"
)

// QuoteWith quotes an identifier with the given delimiters, doubling the closing one.
func QuoteWith(ident string, opening, closing byte) string {
	return string(opening) + strings.ReplaceAll(ident, string(closing), string(closing)+string(closing)) + string(closing)
}

// QuoteDouble quotes with "x" (standard SQL).
func QuoteDouble(ident string) string { return QuoteWith(ident, '"', '"') }

// Join qualifies a name with the non-empty parts, each quoted with quote.
func Join(quote func(string) string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, quote(p))
		}
	}
	return strings.Join(out, ".")
}

// subquery wraps q so it can be followed by more SQL: a trailing line comment in q
// must not swallow the closing parenthesis.
func subquery(q string) string {
	return "(\n" + strings.TrimRight(strings.TrimSpace(q), ";") + "\n)"
}

// WrapLimitOffset pages q with LIMIT/OFFSET (PostgreSQL, MySQL, SQLite...).
func WrapLimitOffset(q string, limit, offset int64) string {
	s := "SELECT * FROM " + subquery(q) + " quire_q LIMIT " + strconv.FormatInt(limit, 10)
	if offset > 0 {
		s += " OFFSET " + strconv.FormatInt(offset, 10)
	}
	return s
}

// WrapOffsetFetch pages q with OFFSET … FETCH (Oracle 12c+, standard SQL).
func WrapOffsetFetch(q string, limit, offset int64) string {
	return fmt.Sprintf("SELECT * FROM %s quire_q OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", subquery(q), offset, limit)
}

// CountQuery counts the rows of q.
func CountQuery(q string) string {
	return "SELECT COUNT(*) FROM " + subquery(q) + " quire_q"
}

// Counter is implemented by drivers that count the rows of a query their own way.
type Counter interface {
	Count(q string) string
}

// Count returns the statement counting the rows of q for driver d.
func Count(d any, q string) string {
	if c, ok := d.(Counter); ok {
		return c.Count(q)
	}
	return CountQuery(q)
}

// PlaceholderDollar is $n (PostgreSQL).
func PlaceholderDollar(n int) string { return "$" + strconv.Itoa(n) }

// PlaceholderQuestion is ? (MySQL, SQLite).
func PlaceholderQuestion(int) string { return "?" }

// Update builds a parameterized UPDATE of the row identified by key. Parameters are the
// set values followed by the key values.
func Update(s Syntax, table string, set, key []string) string {
	var b strings.Builder
	b.WriteString("UPDATE " + table + " SET ")
	n := 1
	for i, c := range set {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(s.Quote(c) + " = " + s.Placeholder(n))
		n++
	}
	b.WriteString(" WHERE ")
	for i, c := range key {
		if i > 0 {
			b.WriteString(" AND ")
		}
		b.WriteString(s.Quote(c) + " = " + s.Placeholder(n))
		n++
	}
	return b.String()
}

// Insert builds a parameterized INSERT of one row.
func Insert(s Syntax, table string, cols []string) string {
	if len(cols) == 0 {
		return "INSERT INTO " + table + " DEFAULT VALUES"
	}
	q := make([]string, len(cols))
	p := make([]string, len(cols))
	for i, c := range cols {
		q[i] = s.Quote(c)
		p[i] = s.Placeholder(i + 1)
	}
	return "INSERT INTO " + table + " (" + strings.Join(q, ", ") + ") VALUES (" + strings.Join(p, ", ") + ")"
}

// InsertRows builds a multi-row INSERT with literal-free placeholders for rows rows.
func InsertRows(s Syntax, table string, cols []string, rows int) string {
	var b strings.Builder
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = s.Quote(c)
	}
	b.WriteString("INSERT INTO " + table + " (" + strings.Join(q, ", ") + ") VALUES ")
	n := 1
	for r := range rows {
		if r > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('(')
		for i := range cols {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(s.Placeholder(n))
			n++
		}
		b.WriteByte(')')
	}
	return b.String()
}

// Delete builds a parameterized DELETE of the row identified by key.
func Delete(s Syntax, table string, key []string) string {
	var b strings.Builder
	b.WriteString("DELETE FROM " + table + " WHERE ")
	for i, c := range key {
		if i > 0 {
			b.WriteString(" AND ")
		}
		b.WriteString(s.Quote(c) + " = " + s.Placeholder(i+1))
	}
	return b.String()
}

// Select builds SELECT cols FROM table [WHERE where] [ORDER BY order].
func Select(s Syntax, table string, cols []string, where, order string) string {
	list := "*"
	if len(cols) > 0 {
		q := make([]string, len(cols))
		for i, c := range cols {
			q[i] = s.Quote(c)
		}
		list = strings.Join(q, ", ")
	}
	sql := "SELECT " + list + " FROM " + table
	if w := strings.TrimSpace(where); w != "" {
		sql += "\nWHERE " + w
	}
	if o := strings.TrimSpace(order); o != "" {
		sql += "\nORDER BY " + o
	}
	return sql
}

// AppendLimit adds LIMIT n to a SELECT.
func AppendLimit(q string, limit int64) string {
	return strings.TrimRight(strings.TrimSpace(q), ";") + "\nLIMIT " + strconv.FormatInt(limit, 10)
}

// Keywords that must be quoted when used as identifiers, in any dialect we generate
// readable SQL for.
var keywords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`all alter and any array as asc between both by case cast check collate column
constraint create cross current_date current_time current_timestamp current_user default deferrable delete desc distinct
do drop else end except exists false fetch for foreign from full grant group having in index inner insert intersect into
is join key leading left like limit natural not null offset on only or order outer primary references returning right
select session_user set some table then to trailing true union unique update user using values when where window with`) {
		keywords[w] = true
	}
}

// Pretty is implemented by drivers that leave plain identifiers unquoted in the SQL
// they write for people to read.
type Pretty interface {
	// Plain reports whether ident can be written without quotes.
	Plain(ident string) bool
}

// Readable quotes ident only when the driver needs it.
func Readable(d Syntax, ident string) string {
	if p, ok := d.(Pretty); ok && p.Plain(ident) && !keywords[strings.ToLower(ident)] {
		return ident
	}
	return d.Quote(ident)
}

// ReadableName joins the non-empty parts of a name, quoting only where needed.
func ReadableName(d Syntax, parts ...string) string {
	return Join(func(s string) string { return Readable(d, s) }, parts...)
}

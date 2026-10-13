// Package duckdb is the DuckDB driver, built on duckdb-go v2 (cgo, with the DuckDB
// library linked in). A connection opens a local database file, or an in-memory
// database.
package duckdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	duck "github.com/duckdb/duckdb-go/v2"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/sqlsplit"
)

func init() {
	dialect.Register(&Driver{})
}

// Memory is the database name of an in-memory database.
const Memory = ":memory:"

// Driver is the DuckDB driver.
type Driver struct{}

// Info describes DuckDB.
func (d *Driver) Info() dialect.Info {
	return dialect.Info{
		ID:            "duckdb",
		Label:         "DuckDB",
		Description:   "A local DuckDB file, or an in-memory database for files and data lakes",
		Editor:        "DuckDB",
		File:          true,
		Databases:     true,
		Schemas:       true,
		DatabaseLabel: "Database",
		SchemaLabel:   "Schema",
		Folders:       []string{dialect.KindTable, dialect.KindView, dialect.KindFunction, dialect.KindSequence},
		Transactions:  true,
		// DuckDB DDL is transactional.
		TransactionalDDL: true,
		Upsert:           true,
		Explain:          "EXPLAIN",
		Fields: []dialect.Field{
			{Key: "create", Label: "Create the file when it does not exist", Section: "server", Type: "bool"},
			{Key: "attach", Label: "Attach databases", Section: "advanced", Type: "text", Placeholder: "lake=/path/to/other.duckdb",
				Hint: "Comma separated name=path pairs attached when the database opens."},
			{Key: "threads", Label: "Threads", Section: "advanced", Type: "number", Placeholder: "all cores"},
			{Key: "memoryLimit", Label: "Memory limit", Section: "advanced", Type: "text", Placeholder: "80% of RAM",
				Hint: "For example 4GB."},
		},
		Index:     dialect.IndexFeatures{Methods: []string{"art"}, Expressions: true, IfNotExists: true, Editable: true},
		Functions: functions,
		Snippets:  snippets,
		Note:      "A DuckDB file can be open for writing in one program at a time; open it read-only to share it.",
		Order:     21,
	}
}

// Open opens the database file (or an in-memory database). Every pooled connection
// shares the same database instance.
func (d *Driver) Open(_ context.Context, p profile.Profile, _ string, _ dialect.Network) (*sql.DB, error) {
	dsn, err := dsn(p)
	if err != nil {
		return nil, err
	}
	attach := attachments(p)
	c, err := duck.NewConnector(dsn, nil)
	if err != nil {
		return nil, fmt.Errorf("open DuckDB: %w", err)
	}
	if len(attach) > 0 {
		// ATTACH belongs to the database instance, so it runs once.
		db := sql.OpenDB(c)
		for _, a := range attach {
			stmt := "ATTACH " + dialect.QuoteString(a[1]) + " AS " + d.Quote(a[0]) //nolint:gosec // G202: a quoted path and name
			if p.ReadOnly {
				stmt += " (READ_ONLY)"
			}
			if _, err := db.ExecContext(context.Background(), stmt); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("attach %s: %w", a[0], err)
			}
		}
		db.SetMaxOpenConns(8)
		return db, nil
	}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(8)
	return db, nil
}

func dsn(p profile.Profile) (string, error) {
	if p.URI != "" {
		return p.URI, nil
	}
	q := url.Values{}
	if n, err := strconv.Atoi(p.Options["threads"]); err == nil && n > 0 {
		q.Set("threads", strconv.Itoa(n))
	}
	if m := strings.TrimSpace(p.Options["memoryLimit"]); m != "" {
		q.Set("memory_limit", m)
	}
	path := strings.TrimSpace(p.Database)
	if path == "" || path == Memory {
		path = ""
	} else {
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		path = abs
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if p.ReadOnly || p.Options["create"] != "true" {
				return "", fmt.Errorf("database file %s does not exist", path)
			}
		} else if err != nil {
			return "", err
		}
		if p.ReadOnly {
			q.Set("access_mode", "READ_ONLY")
		}
	}
	if len(q) == 0 {
		return path, nil
	}
	return path + "?" + q.Encode(), nil
}

// attachments returns the name=path pairs to ATTACH.
func attachments(p profile.Profile) [][2]string {
	var out [][2]string
	for pair := range strings.SplitSeq(p.Options["attach"], ",") {
		name, file, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && strings.TrimSpace(name) != "" && strings.TrimSpace(file) != "" {
			out = append(out, [2]string{strings.TrimSpace(name), strings.TrimSpace(file)})
		}
	}
	return out
}

// Version returns the DuckDB version.
func (d *Driver) Version(ctx context.Context, q dialect.Querier) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT version()").Scan(&v)
	return v, err
}

// Current returns the database and schema of a new connection.
func (d *Driver) Current(ctx context.Context, q dialect.Querier) (string, string, error) {
	var db, schema string
	err := q.QueryRowContext(ctx, "SELECT current_database(), current_schema()").Scan(&db, &schema)
	return db, schema, err
}

// Transient reports nothing worth retrying but a lost connection: the database is a
// local file.
func (d *Driver) Transient(err error) bool { return errors.Is(err, driver.ErrBadConn) }

// Quote quotes an identifier with double quotes.
func (d *Driver) Quote(ident string) string { return dialect.QuoteDouble(ident) }

// Placeholder is $n.
func (d *Driver) Placeholder(n int) string { return dialect.PlaceholderDollar(n) }

// Limit pages a query.
func (d *Driver) Limit(q string, limit, offset int64) string {
	return dialect.WrapLimitOffset(q, limit, offset)
}

// LimitRows adds LIMIT n.
func (d *Driver) LimitRows(q string, limit int64) string { return dialect.AppendLimit(q, limit) }

// Split returns the DuckDB script rules ($$ strings).
func (d *Driver) Split() sqlsplit.Options { return sqlsplit.Options{DollarQuotes: true} }

// Use makes database.schema current.
func (d *Driver) Use(database, schema string) []string {
	switch {
	case database != "" && schema != "":
		return []string{"USE " + d.Quote(database) + "." + d.Quote(schema)}
	case database != "":
		return []string{"USE " + d.Quote(database)}
	case schema != "":
		return []string{"USE " + d.Quote(schema)}
	}
	return nil
}

// Explain returns the plan statement.
func (d *Driver) Explain(stmt string) dialect.Plan { return dialect.Plan{Query: "EXPLAIN " + stmt} }

// Upsert inserts or updates on primary key conflicts.
func (d *Driver) Upsert(table string, cols, key []string) string {
	ins := dialect.Insert(d, table, cols)
	q := make([]string, len(key))
	isKey := map[string]bool{}
	for i, k := range key {
		q[i] = d.Quote(k)
		isKey[k] = true
	}
	var set []string
	for _, c := range cols {
		if !isKey[c] {
			set = append(set, d.Quote(c)+" = EXCLUDED."+d.Quote(c))
		}
	}
	if len(set) == 0 {
		return ins + " ON CONFLICT (" + strings.Join(q, ", ") + ") DO NOTHING"
	}
	return ins + " ON CONFLICT (" + strings.Join(q, ", ") + ") DO UPDATE SET " + strings.Join(set, ", ")
}

// Qualify joins database, schema and name.
func (d *Driver) Qualify(database, schema, name string) string {
	return dialect.Join(d.Quote, database, schema, name)
}

var plain = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Plain reports whether ident needs no quotes.
func (d *Driver) Plain(ident string) bool { return plain.MatchString(ident) }

// MaxParams limits the parameters of a multi-row INSERT.
func (d *Driver) MaxParams() int { return 30000 }

// Literal writes binary values with unhex.
func (d *Driver) Literal(kind, text string) (string, bool) {
	if kind == dialect.ValueBinary {
		if b, ok := dialect.HexBytes(text); ok {
			return "unhex('" + hex.EncodeToString(b) + "')", true
		}
	}
	return "", false
}

// FormatValue renders the values duckdb-go scans into its own types: UUIDs (16
// bytes), intervals, maps, lists and structs.
func (d *Driver) FormatValue(dbType string, v any) (string, bool) {
	switch x := v.(type) {
	case []byte:
		if strings.EqualFold(dbType, "UUID") && len(x) == 16 {
			h := hex.EncodeToString(x)
			return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], true
		}
		return "", false
	case duck.Interval:
		return interval(x), true
	case duck.OrderedMap, []any, map[string]any:
		b, err := json.Marshal(x)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() != reflect.Uint8 {
		b, err := json.Marshal(v)
		return string(b), err == nil
	}
	return "", false
}

// interval writes an interval as DuckDB does: 1 year 2 months 3 days 04:05:06.
func interval(i duck.Interval) string {
	var parts []string
	plural := func(n int64, unit string) {
		if n == 0 {
			return
		}
		s := strconv.FormatInt(n, 10) + " " + unit
		if n != 1 && n != -1 {
			s += "s"
		}
		parts = append(parts, s)
	}
	plural(int64(i.Months/12), "year")
	plural(int64(i.Months%12), "month")
	plural(int64(i.Days), "day")
	if i.Micros != 0 || len(parts) == 0 {
		d := time.Duration(i.Micros) * time.Microsecond
		sign := ""
		if d < 0 {
			sign, d = "-", -d
		}
		h, m, s := int64(d/time.Hour), int64(d%time.Hour/time.Minute), d%time.Minute
		t := fmt.Sprintf("%s%02d:%02d:%02d", sign, h, m, int64(s/time.Second))
		if us := int64(s % time.Second / time.Microsecond); us != 0 {
			t += strings.TrimRight(fmt.Sprintf(".%06d", us), "0")
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, " ")
}

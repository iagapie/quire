// Package sqlite is the SQLite driver, built on the cgo-free modernc.org/sqlite.
package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"modernc.org/sqlite"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/sqlsplit"
)

func init() {
	dialect.Register(&Driver{})
}

// Memory is the database name of an in-memory database.
const Memory = ":memory:"

// Driver is the SQLite driver.
type Driver struct{}

// Info describes SQLite.
func (d *Driver) Info() dialect.Info {
	return dialect.Info{
		ID:            "sqlite",
		Label:         "SQLite",
		Description:   "A local database file, or an in-memory database",
		Editor:        "SQLite",
		File:          true,
		Databases:     true,
		DatabaseLabel: "Database",
		Folders:       []string{dialect.KindTable, dialect.KindView, dialect.KindTrigger},
		Transactions:  true,
		// SQLite DDL is transactional too.
		TransactionalDDL: true,
		Upsert:           true,
		Explain:          "EXPLAIN QUERY PLAN",
		Fields: []dialect.Field{
			{Key: "create", Label: "Create the file when it does not exist", Section: "server", Type: "bool"},
			{Key: "foreignKeys", Label: "Enforce foreign keys", Section: "advanced", Type: "bool", Default: "true",
				Hint: "PRAGMA foreign_keys = ON on every connection."},
			{Key: "attach", Label: "Attach databases", Section: "advanced", Type: "text", Placeholder: "aux=/path/to/other.db",
				Hint: "Comma separated name=path pairs attached to every connection."},
		},
		Index: dialect.IndexFeatures{
			Desc: true, Partial: true, Collation: true, Expressions: true, IfNotExists: true, Editable: true,
		},
		Functions: functions,
		Snippets:  snippets,
		Order:     20,
	}
}

// Open opens the database file. The profile Database is the path, or ":memory:".
func (d *Driver) Open(ctx context.Context, p profile.Profile, _ string, _ dialect.Network) (*sql.DB, error) {
	dsn, memory, err := dsn(p)
	if err != nil {
		return nil, err
	}
	conn, err := sqlite.NewConnector(dsn)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(&attacher{Connector: conn, attach: Attachments(p), readOnly: p.ReadOnly})
	db.SetMaxOpenConns(8)
	if memory {
		// The shared in-memory database lives as long as one connection does.
		db.SetMaxIdleConns(8)
		db.SetConnMaxIdleTime(0)
		db.SetConnMaxLifetime(0)
	}
	return db, nil
}

func dsn(p profile.Profile) (string, bool, error) {
	if p.URI != "" {
		return p.URI, strings.Contains(p.URI, "mode=memory") || strings.Contains(p.URI, Memory), nil
	}
	path := strings.TrimSpace(p.Database)
	q := url.Values{}
	q.Set("_busy_timeout", "5000")
	q.Set("_time_format", "sqlite")
	if p.Options["foreignKeys"] != "false" {
		q.Set("_fk", "1")
	}
	memory := path == "" || path == Memory
	if memory {
		// A named shared-cache memory database, so every pooled connection sees it.
		q.Set("mode", "memory")
		q.Set("cache", "shared")
		path = "quire-" + p.ID
	} else {
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", false, err
		}
		path = abs
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if p.ReadOnly || p.Options["create"] != "true" {
				return "", false, fmt.Errorf("database file %s does not exist", path)
			}
		} else if err != nil {
			return "", false, err
		}
		if p.ReadOnly {
			q.Set("mode", "ro")
		} else {
			q.Set("mode", "rwc")
		}
	}
	if p.ReadOnly {
		q.Set("_query_only", "1")
	}
	u := "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + q.Encode()
	return u, memory, nil
}

// Attachments returns the name=path pairs to ATTACH on each connection.
func Attachments(p profile.Profile) [][2]string {
	var out [][2]string
	for pair := range strings.SplitSeq(p.Options["attach"], ",") {
		name, file, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && strings.TrimSpace(name) != "" && strings.TrimSpace(file) != "" {
			out = append(out, [2]string{strings.TrimSpace(name), strings.TrimSpace(file)})
		}
	}
	return out
}

// Version returns the SQLite library version.
func (d *Driver) Version(ctx context.Context, q dialect.Querier) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&v)
	return v, err
}

// Transient reports a busy or locked database, which clears up on its own.
func (d *Driver) Transient(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "(5)")
}

// Quote quotes an identifier.
func (d *Driver) Quote(ident string) string { return dialect.QuoteDouble(ident) }

// Placeholder is ?.
func (d *Driver) Placeholder(n int) string { return dialect.PlaceholderQuestion(n) }

// Limit pages a query.
func (d *Driver) Limit(q string, limit, offset int64) string {
	return dialect.WrapLimitOffset(q, limit, offset)
}

// LimitRows adds LIMIT n.
func (d *Driver) LimitRows(q string, limit int64) string { return dialect.AppendLimit(q, limit) }

// Split returns the SQLite script rules.
func (d *Driver) Split() sqlsplit.Options {
	return sqlsplit.Options{Backticks: true, Brackets: true, BlockStatements: true}
}

// Use does nothing: attached databases are reached with qualified names.
func (d *Driver) Use(_, _ string) []string { return nil }

// Explain returns the query plan statement.
func (d *Driver) Explain(stmt string) dialect.Plan {
	return dialect.Plan{Query: "EXPLAIN QUERY PLAN " + stmt}
}

// Upsert inserts or updates on primary key conflicts.
func (d *Driver) Upsert(table string, cols, key []string) string {
	ins := dialect.Insert(d, table, cols)
	q := make([]string, len(key))
	for i, k := range key {
		q[i] = d.Quote(k)
	}
	var set []string
	for _, c := range cols {
		isKey := false
		for _, k := range key {
			isKey = isKey || k == c
		}
		if !isKey {
			set = append(set, d.Quote(c)+" = excluded."+d.Quote(c))
		}
	}
	if len(set) == 0 {
		return ins + " ON CONFLICT (" + strings.Join(q, ", ") + ") DO NOTHING"
	}
	return ins + " ON CONFLICT (" + strings.Join(q, ", ") + ") DO UPDATE SET " + strings.Join(set, ", ")
}

// Qualify joins the attached database and the name; main is left out.
func (d *Driver) Qualify(database, _, name string) string {
	if database == "main" {
		database = ""
	}
	return dialect.Join(d.Quote, database, name)
}

// attacher attaches the configured databases to every new connection.
type attacher struct {
	driver.Connector
	attach   [][2]string
	readOnly bool
}

func (a *attacher) Connect(ctx context.Context) (driver.Conn, error) {
	c, err := a.Connector.Connect(ctx)
	if err != nil || len(a.attach) == 0 {
		return c, err
	}
	ex, ok := c.(driver.ExecerContext)
	if !ok {
		return c, nil
	}
	for _, at := range a.attach {
		file := at[1]
		if strings.HasPrefix(file, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				file = filepath.Join(home, file[2:])
			}
		}
		uri := "file:" + (&url.URL{Path: file}).EscapedPath()
		if a.readOnly {
			uri += "?mode=ro"
		}
		if _, err := ex.ExecContext(ctx, "ATTACH DATABASE ? AS "+dialect.QuoteDouble(at[0]), []driver.NamedValue{{Ordinal: 1, Value: uri}}); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("attach %s: %w", at[0], err)
		}
	}
	return c, nil
}

// Current is always the main database.
func (d *Driver) Current(context.Context, dialect.Querier) (string, string, error) {
	return "main", "", nil
}

// Plain reports whether ident needs no quotes.
func (d *Driver) Plain(ident string) bool { return plain.MatchString(ident) }

var plain = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Literal writes booleans as 1/0 and binary values as X'…'.
func (d *Driver) Literal(kind, text string) (string, bool) {
	switch kind {
	case dialect.ValueBool:
		if b, ok := dialect.ParseBool(text); ok {
			return map[bool]string{true: "1", false: "0"}[b], true
		}
	case dialect.ValueBinary:
		if b, ok := dialect.HexBytes(text); ok {
			return "X'" + hex.EncodeToString(b) + "'", true
		}
	}
	return "", false
}

// MaxParams stays below SQLite's limit of 32766 variables.
func (d *Driver) MaxParams() int { return 30000 }

// Package postgres is the PostgreSQL driver, built on pgx v5 through database/sql.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/sqlsplit"
)

func init() {
	dialect.Register(&Driver{info: info()})
}

// Driver is the PostgreSQL driver. Wire-compatible databases reuse it with their own Info.
type Driver struct {
	info dialect.Info
	// noCopy: the server has no COPY … FROM STDIN (Redshift loads files from S3).
	noCopy bool
	// clientReadOnly: the server cannot make a session read-only, so read-only
	// connections rely on the client guard alone.
	clientReadOnly bool
}

// Option changes a driver built with New.
type Option func(*Driver)

// NoCopy is for servers without COPY … FROM STDIN: imports use INSERT statements.
func NoCopy() Option { return func(d *Driver) { d.noCopy = true } }

// ClientReadOnly is for servers without default_transaction_read_only.
func ClientReadOnly() Option { return func(d *Driver) { d.clientReadOnly = true } }

// New returns a driver with the given info, for PostgreSQL-compatible databases.
func New(info dialect.Info, opts ...Option) *Driver {
	d := &Driver{info: info}
	for _, o := range opts {
		o(d)
	}
	return d
}

// CopySupported reports whether CopyCSV works on this server.
func (d *Driver) CopySupported() bool { return !d.noCopy }

// Info describes PostgreSQL.
func (d *Driver) Info() dialect.Info { return d.info }

// PerDatabasePools reports that a PostgreSQL connection is bound to one database.
func (d *Driver) PerDatabasePools() bool { return true }

// Info returns the default PostgreSQL description, for drivers built on this one.
func Info() dialect.Info { return info() }

func info() dialect.Info {
	return dialect.Info{
		ID:               "postgres",
		Label:            "PostgreSQL",
		Description:      "PostgreSQL 11 and later, including Amazon RDS and Aurora",
		DefaultPort:      5432,
		DefaultUser:      "postgres",
		DefaultDatabase:  "postgres",
		Editor:           "PostgreSQL",
		Databases:        true,
		Schemas:          true,
		DatabaseLabel:    "Database",
		SchemaLabel:      "Schema",
		Folders:          []string{dialect.KindTable, dialect.KindView, dialect.KindMatView, dialect.KindFunction, dialect.KindProcedure, dialect.KindSequence, dialect.KindTrigger},
		Transactions:     true,
		TransactionalDDL: true,
		TLS:              []string{profile.TLSDisable, profile.TLSPrefer, profile.TLSRequire, profile.TLSVerifyCA, profile.TLSVerifyFull},
		SSH:              true,
		Upsert:           true,
		Explain:          "EXPLAIN",
		Fields: []dialect.Field{
			{Key: "params", Label: "Extra parameters", Section: "advanced", Type: "text", Placeholder: "application_name=reports connect_timeout=10",
				Hint: "libpq key=value settings added to the connection string."},
			{Key: "simpleProtocol", Label: "Use the simple query protocol", Section: "advanced", Type: "bool",
				Hint: "For PgBouncer in transaction mode and other proxies without prepared statements."},
		},
		Index: dialect.IndexFeatures{
			Methods: []string{"btree", "hash", "gin", "gist", "spgist", "brin"},
			Desc:    true, Nulls: true, Partial: true, Include: true, Concurrently: true, Tablespace: true,
			FillFactor: true, Collation: true, Expressions: true, IfNotExists: true, Editable: true,
		},
		Functions: functions,
		Snippets:  snippets,
		Order:     10,
	}
}

// Open connects to database (the profile default when empty).
func (d *Driver) Open(ctx context.Context, p profile.Profile, database string, n dialect.Network) (*sql.DB, error) {
	cfg, err := d.config(p, database, n)
	if err != nil {
		return nil, err
	}
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(45 * time.Second)
	return db, nil
}

func (d *Driver) config(p profile.Profile, database string, n dialect.Network) (*pgx.ConnConfig, error) {
	var dsn string
	if p.URI != "" {
		dsn = p.URI
	} else {
		parts := []string{}
		add := func(k, v string) {
			if v != "" {
				parts = append(parts, k+"="+quoteValue(v))
			}
		}
		host := p.Host
		if host == "" {
			host = "localhost"
		}
		add("host", host)
		port := p.Port
		if port == 0 {
			port = d.info.DefaultPort
		}
		add("port", strconv.Itoa(port))
		add("user", p.User)
		add("password", p.Password)
		add("dbname", p.Database)
		add("sslmode", sslMode(p.TLS.Mode))
		dsn = strings.Join(parts, " ")
		if extra := p.Options["params"]; extra != "" {
			dsn += " " + extra
		}
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid connection settings: %w", err)
	}
	if database != "" {
		cfg.Database = database
	}
	if p.URI == "" {
		tc, err := dialect.TLSConfig(p.TLS, cfg.Host)
		if err != nil {
			return nil, err
		}
		if cfg.TLSConfig != nil {
			cfg.TLSConfig = tc
		}
		for _, fb := range cfg.Fallbacks {
			if fb.TLSConfig != nil {
				fb.TLSConfig = tc
			}
		}
	}
	if n.Dial != nil {
		cfg.DialFunc = n.Dial
	} else {
		dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 15 * time.Second}
		cfg.DialFunc = dialer.DialContext
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 15 * time.Second
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	if cfg.RuntimeParams["application_name"] == "" {
		cfg.RuntimeParams["application_name"] = "Quire"
	}
	if p.ReadOnly && !d.clientReadOnly {
		// Every transaction of the session is read-only, enforced by the server.
		cfg.RuntimeParams["default_transaction_read_only"] = "on"
	}
	// Cancelling an operation asks the server to stop it and keeps the connection, so
	// the session (SET, temporary tables, an open transaction) survives a Stop.
	cfg.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: c, DeadlineDelay: 5 * time.Second}
	}
	// Statements are described, not cached: ad-hoc SQL would only fill the cache, and a
	// cached plan breaks after the user changes a table.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
	if p.Options["simpleProtocol"] == "true" {
		cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	}
	return cfg, nil
}

func sslMode(m string) string {
	switch m {
	case profile.TLSDisable, profile.TLSPrefer, profile.TLSRequire, profile.TLSVerifyCA, profile.TLSVerifyFull:
		return m
	}
	return profile.TLSPrefer
}

func quoteValue(v string) string {
	if v != "" && !strings.ContainsAny(v, " '\\") {
		return v
	}
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

// Version returns the server version.
func (d *Driver) Version(ctx context.Context, q dialect.Querier) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT current_setting('server_version')").Scan(&v)
	return v, err
}

// Transient reports network failures and server states worth retrying.
func (d *Driver) Transient(err error) bool {
	if err == nil {
		return false
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch {
		case strings.HasPrefix(pe.Code, "08"), // connection exception
			pe.Code == "57P01", pe.Code == "57P02", pe.Code == "57P03", // shutdown, cannot connect now
			pe.Code == "40001", pe.Code == "40P01": // serialization failure, deadlock
			return true
		}
		return false
	}
	return pgconn.SafeToRetry(err) || dialect.NetworkError(err)
}

// InTransaction reports whether the connection is inside a transaction.
func (d *Driver) InTransaction(ctx context.Context, c *sql.Conn) (bool, error) {
	var in bool
	err := c.Raw(func(dc any) error {
		conn, ok := dc.(*stdlib.Conn)
		if !ok {
			return dialect.ErrUnsupported
		}
		in = conn.Conn().PgConn().TxStatus() != 'I'
		return nil
	})
	return in, err
}

// Quote quotes an identifier.
func (d *Driver) Quote(ident string) string { return dialect.QuoteDouble(ident) }

// Placeholder is $n.
func (d *Driver) Placeholder(n int) string { return dialect.PlaceholderDollar(n) }

// Limit pages a query.
func (d *Driver) Limit(q string, limit, offset int64) string {
	return dialect.WrapLimitOffset(q, limit, offset)
}

// LimitRows adds LIMIT n.
func (d *Driver) LimitRows(q string, limit int64) string { return dialect.AppendLimit(q, limit) }

// Split returns the PostgreSQL script rules.
func (d *Driver) Split() sqlsplit.Options {
	return sqlsplit.Options{DollarQuotes: true, NestedComments: true, EStrings: true}
}

// Use sets the schema search path.
func (d *Driver) Use(_, schema string) []string {
	if schema == "" {
		return nil
	}
	path := d.Quote(schema)
	if schema != "public" {
		path += ", public"
	}
	return []string{"SET search_path TO " + path}
}

// Explain returns the plan statement.
func (d *Driver) Explain(stmt string) dialect.Plan { return dialect.Plan{Query: "EXPLAIN " + stmt} }

// Upsert inserts or updates on primary key conflicts.
func (d *Driver) Upsert(table string, cols, key []string) string {
	ins := dialect.Insert(d, table, cols)
	q := make([]string, len(key))
	for i, k := range key {
		q[i] = d.Quote(k)
	}
	var set []string
	for _, c := range cols {
		if !contains(key, c) {
			set = append(set, d.Quote(c)+" = EXCLUDED."+d.Quote(c))
		}
	}
	if len(set) == 0 {
		return ins + " ON CONFLICT (" + strings.Join(q, ", ") + ") DO NOTHING"
	}
	return ins + " ON CONFLICT (" + strings.Join(q, ", ") + ") DO UPDATE SET " + strings.Join(set, ", ")
}

// Qualify joins schema and name (a PostgreSQL connection is bound to one database).
func (d *Driver) Qualify(_, schema, name string) string { return dialect.Join(d.Quote, schema, name) }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Current returns the database and schema of a new connection.
func (d *Driver) Current(ctx context.Context, q dialect.Querier) (string, string, error) {
	var db, schema sql.NullString
	err := q.QueryRowContext(ctx, "SELECT current_database(), current_schema()").Scan(&db, &schema)
	return db.String, schema.String, err
}

// Fold lowers unquoted identifiers, as PostgreSQL does.
func (d *Driver) Fold(ident string) string { return strings.ToLower(ident) }

// Plain reports whether ident needs no quotes: lower case letters, digits, _ and $.
func (d *Driver) Plain(ident string) bool { return plainLower.MatchString(ident) }

var plainLower = regexp.MustCompile(`^[a-z_][a-z0-9_$]*$`)

// CopyCSV bulk loads CSV with COPY … FROM STDIN; the server converts the text to the
// column types.
func (d *Driver) CopyCSV(ctx context.Context, c *sql.Conn, table string, cols []string, r io.Reader) (int64, error) {
	q := make([]string, len(cols))
	for i, col := range cols {
		q[i] = d.Quote(col)
	}
	stmt := "COPY " + table + " (" + strings.Join(q, ", ") + ") FROM STDIN WITH (FORMAT csv, HEADER true)"
	var n int64
	err := c.Raw(func(dc any) error {
		conn, ok := dc.(*stdlib.Conn)
		if !ok {
			return dialect.ErrUnsupported
		}
		tag, err := conn.Conn().PgConn().CopyFrom(ctx, r, stmt)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

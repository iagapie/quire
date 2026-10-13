// Package mysql is the MySQL and MariaDB driver, built on go-sql-driver/mysql.
package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/sqlsplit"
)

func init() {
	dialect.Register(&Driver{info: info()})
	dialect.Register(&Driver{info: mariaInfo(), maria: true})
}

// Driver is the MySQL driver. MariaDB and wire-compatible databases reuse it with
// their own Info.
type Driver struct {
	info  dialect.Info
	maria bool
	// clientReadOnly: the server has no read-only sessions, so read-only connections
	// rely on the client guard alone.
	clientReadOnly bool
}

// Option changes a driver built with New.
type Option func(*Driver)

// ClientReadOnly is for servers without read-only sessions (TiDB accepts SET SESSION
// TRANSACTION READ ONLY only as a no-op).
func ClientReadOnly() Option { return func(d *Driver) { d.clientReadOnly = true } }

// New returns a driver with the given info, for MySQL-compatible databases.
func New(info dialect.Info, opts ...Option) *Driver {
	d := &Driver{info: info}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Info describes the database.
func (d *Driver) Info() dialect.Info { return d.info }

// Info returns the default MySQL description, for drivers built on this one.
func Info() dialect.Info { return info() }

func info() dialect.Info {
	return dialect.Info{
		ID:            "mysql",
		Label:         "MySQL",
		Description:   "MySQL 5.7 and 8, including Amazon RDS, Aurora MySQL and Azure Database for MySQL",
		DefaultPort:   3306,
		DefaultUser:   "root",
		Editor:        "MySQL",
		Databases:     true,
		DatabaseLabel: "Database",
		Folders:       []string{dialect.KindTable, dialect.KindView, dialect.KindFunction, dialect.KindProcedure, dialect.KindTrigger},
		Transactions:  true,
		TLS:           []string{profile.TLSDisable, profile.TLSPrefer, profile.TLSRequire, profile.TLSVerifyCA, profile.TLSVerifyFull},
		SSH:           true,
		Upsert:        true,
		Explain:       "EXPLAIN",
		Fields: []dialect.Field{
			{Key: "params", Label: "Extra parameters", Section: "advanced", Type: "text", Placeholder: "charset=utf8mb4&sql_mode=ANSI_QUOTES",
				Hint: "Driver parameters and session variables, as in a DSN query string."},
			{Key: "cleartext", Label: "Allow cleartext passwords", Section: "auth", Type: "bool",
				Hint: "For AWS IAM authentication and PAM. Use it only with TLS."},
		},
		Index: dialect.IndexFeatures{
			Methods: []string{"btree", "hash", "fulltext", "spatial"},
			Desc:    true, Expressions: true, Length: true, Invisible: true, Editable: true,
		},
		Functions: functions,
		Snippets:  snippets,
		Order:     30,
	}
}

func mariaInfo() dialect.Info {
	i := info()
	i.ID, i.Label, i.Editor = "mariadb", "MariaDB", "MariaSQL"
	i.Description = "MariaDB 10.3 and later, including Amazon RDS for MariaDB"
	i.Index.Expressions, i.Index.Invisible, i.Index.IfNotExists = false, false, true
	i.Order = 31
	return i
}

// Open creates a connection pool.
func (d *Driver) Open(_ context.Context, p profile.Profile, database string, n dialect.Network) (*sql.DB, error) {
	cfg, err := d.config(p, database, n)
	if err != nil {
		return nil, err
	}
	conn, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	if p.ReadOnly && !d.clientReadOnly {
		// Every transaction of the session is read-only, enforced by the server.
		conn = &dialect.InitConnector{Connector: conn, Init: []string{"SET SESSION TRANSACTION READ ONLY"}}
	}
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(45 * time.Second)
	return db, nil
}

func (d *Driver) config(p profile.Profile, database string, n dialect.Network) (*mysql.Config, error) {
	var (
		cfg *mysql.Config
		err error
	)
	if p.URI != "" {
		cfg, err = mysql.ParseDSN(fromURL(p.URI))
		if err != nil {
			return nil, fmt.Errorf("invalid connection string: %w", err)
		}
	} else {
		cfg = mysql.NewConfig()
		host := p.Host
		if host == "" {
			host = "localhost"
		}
		port := p.Port
		if port == 0 {
			port = d.info.DefaultPort
		}
		cfg.User, cfg.Passwd = p.User, p.Password
		cfg.Net, cfg.Addr = "tcp", net.JoinHostPort(host, strconv.Itoa(port))
		cfg.DBName = p.Database
		if extra := strings.TrimLeft(strings.TrimSpace(p.Options["params"]), "?&"); extra != "" {
			dsn := cfg.FormatDSN()
			sep := "?"
			if strings.Contains(dsn, "?") {
				sep = "&"
			}
			if cfg, err = mysql.ParseDSN(dsn + sep + extra); err != nil {
				return nil, fmt.Errorf("invalid extra parameters: %w", err)
			}
		}
		tc, err := dialect.TLSConfig(p.TLS, host)
		if err != nil {
			return nil, err
		}
		cfg.TLS, cfg.TLSConfig = tc, ""
		cfg.AllowFallbackToPlaintext = p.TLS.Mode == profile.TLSPrefer || p.TLS.Mode == ""
		cfg.AllowCleartextPasswords = p.Options["cleartext"] == "true"
	}
	if database != "" {
		cfg.DBName = database
	}
	if n.Dial != nil {
		cfg.DialFunc = n.Dial
	} else {
		dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 15 * time.Second}
		cfg.DialFunc = dialer.DialContext
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.ConnectionAttributes == "" {
		cfg.ConnectionAttributes = "program_name:Quire"
	}
	// Statements are split by the application; values stay text, as the server sends them.
	cfg.MultiStatements, cfg.ParseTime, cfg.InterpolateParams = false, false, false
	return cfg, nil
}

// fromURL turns mysql://user:pass@host:port/db?params into a driver DSN; other text
// is returned as it is.
func fromURL(s string) string {
	if !strings.HasPrefix(s, "mysql://") && !strings.HasPrefix(s, "mariadb://") {
		return s
	}
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	var b strings.Builder
	if u.User != nil {
		b.WriteString(u.User.Username())
		if pw, ok := u.User.Password(); ok {
			b.WriteString(":" + pw)
		}
		b.WriteString("@")
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "3306")
	}
	b.WriteString("tcp(" + host + ")/" + strings.TrimPrefix(u.Path, "/"))
	if u.RawQuery != "" {
		b.WriteString("?" + u.RawQuery)
	}
	return b.String()
}

// Version returns the server version.
func (d *Driver) Version(ctx context.Context, q dialect.Querier) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT VERSION()").Scan(&v)
	return v, err
}

// Current returns the default database of a new connection.
func (d *Driver) Current(ctx context.Context, q dialect.Querier) (string, string, error) {
	var db sql.NullString
	err := q.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&db)
	return db.String, "", err
}

// Transient reports network failures and server states worth retrying.
func (d *Driver) Transient(err error) bool {
	if err == nil {
		return false
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1040, // too many connections
			1053,       // server shutdown in progress
			1213,       // deadlock
			1927,       // connection was killed (MariaDB)
			2006, 2013: // server has gone away, lost connection
			return true
		}
		return false
	}
	return errors.Is(err, mysql.ErrInvalidConn) || errors.Is(err, driver.ErrBadConn) || dialect.NetworkError(err)
}

// SessionID returns the connection id, used to interrupt its statements.
func (d *Driver) SessionID(ctx context.Context, c *sql.Conn) (string, error) {
	var id uint64
	err := c.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id)
	return strconv.FormatUint(id, 10), err
}

// Interrupt stops the statement of connection id and keeps the connection: the
// driver would close it when a statement is cancelled.
func (d *Driver) Interrupt(ctx context.Context, q dialect.Querier, id string) error {
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return fmt.Errorf("invalid connection id %q", id)
	}
	_, err := q.ExecContext(ctx, "KILL QUERY "+id)
	return err
}

// Quote quotes an identifier with backticks.
func (d *Driver) Quote(ident string) string { return dialect.QuoteWith(ident, '`', '`') }

// Placeholder is ?.
func (d *Driver) Placeholder(n int) string { return dialect.PlaceholderQuestion(n) }

// Limit pages a query.
func (d *Driver) Limit(q string, limit, offset int64) string {
	return dialect.WrapLimitOffset(q, limit, offset)
}

// LimitRows adds LIMIT n.
func (d *Driver) LimitRows(q string, limit int64) string { return dialect.AppendLimit(q, limit) }

// Split returns the MySQL script rules.
func (d *Driver) Split() sqlsplit.Options {
	return sqlsplit.Options{HashComments: true, Backticks: true, BackslashEscapes: true, Delimiter: true, BlockStatements: true}
}

// Use makes database the default one.
func (d *Driver) Use(database, _ string) []string {
	if database == "" {
		return nil
	}
	return []string{"USE " + d.Quote(database)}
}

// Explain returns the plan statement.
func (d *Driver) Explain(stmt string) dialect.Plan { return dialect.Plan{Query: "EXPLAIN " + stmt} }

// Upsert inserts or updates rows with the same primary or unique key.
func (d *Driver) Upsert(table string, cols, key []string) string {
	ins := dialect.Insert(d, table, cols)
	var set []string
	for _, c := range cols {
		isKey := false
		for _, k := range key {
			isKey = isKey || k == c
		}
		if !isKey {
			set = append(set, d.Quote(c)+" = VALUES("+d.Quote(c)+")")
		}
	}
	if len(set) == 0 && len(key) > 0 {
		set = append(set, d.Quote(key[0])+" = "+d.Quote(key[0]))
	}
	return ins + " ON DUPLICATE KEY UPDATE " + strings.Join(set, ", ")
}

// Qualify joins database and name.
func (d *Driver) Qualify(database, _, name string) string {
	return dialect.Join(d.Quote, database, name)
}

// Plain reports whether ident needs no quotes.
func (d *Driver) Plain(ident string) bool { return plain.MatchString(ident) }

var plain = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

// FormatValue renders BIT values as numbers.
func (d *Driver) FormatValue(dbType string, v any) (string, bool) {
	b, ok := v.([]byte)
	if !ok || dbType != "BIT" || len(b) > 8 {
		return "", false
	}
	var n uint64
	for _, x := range b {
		n = n<<8 | uint64(x)
	}
	return strconv.FormatUint(n, 10), true
}

// Literal writes booleans as 1/0, binary values as X'…', and escapes backslashes in
// strings (MySQL reads them as escapes).
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
	case dialect.ValueNumber:
		if dialect.IsNumber(text) {
			return text, true
		}
	}
	return "'" + strings.NewReplacer(`\`, `\\`, "'", "''").Replace(text) + "'", true
}

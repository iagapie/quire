// Package clickhouse is the ClickHouse driver, built on clickhouse-go v2 through
// database/sql, over the native protocol or HTTP.
package clickhouse

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/sqlsplit"
)

func init() {
	dialect.Register(&Driver{})
}

// Driver is the ClickHouse driver.
type Driver struct{}

// Info describes ClickHouse.
func (d *Driver) Info() dialect.Info {
	return dialect.Info{
		ID:              "clickhouse",
		Label:           "ClickHouse",
		Description:     "ClickHouse 23.8 and later, self-hosted or ClickHouse Cloud",
		DefaultPort:     9000,
		DefaultUser:     "default",
		DefaultDatabase: "default",
		Editor:          "ClickHouse",
		Databases:       true,
		DatabaseLabel:   "Database",
		Folders:         []string{dialect.KindTable, dialect.KindView, dialect.KindMatView},
		TLS:             []string{profile.TLSDisable, profile.TLSRequire, profile.TLSVerifyCA, profile.TLSVerifyFull},
		SSH:             true,
		Explain:         "EXPLAIN",
		URIPlaceholder:  "clickhouse://user:password@host:9000/default?secure=true",
		Fields: []dialect.Field{
			{Key: "protocol", Label: "Protocol", Section: "server", Type: "select", Default: "native",
				Options: []dialect.Choice{{Value: "native", Label: "Native (TCP 9000, TLS 9440)"}, {Value: "http", Label: "HTTP (8123, HTTPS 8443)"}},
				Hint:    "ClickHouse Cloud listens on 9440 (native) and 8443 (HTTPS), with TLS."},
			{Key: "compress", Label: "Compress data (LZ4)", Section: "advanced", Type: "bool",
				Hint: "Less traffic for large results, a little more CPU."},
			{Key: "settings", Label: "Settings", Section: "advanced", Type: "text", Placeholder: "max_execution_time=60 max_threads=4",
				Hint: "Session settings as name=value pairs."},
		},
		// Data skipping indexes are listed; they are changed with ALTER TABLE.
		Index:     dialect.IndexFeatures{},
		Functions: functions,
		Snippets:  snippets,
		Note:      "Rows cannot be edited in the grid: ClickHouse tables have sorting keys, not unique keys. Data skipping indexes are listed with the primary key.",
		Order:     50,
	}
}

// Open creates a connection pool.
func (d *Driver) Open(_ context.Context, p profile.Profile, database string, n dialect.Network) (*sql.DB, error) {
	opt, err := d.options(p, database, n)
	if err != nil {
		return nil, err
	}
	db := ch.OpenDB(opt)
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(45 * time.Second)
	return db, nil
}

func (d *Driver) options(p profile.Profile, database string, n dialect.Network) (*ch.Options, error) {
	var opt *ch.Options
	if p.URI != "" {
		var err error
		if opt, err = ch.ParseDSN(p.URI); err != nil {
			return nil, fmt.Errorf("invalid connection string: %w", err)
		}
	} else {
		host := p.Host
		if host == "" {
			host = "localhost"
		}
		port := p.Port
		if port == 0 {
			port = d.Info().DefaultPort
		}
		opt = &ch.Options{
			Addr: []string{net.JoinHostPort(host, strconv.Itoa(port))},
			Auth: ch.Auth{Database: p.Database, Username: p.User, Password: p.Password},
		}
		if p.Options["protocol"] == "http" {
			opt.Protocol = ch.HTTP
		}
		// ClickHouse has separate ports for TLS, so there is nothing to prefer: "prefer"
		// (the profile default) is a plain connection.
		t := p.TLS
		if t.Mode == profile.TLSPrefer {
			t.Mode = profile.TLSDisable
		}
		tc, err := dialect.TLSConfig(t, host)
		if err != nil {
			return nil, err
		}
		opt.TLS = tc
		if p.Options["compress"] == "true" {
			opt.Compression = &ch.Compression{Method: ch.CompressionLZ4}
		}
		settings, err := parseSettings(p.Options["settings"])
		if err != nil {
			return nil, err
		}
		opt.Settings = settings
	}
	if database != "" {
		opt.Auth.Database = database
	}
	if opt.Settings == nil {
		opt.Settings = ch.Settings{}
	}
	if p.ReadOnly {
		// Reads and SET only: the server refuses writes and DDL for the session.
		opt.Settings["readonly"] = 2
	}
	dial := n.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 15 * time.Second}).DialContext
	}
	tc := opt.TLS
	native := opt.Protocol == ch.Native
	opt.DialContext = func(ctx context.Context, addr string) (net.Conn, error) {
		c, err := dial(ctx, "tcp", addr)
		if err != nil || !native || tc == nil {
			return c, err // HTTPS: the HTTP transport adds TLS itself
		}
		// With its own dialer the client leaves TLS to it on the native protocol.
		cfg := tc.Clone()
		if cfg.ServerName == "" {
			cfg.ServerName, _, _ = net.SplitHostPort(addr)
		}
		tlsConn := tls.Client(c, cfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = c.Close()
			return nil, err
		}
		return tlsConn, nil
	}
	if opt.DialTimeout == 0 {
		opt.DialTimeout = 15 * time.Second
	}
	opt.ClientInfo.Products = append(opt.ClientInfo.Products, struct{ Name, Version string }{"Quire", ""})
	return opt, nil
}

// parseSettings reads "name=value name=value" (commas or new lines also separate).
func parseSettings(s string) (ch.Settings, error) {
	out := ch.Settings{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '\n' || r == '\t' }) {
		k, v, ok := strings.Cut(f, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid setting %q: use name=value", f)
		}
		out[k] = v
	}
	return out, nil
}

// Version returns the server version.
func (d *Driver) Version(ctx context.Context, q dialect.Querier) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT version()").Scan(&v)
	return v, err
}

// Current returns the database of a new connection.
func (d *Driver) Current(ctx context.Context, q dialect.Querier) (string, string, error) {
	var db string
	err := q.QueryRowContext(ctx, "SELECT currentDatabase()").Scan(&db)
	return db, "", err
}

// Transient reports network failures and server states worth retrying.
func (d *Driver) Transient(err error) bool {
	if err == nil {
		return false
	}
	var ex *ch.Exception
	if errors.As(err, &ex) {
		switch ex.Code {
		case 3, 32, // unexpected end of file, read after EOF
			202, // too many simultaneous queries
			209, // socket timeout
			210, // network error
			242, // table is read-only (replica lost its session to Keeper)
			252, // too many parts (merges are behind)
			999: // Keeper exception
			return true
		}
		return false
	}
	return errors.Is(err, driver.ErrBadConn) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || dialect.NetworkError(err)
}

// SessionID identifies a native-protocol connection by its client address and port,
// as system.processes shows them. HTTP requests have no lasting connection, so they
// are cancelled the usual way.
func (d *Driver) SessionID(ctx context.Context, c *sql.Conn) (string, error) {
	var addr string
	var port uint16
	err := c.QueryRowContext(ctx, `SELECT IPv6NumToString(address), port FROM system.processes
WHERE query_id = queryID() AND interface = 1`).Scan(&addr, &port)
	if err != nil {
		return "", err
	}
	return addr + " " + strconv.Itoa(int(port)), nil
}

// Interrupt kills the query running on the connection with that id, from another
// connection: the client would close a connection whose query it cancels.
func (d *Driver) Interrupt(ctx context.Context, q dialect.Querier, id string) error {
	addr, port, ok := strings.Cut(id, " ")
	ip := net.ParseIP(addr)
	n, err := strconv.ParseUint(port, 10, 16)
	if !ok || ip == nil || err != nil {
		return fmt.Errorf("invalid connection id %q", id)
	}
	_, err = q.ExecContext(ctx, "KILL QUERY WHERE address = toIPv6('"+ip.String()+"') AND port = "+strconv.FormatUint(n, 10)+" ASYNC")
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

// Split returns the ClickHouse script rules: # comments, backticks, backslash escapes in strings
// and $heredoc$ strings.
func (d *Driver) Split() sqlsplit.Options {
	return sqlsplit.Options{HashComments: true, Backticks: true, BackslashEscapes: true, DollarQuotes: true}
}

// Use makes database the current one.
func (d *Driver) Use(database, _ string) []string {
	if database == "" {
		return nil
	}
	return []string{"USE " + d.Quote(database)}
}

// Explain returns the plan statement.
func (d *Driver) Explain(stmt string) dialect.Plan { return dialect.Plan{Query: "EXPLAIN " + stmt} }

// Upsert is not available: ClickHouse replaces rows only through table engines
// (ReplacingMergeTree).
func (d *Driver) Upsert(string, []string, []string) string { return "" }

// DeleteAll empties a table with TRUNCATE: a DELETE needs a WHERE and only marks the
// rows.
func (d *Driver) DeleteAll(table string) string { return "TRUNCATE TABLE " + table }

// Qualify joins database and name.
func (d *Driver) Qualify(database, _, name string) string {
	return dialect.Join(d.Quote, database, name)
}

var plain = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Plain reports whether ident needs no quotes.
func (d *Driver) Plain(ident string) bool { return plain.MatchString(ident) }

// MaxParams: values are bound by the client into the statement text, so batches are
// limited by statement size, not by a parameter count.
func (d *Driver) MaxParams() int { return 20000 }

// Literal escapes backslashes in strings (ClickHouse reads them as escapes) and
// writes binary values with unhex.
func (d *Driver) Literal(kind, text string) (string, bool) {
	switch kind {
	case dialect.ValueBinary:
		if b, ok := dialect.HexBytes(text); ok {
			return "unhex('" + hex.EncodeToString(b) + "')", true
		}
	case dialect.ValueNumber:
		if dialect.IsNumber(text) {
			return text, true
		}
		return "", false
	case dialect.ValueBool:
		return "", false
	}
	return "'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(text) + "'", true
}

// FormatValue renders arrays, maps and tuples as JSON (the query engine has already
// dereferenced nullable values).
func (d *Driver) FormatValue(_ string, v any) (string, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return "", false // []byte: binary or text, as with other drivers
		}
	case reflect.Map:
	case reflect.Struct:
		if _, ok := v.(fmt.Stringer); ok {
			return "", false // time, decimal, UUID…
		}
	default:
		return "", false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(b), true
}

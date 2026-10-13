// Package mssql is the SQL Server and Azure SQL driver, built on microsoft/go-mssqldb.
package mssql

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
	"sync"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/sqlsplit"
)

func init() {
	// PRINT output, informational messages and row counts reach the statement that
	// runs (see dialect.Notify); nothing is logged otherwise.
	mssql.SetContextLogger(messages{})
	dialect.Register(&Driver{})
}

type messages struct{}

var rowCount = regexp.MustCompile(`(\d+)`)

func (messages) Log(ctx context.Context, category msdsn.Log, msg string) {
	switch category {
	case msdsn.LogMessages:
		dialect.Notify(ctx, msg)
	case msdsn.LogRows:
		// Row counts read as in SQL Server Management Studio.
		if m := rowCount.FindStringSubmatch(msg); m != nil {
			if m[1] == "1" {
				msg = "(1 row affected)"
			} else {
				msg = "(" + m[1] + " rows affected)"
			}
		}
		dialect.Notify(ctx, msg)
	}
}

// Driver is the SQL Server driver.
type Driver struct{}

// Info describes SQL Server.
func (d *Driver) Info() dialect.Info {
	return dialect.Info{
		ID:               "sqlserver",
		Label:            "SQL Server",
		Description:      "SQL Server 2016 and later, Azure SQL Database and Managed Instance, Amazon RDS",
		DefaultPort:      1433,
		DefaultUser:      "sa",
		Editor:           "MSSQL",
		Databases:        true,
		Schemas:          true,
		DatabaseLabel:    "Database",
		SchemaLabel:      "Schema",
		Folders:          []string{dialect.KindTable, dialect.KindView, dialect.KindFunction, dialect.KindProcedure, dialect.KindSequence, dialect.KindTrigger},
		Transactions:     true,
		TransactionalDDL: true,
		TLS:              []string{profile.TLSDisable, profile.TLSPrefer, profile.TLSRequire, profile.TLSVerifyCA, profile.TLSVerifyFull},
		SSH:              true,
		Upsert:           true,
		Explain:          "Show plan",
		Fields: []dialect.Field{
			{Key: "instance", Label: "Instance", Section: "server", Type: "text", Placeholder: "SQLEXPRESS",
				Hint: "A named instance, found through the SQL Server Browser. Leave empty when a port is set."},
			{Key: "params", Label: "Extra parameters", Section: "advanced", Type: "text", Placeholder: "app name=reports&packet size=8192",
				Hint: "go-mssqldb connection parameters, as in a sqlserver:// query string."},
		},
		Index: dialect.IndexFeatures{
			Desc: true, Partial: true, Include: true, FillFactor: true, Clustered: true, Editable: true,
		},
		Functions: functions,
		Snippets:  snippets,
		Note:      "Read-only connections are enforced by Quire and sent as ApplicationIntent=ReadOnly; for a guarantee on the server, use a login that can only read.",
		Order:     40,
	}
}

// PerDatabasePools reports that connections are opened per database: Azure SQL
// Database does not support USE.
func (d *Driver) PerDatabasePools() bool { return true }

// Open creates a connection pool to database (the profile default when empty).
func (d *Driver) Open(_ context.Context, p profile.Profile, database string, n dialect.Network) (*sql.DB, error) {
	cfg, err := config(p, database)
	if err != nil {
		return nil, err
	}
	conn := mssql.NewConnectorConfig(cfg)
	dial := n.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 15 * time.Second}
		dial = dialer.DialContext
	}
	// The host is resolved where the dialer is (on the other side of a tunnel).
	conn.Dialer = hostDialer{dial: dial, host: cfg.Host}
	db := sql.OpenDB(&loginConnector{conn})
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(45 * time.Second)
	return db, nil
}

type hostDialer struct {
	dial func(ctx context.Context, network, address string) (net.Conn, error)
	host string
}

func (h hostDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := h.dial(ctx, network, addr)
	if err == nil {
		if l, ok := ctx.Value(loginKey{}).(*login); ok {
			l.add(c)
		}
	}
	return c, err
}

type loginKey struct{}

// login holds the network connections opened while logging in.
type login struct {
	mu    sync.Mutex
	conns []net.Conn
}

func (l *login) add(c net.Conn) {
	l.mu.Lock()
	l.conns = append(l.conns, c)
	l.mu.Unlock()
}

func (l *login) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		_ = c.Close()
	}
}

// loginConnector bounds the login with the context: go-mssqldb only bounds the dial,
// and waits for the server's answer to the login forever (a frozen server hangs it).
type loginConnector struct {
	*mssql.Connector
}

func (c *loginConnector) Connect(ctx context.Context) (driver.Conn, error) {
	l := &login{}
	stop := context.AfterFunc(ctx, l.close)
	conn, err := c.Connector.Connect(context.WithValue(ctx, loginKey{}, l))
	if !stop() {
		if err == nil {
			_ = conn.Close()
		}
		return nil, fmt.Errorf("login: %w", ctx.Err())
	}
	return conn, err
}

func (h hostDialer) HostName() string { return h.host }

func config(p profile.Profile, database string) (msdsn.Config, error) {
	dsn := p.URI
	if dsn == "" {
		host := p.Host
		if host == "" {
			host = "localhost"
		}
		u := &url.URL{Scheme: "sqlserver", Host: host}
		if inst := strings.TrimSpace(p.Options["instance"]); inst != "" && p.Port == 0 {
			u.Path = "/" + inst
		} else {
			port := p.Port
			if port == 0 {
				port = 1433
			}
			u.Host = net.JoinHostPort(host, strconv.Itoa(port))
		}
		if p.User != "" {
			u.User = url.UserPassword(p.User, p.Password)
		}
		q, err := url.ParseQuery(strings.TrimLeft(strings.TrimSpace(p.Options["params"]), "?&"))
		if err != nil {
			return msdsn.Config{}, fmt.Errorf("invalid extra parameters: %w", err)
		}
		if p.Database != "" {
			q.Set("database", p.Database)
		}
		u.RawQuery = q.Encode()
		dsn = u.String()
	}
	cfg, err := msdsn.Parse(dsn)
	if err != nil {
		return cfg, fmt.Errorf("invalid connection settings: %w", err)
	}
	if database != "" {
		cfg.Database = database
	}
	if p.URI == "" {
		switch p.TLS.Mode {
		case profile.TLSDisable:
			cfg.Encryption, cfg.TLSConfig = msdsn.EncryptionDisabled, nil
		default:
			tc, err := dialect.TLSConfig(profile.TLS{Mode: tlsMode(p.TLS.Mode), UseAWSBundle: p.TLS.UseAWSBundle, CAFile: p.TLS.CAFile,
				CertFile: p.TLS.CertFile, KeyFile: p.TLS.KeyFile, ServerName: p.TLS.ServerName}, cfg.Host)
			if err != nil {
				return cfg, err
			}
			cfg.TLSConfig = tc
			cfg.Encryption = msdsn.EncryptionRequired
			if p.TLS.Mode == profile.TLSPrefer || p.TLS.Mode == "" {
				// Only the login is encrypted, unless the server asks for more.
				cfg.Encryption = msdsn.EncryptionOff
			}
			cfg.TrustServerCertificate = p.TLS.Mode != profile.TLSVerifyCA && p.TLS.Mode != profile.TLSVerifyFull
			cfg.HostInCertificateProvided = p.TLS.ServerName != ""
		}
	}
	if cfg.AppName == "" || cfg.AppName == "go-mssqldb" {
		cfg.AppName = "Quire"
	}
	if p.ReadOnly {
		cfg.ReadOnlyIntent = true
	}
	cfg.LogFlags |= msdsn.LogMessages | msdsn.LogRows
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 15 * time.Second
	}
	if cfg.KeepAlive == 0 {
		cfg.KeepAlive = 15 * time.Second
	}
	return cfg, nil
}

func tlsMode(m string) string {
	if m == "" {
		return profile.TLSPrefer
	}
	return m
}

// Version returns the server version.
func (d *Driver) Version(ctx context.Context, q dialect.Querier) (string, error) {
	var v, edition string
	err := q.QueryRowContext(ctx, `SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128)), CAST(SERVERPROPERTY('Edition') AS nvarchar(128))`).Scan(&v, &edition)
	if edition != "" {
		v += " " + strings.TrimSuffix(edition, " (64-bit)")
	}
	return v, err
}

// Current returns the database and the default schema of a new connection.
func (d *Driver) Current(ctx context.Context, q dialect.Querier) (string, string, error) {
	var db, schema sql.NullString
	err := q.QueryRowContext(ctx, "SELECT DB_NAME(), SCHEMA_NAME()").Scan(&db, &schema)
	return db.String, schema.String, err
}

// Transient reports network failures and server states worth retrying, including the
// transient errors of Azure SQL.
func (d *Driver) Transient(err error) bool {
	if err == nil {
		return false
	}
	var me mssql.Error
	if errors.As(err, &me) {
		switch me.Number {
		case 1205, // deadlock victim
			233, 10053, 10054, 10060, // transport errors
			4060, 40197, 40501, 40613, 49918, 49919, 49920, 4221, 10928, 10929: // Azure SQL
			return true
		}
		return false
	}
	return errors.Is(err, driver.ErrBadConn) || dialect.NetworkError(err)
}

// InTransaction reports whether the connection is inside a transaction.
func (d *Driver) InTransaction(ctx context.Context, c *sql.Conn) (bool, error) {
	var n int
	err := c.QueryRowContext(ctx, "SELECT @@TRANCOUNT").Scan(&n)
	return n > 0, err
}

// Quote quotes an identifier with brackets.
func (d *Driver) Quote(ident string) string { return dialect.QuoteWith(ident, '[', ']') }

// Placeholder is @pn.
func (d *Driver) Placeholder(n int) string { return "@p" + strconv.Itoa(n) }

var split = sqlsplit.Options{Brackets: true, GoBatches: true, NestedComments: true}

// orderedOnly reports a query with ORDER BY but without TOP or OFFSET: SQL Server
// refuses it in a derived table, so it is paged in place.
func orderedOnly(q string) bool {
	return dialect.TopLevel(q, split, "ORDER", "BY") && !dialect.TopLevel(q, split, "TOP") && !dialect.TopLevel(q, split, "OFFSET")
}

// Limit pages a query with OFFSET … FETCH.
func (d *Driver) Limit(q string, limit, offset int64) string {
	q = strings.TrimRight(strings.TrimSpace(q), ";")
	if orderedOnly(q) {
		return fmt.Sprintf("%s\nOFFSET %d ROWS FETCH NEXT %d ROWS ONLY", q, offset, limit)
	}
	return fmt.Sprintf("SELECT * FROM (\n%s\n) quire_q ORDER BY (SELECT NULL) OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", q, offset, limit)
}

// Count counts the rows of a query.
func (d *Driver) Count(q string) string {
	q = strings.TrimRight(strings.TrimSpace(q), ";")
	if orderedOnly(q) {
		q += "\nOFFSET 0 ROWS"
	}
	return dialect.CountQuery(q)
}

var selectPrefix = regexp.MustCompile(`(?i)^\s*SELECT(\s+DISTINCT)?\s+`)

// LimitRows adds TOP (n).
func (d *Driver) LimitRows(q string, limit int64) string {
	q = strings.TrimRight(strings.TrimSpace(q), ";")
	loc := selectPrefix.FindStringIndex(q)
	if loc == nil {
		return q
	}
	return q[:loc[1]] + "TOP (" + strconv.FormatInt(limit, 10) + ") " + q[loc[1]:]
}

// Split returns the T-SQL script rules: batches end at GO.
func (d *Driver) Split() sqlsplit.Options { return split }

// Use does nothing: connections are opened per database, and the default schema
// belongs to the user.
func (d *Driver) Use(_, _ string) []string { return nil }

// Explain shows the estimated plan.
func (d *Driver) Explain(stmt string) dialect.Plan {
	return dialect.Plan{Before: []string{"SET SHOWPLAN_ALL ON"}, Query: stmt, After: []string{"SET SHOWPLAN_ALL OFF"}}
}

// Upsert merges rows on the primary key.
func (d *Driver) Upsert(table string, cols, key []string) string {
	q := make([]string, len(cols))
	p := make([]string, len(cols))
	src := make([]string, len(cols))
	var set []string
	for i, c := range cols {
		q[i], p[i], src[i] = d.Quote(c), d.Placeholder(i+1), "quire_s."+d.Quote(c)
		isKey := false
		for _, k := range key {
			isKey = isKey || k == c
		}
		if !isKey {
			set = append(set, d.Quote(c)+" = quire_s."+d.Quote(c))
		}
	}
	on := make([]string, len(key))
	for i, k := range key {
		on[i] = "quire_t." + d.Quote(k) + " = quire_s." + d.Quote(k)
	}
	s := "MERGE INTO " + table + " WITH (HOLDLOCK) AS quire_t USING (VALUES (" + strings.Join(p, ", ") + ")) AS quire_s (" +
		strings.Join(q, ", ") + ") ON " + strings.Join(on, " AND ")
	if len(set) > 0 {
		s += " WHEN MATCHED THEN UPDATE SET " + strings.Join(set, ", ")
	}
	return s + " WHEN NOT MATCHED THEN INSERT (" + strings.Join(q, ", ") + ") VALUES (" + strings.Join(src, ", ") + ");"
}

// Qualify joins schema and name (connections are bound to one database).
func (d *Driver) Qualify(_, schema, name string) string { return dialect.Join(d.Quote, schema, name) }

// Plain reports whether ident needs no brackets.
func (d *Driver) Plain(ident string) bool { return plain.MatchString(ident) }

var plain = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// FormatValue renders uniqueidentifier values as GUIDs.
func (d *Driver) FormatValue(dbType string, v any) (string, bool) {
	if b, ok := v.([]byte); ok && dbType == "UNIQUEIDENTIFIER" && len(b) == 16 {
		var u mssql.UniqueIdentifier
		if u.Scan(b) == nil {
			return u.String(), true
		}
	}
	return "", false
}

// Literal writes booleans as 1/0, binary values as 0x…, and strings as N'…'.
func (d *Driver) Literal(kind, text string) (string, bool) {
	switch kind {
	case dialect.ValueBool:
		if b, ok := dialect.ParseBool(text); ok {
			return map[bool]string{true: "1", false: "0"}[b], true
		}
	case dialect.ValueBinary:
		if b, ok := dialect.HexBytes(text); ok {
			return "0x" + hex.EncodeToString(b), true
		}
	case dialect.ValueNumber:
		if dialect.IsNumber(text) {
			return text, true
		}
	}
	return "N" + dialect.QuoteString(text), true
}

// MaxParams is SQL Server's limit (2100), with room to spare.
func (d *Driver) MaxParams() int { return 2000 }

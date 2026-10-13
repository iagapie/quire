// Package oracle is the Oracle Database driver. By default it uses godror, which loads
// the Oracle Instant Client at run time (without it, connecting fails with
// instructions); the built-in client, go-ora, is written in Go and needs nothing
// installed.
package oracle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/godror/godror"
	"github.com/godror/godror/dsn"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/sqlsplit"
)

func init() {
	dialect.Register(&Driver{})
}

// Driver is the Oracle driver.
type Driver struct{}

// Info describes Oracle.
func (d *Driver) Info() dialect.Info {
	return dialect.Info{
		ID:              "oracle",
		Label:           "Oracle",
		Description:     "Oracle Database 12.2 and later, including Autonomous Database and Amazon RDS",
		DefaultPort:     1521,
		DefaultDatabase: "FREEPDB1",
		DatabaseField:   "Service name",
		Editor:          "PLSQL",
		Schemas:         true,
		SchemaLabel:     "Schema",
		Folders: []string{dialect.KindTable, dialect.KindView, dialect.KindMatView, dialect.KindFunction, dialect.KindProcedure,
			dialect.KindPackage, dialect.KindSequence, dialect.KindTrigger},
		Transactions: true,
		SSH:          true,
		Upsert:       true,
		Explain:      "Explain plan",
		Fields: []dialect.Field{
			{Key: "client", Label: "Client", Section: "server", Type: "select", Default: ClientInstant,
				Options: []dialect.Choice{{Value: ClientInstant, Label: "Oracle Instant Client (godror)"}, {Value: ClientBuiltin, Label: "Built-in (go-ora), nothing to install"}},
				Hint:    "The Instant Client is Oracle's own client and reads tnsnames.ora. The built-in client needs no installation; TNS aliases are not available with it."},
			{Key: "sid", Label: "The database is a SID, not a service name", Section: "server", Type: "bool",
				Hint: "Older databases are reached by SID; the Database field is a service name otherwise."},
			{Key: "role", Label: "Connect as", Section: "auth", Type: "select", Default: "",
				Options: []dialect.Choice{{Value: "", Label: "Normal"}, {Value: "SYSDBA", Label: "SYSDBA"}, {Value: "SYSOPER", Label: "SYSOPER"}, {Value: "SYSBACKUP", Label: "SYSBACKUP"}}},
			{Key: "libDir", Label: "Instant Client folder", Section: "advanced", Type: "text", Placeholder: "/opt/oracle/instantclient_23_3",
				Hint: "The folder holding the Oracle Instant Client libraries. Quire looks in the usual places when empty. Not used by the built-in client."},
			{Key: "configDir", Label: "Configuration folder (TNS_ADMIN)", Section: "advanced", Type: "text", Placeholder: "~/oracle/wallet",
				Hint: "Where tnsnames.ora, sqlnet.ora and wallets are, for TNS aliases and TCPS (Autonomous Database). The built-in client uses the wallet only."},
		},
		Index: dialect.IndexFeatures{
			Methods: []string{"normal", "bitmap"},
			Desc:    true, Expressions: true, Tablespace: true, Invisible: true, Editable: true,
		},
		Functions: functions,
		Snippets:  snippets,
		Note: "The Oracle Instant Client (Basic or Basic Light) must be installed on this computer, or choose the built-in client. " +
			"Read-only connections are enforced by Quire; for a guarantee on the server, use an account that can only read.",
		Order: 50,
	}
}

// Open creates a connection pool. Every database/sql connection is a standalone
// Oracle session.
func (d *Driver) Open(ctx context.Context, p profile.Profile, _ string, n dialect.Network) (*sql.DB, error) {
	if builtin(p) {
		return openBuiltin(p, n)
	}
	P, err := params(ctx, p, n)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(clientCheck{godror.NewConnector(P), P.LibDir})
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(45 * time.Second)
	return db, nil
}

func params(ctx context.Context, p profile.Profile, n dialect.Network) (dsn.ConnectionParams, error) {
	var P dsn.ConnectionParams
	if p.URI != "" {
		parsed, err := dsn.Parse(p.URI)
		if err != nil {
			return P, fmt.Errorf("invalid connection string: %w", err)
		}
		if n.Forward != nil {
			return P, errors.New("an SSH tunnel needs the host and port fields, not a connection string")
		}
		P = parsed
		if P.Username == "" {
			P.Username = p.User
		}
		if P.Password.IsZero() && p.Password != "" {
			P.Password = dsn.NewPassword(p.Password)
		}
	} else {
		host, port := p.Host, p.Port
		if host == "" {
			host = "localhost"
		}
		if port == 0 {
			port = 1521
		}
		if n.Forward != nil {
			var err error
			if host, port, err = n.Forward(ctx, host, port); err != nil {
				return P, err
			}
		}
		P.Username, P.Password = p.User, dsn.NewPassword(p.Password)
		P.ConnectString = connectString(host, port, p.Database, p.Options["sid"] == "true")
	}
	if role := p.Options["role"]; role != "" {
		P.AdminRole = dsn.AdminRole(strings.ToUpper(role))
	}
	P.LibDir = clientDir(p.Options["libDir"])
	if dir := expand(p.Options["configDir"]); dir != "" {
		P.ConfigDir = dir
	}
	// database/sql pools the connections; each one is a session of its own.
	P.StandaloneConnection = dsn.Bool(true)
	return P, nil
}

// connectString is an Easy Connect string, or a descriptor for a SID.
func connectString(host string, port int, database string, sid bool) string {
	if sid {
		return fmt.Sprintf("(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=%s)(PORT=%d))(CONNECT_DATA=(SID=%s)))", host, port, database)
	}
	s := host + ":" + strconv.Itoa(port)
	if database != "" {
		s += "/" + database
	}
	return s
}

func expand(path string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// clientLibrary is the Instant Client library file of this platform.
func clientLibrary() string {
	switch runtime.GOOS {
	case "darwin":
		return "libclntsh.dylib"
	case "windows":
		return "oci.dll"
	}
	return "libclntsh.so"
}

// clientDir returns the configured Instant Client folder, or the newest one found in
// the usual places, or "" to let the system loader search.
func clientDir(configured string) string {
	if dir := expand(configured); dir != "" {
		return dir
	}
	lib := clientLibrary()
	home, _ := os.UserHomeDir()
	var candidates []string
	if oh := os.Getenv("ORACLE_HOME"); oh != "" {
		candidates = append(candidates, oh, filepath.Join(oh, "lib"))
	}
	for _, pattern := range []string{
		filepath.Join(home, "Downloads", "instantclient_*"),
		filepath.Join(home, "instantclient_*"),
		filepath.Join(home, "oracle", "instantclient_*"),
		"/opt/oracle/instantclient_*",
		"/usr/lib/oracle/*/client64/lib",
		"/usr/local/oracle/instantclient_*",
	} {
		matches, _ := filepath.Glob(pattern)
		slices.Sort(matches)
		slices.Reverse(matches) // newest version first
		candidates = append(candidates, matches...)
	}
	candidates = append(candidates, filepath.Join(home, "lib"), "/opt/homebrew/lib", "/usr/local/lib")
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, lib)); err == nil { //nolint:gosec // G703: probing the user's own library folders
			return dir
		}
	}
	return ""
}

// ErrNoClient is returned when the Oracle Instant Client cannot be loaded.
var ErrNoClient = errors.New("the Oracle Instant Client was not found")

// clientCheck turns the errors of a missing or unusable Instant Client into
// instructions.
type clientCheck struct {
	driver.Connector
	libDir string
}

func (c clientCheck) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, clientError(err, c.libDir)
	}
	return conn, nil
}

func clientError(err error, libDir string) error {
	msg := err.Error()
	if !strings.Contains(msg, "DPI-1047") && !strings.Contains(msg, "DPI-1072") && !strings.Contains(msg, "DPI-1050") {
		return err
	}
	where := "in the usual places (~/Downloads/instantclient_*, /opt/oracle/instantclient_*, ~/lib)"
	if libDir != "" {
		where = "in " + libDir
	}
	arch := map[string]string{"arm64": "ARM64", "amd64": "x86-64"}[runtime.GOARCH]
	osName := map[string]string{"darwin": "macOS", "linux": "Linux", "windows": "Windows"}[runtime.GOOS]
	return fmt.Errorf("%w %s. Download Instant Client Basic or Basic Light for %s %s from "+
		"https://www.oracle.com/database/technologies/instant-client/downloads.html, unpack it (for example into "+
		"/opt/oracle/instantclient_23), then set that folder in the connection's Advanced → Instant Client folder. "+
		"Or choose Server → Client → Built-in, which needs nothing installed",
		ErrNoClient, where, osName, arch)
}

// Version returns the server version.
func (d *Driver) Version(ctx context.Context, q dialect.Querier) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT version_full FROM product_component_version WHERE product LIKE 'Oracle%' AND ROWNUM = 1`).Scan(&v)
	if err != nil {
		err = q.QueryRowContext(ctx, `SELECT version FROM product_component_version WHERE product LIKE 'Oracle%' AND ROWNUM = 1`).Scan(&v)
	}
	return v, err
}

// Current returns the database name and the current schema.
func (d *Driver) Current(ctx context.Context, q dialect.Querier) (string, string, error) {
	var db, schema sql.NullString
	err := q.QueryRowContext(ctx, `SELECT SYS_CONTEXT('USERENV', 'DB_NAME'), SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM dual`).Scan(&db, &schema)
	return db.String, schema.String, err
}

// Transient reports network failures and server states worth retrying.
func (d *Driver) Transient(err error) bool {
	if err == nil || errors.Is(err, ErrNoClient) {
		return false
	}
	if oe, ok := godror.AsOraErr(err); ok {
		return transientCode(oe.Code()) || oe.Recoverable()
	}
	if transient, known := builtinTransient(err); known {
		return transient
	}
	msg := err.Error()
	for _, code := range []string{"DPI-1010", "DPI-1080", "DPI-1067"} { // not connected, closed, call timeout
		if strings.Contains(msg, code) {
			return true
		}
	}
	return errors.Is(err, driver.ErrBadConn) || dialect.NetworkError(err)
}

// transientCode reports ORA- codes of lost connections and server states worth
// retrying.
func transientCode(code int) bool {
	switch code {
	case 60, // deadlock
		1033, 1034, 1089, 1090, 1092, // starting up, not available, shutting down
		3113, 3114, 3135, 3136, 3156, // end of file on channel, not connected, connection lost
		8177, // cannot serialize access
		12150, 12152, 12153, 12157, 12170, 12500, 12514, 12516, 12519, 12520, 12521, 12526, 12527, 12528,
		12537, 12541, 12543, 12547, 12560, 12570, 12571, 12582, // TNS
		25402, 25403, 25404, 25407, 25408, 25409: // TAF
		return true
	}
	return false
}

// EnableOutput turns DBMS_OUTPUT on for the session.
func (d *Driver) EnableOutput(ctx context.Context, c *sql.Conn) error {
	if isBuiltin(c) {
		_, err := c.ExecContext(ctx, `BEGIN DBMS_OUTPUT.ENABLE(NULL); END;`)
		return err
	}
	return godror.EnableDbmsOutput(ctx, c)
}

// ReadOutput returns the lines printed with DBMS_OUTPUT.
func (d *Driver) ReadOutput(ctx context.Context, c *sql.Conn) ([]string, error) {
	if isBuiltin(c) {
		return readOutputPLSQL(ctx, c)
	}
	var b strings.Builder
	if err := godror.ReadDbmsOutput(ctx, &b, c); err != nil {
		return nil, err
	}
	text := strings.TrimRight(b.String(), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

// Quote quotes an identifier.
func (d *Driver) Quote(ident string) string { return dialect.QuoteDouble(ident) }

// Placeholder is :n.
func (d *Driver) Placeholder(n int) string { return ":" + strconv.Itoa(n) }

// Limit pages a query with OFFSET … FETCH (12c and later).
func (d *Driver) Limit(q string, limit, offset int64) string {
	return dialect.WrapOffsetFetch(q, limit, offset)
}

// TableRowLimit is 100 for Oracle: opening a table reads FETCH FIRST 100 ROWS ONLY.
func (d *Driver) TableRowLimit() int64 { return 100 }

// LimitRows adds FETCH FIRST n ROWS ONLY.
func (d *Driver) LimitRows(q string, limit int64) string {
	return strings.TrimRight(strings.TrimSpace(q), ";") + "\nFETCH FIRST " + strconv.FormatInt(limit, 10) + " ROWS ONLY"
}

// Split returns the Oracle script rules: PL/SQL blocks end with "/".
func (d *Driver) Split() sqlsplit.Options {
	return sqlsplit.Options{SlashTerminator: true, QQuotes: true}
}

// Use sets the schema unqualified names resolve in.
func (d *Driver) Use(_, schema string) []string {
	if schema == "" {
		return nil
	}
	return []string{"ALTER SESSION SET CURRENT_SCHEMA = " + d.Quote(schema)}
}

// Explain stores the plan in PLAN_TABLE and shows it with DBMS_XPLAN.
func (d *Driver) Explain(stmt string) dialect.Plan {
	return dialect.Plan{
		Before: []string{"EXPLAIN PLAN FOR " + strings.TrimRight(strings.TrimSpace(stmt), ";")},
		Query:  "SELECT plan_table_output AS plan FROM TABLE(DBMS_XPLAN.DISPLAY())",
	}
}

// Upsert merges rows on the primary key.
func (d *Driver) Upsert(table string, cols, key []string) string {
	sel := make([]string, len(cols))
	q := make([]string, len(cols))
	src := make([]string, len(cols))
	var set []string
	for i, c := range cols {
		sel[i] = d.Placeholder(i+1) + " AS " + d.Quote(c)
		q[i], src[i] = d.Quote(c), "quire_s."+d.Quote(c)
		if !slices.Contains(key, c) {
			set = append(set, "quire_t."+d.Quote(c)+" = quire_s."+d.Quote(c))
		}
	}
	on := make([]string, len(key))
	for i, k := range key {
		on[i] = "quire_t." + d.Quote(k) + " = quire_s." + d.Quote(k)
	}
	s := "MERGE INTO " + table + " quire_t USING (SELECT " + strings.Join(sel, ", ") + " FROM dual) quire_s ON (" + strings.Join(on, " AND ") + ")"
	if len(set) > 0 {
		s += " WHEN MATCHED THEN UPDATE SET " + strings.Join(set, ", ")
	}
	return s + " WHEN NOT MATCHED THEN INSERT (" + strings.Join(q, ", ") + ") VALUES (" + strings.Join(src, ", ") + ")"
}

// Qualify joins schema and name.
func (d *Driver) Qualify(_, schema, name string) string { return dialect.Join(d.Quote, schema, name) }

// Fold upper-cases unquoted identifiers, as Oracle does.
func (d *Driver) Fold(ident string) string { return strings.ToUpper(ident) }

// Plain reports whether ident needs no quotes.
func (d *Driver) Plain(ident string) bool { return plainUpper.MatchString(ident) }

var plainUpper = regexp.MustCompile(`^[A-Z][A-Z0-9_$#]*$`)

// Literal writes booleans as 1/0, binary values with HEXTORAW and dates with
// TO_TIMESTAMP, so the script does not depend on the session's date format.
func (d *Driver) Literal(kind, text string) (string, bool) {
	switch kind {
	case dialect.ValueBool:
		if b, ok := dialect.ParseBool(text); ok {
			return map[bool]string{true: "1", false: "0"}[b], true
		}
	case dialect.ValueBinary:
		if b, ok := dialect.HexBytes(text); ok {
			return "HEXTORAW('" + strings.ToUpper(hex.EncodeToString(b)) + "')", true
		}
	case dialect.ValueDateTime:
		if t, ok := parseTime(text); ok {
			return "TO_TIMESTAMP('" + t.Format("2006-01-02 15:04:05.000000") + "', 'YYYY-MM-DD HH24:MI:SS.FF6')", true
		}
	}
	return "", false
}

// Param passes dates as time values (Oracle would read text with the session's
// NLS_DATE_FORMAT) and binary values as bytes (RAW columns do not take text).
func (d *Driver) Param(kind, _, text string) (any, bool) {
	switch kind {
	case dialect.ValueDateTime:
		if t, ok := parseTime(text); ok {
			return t, true
		}
	case dialect.ValueBinary:
		if b, ok := dialect.HexBytes(text); ok {
			return b, true
		}
	}
	return nil, false
}

var timeLayouts = []string{"2006-01-02 15:04:05.999999999Z07:00", time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999", "2006-01-02"}

func parseTime(s string) (time.Time, bool) {
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, strings.TrimSpace(s)); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// MaxParams is 0: Oracle has no multi-row VALUES, rows are inserted one by one.
func (d *Driver) MaxParams() int { return 0 }

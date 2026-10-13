// Package dialect defines what the application needs to know about each kind of
// database, and keeps the registry of drivers. A driver lives in its own package and
// registers itself from init; adding a database means adding one package (imported
// by internal/dialect/all).
package dialect

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"slices"
	"sync"

	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/sqlsplit"
)

// Info describes a database kind to the UI.
type Info struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Description is a short subtitle in the connection dialog.
	Description string `json:"description"`
	DefaultPort int    `json:"defaultPort"`
	// DefaultUser is suggested for new connections.
	DefaultUser string `json:"defaultUser"`
	// DefaultDatabase is suggested for new connections.
	DefaultDatabase string `json:"defaultDatabase"`
	// Editor is the CodeMirror SQL dialect: PostgreSQL, MySQL, MariaSQL, MSSQL, SQLite,
	// PLSQL, StandardSQL or Cosmos.
	Editor string `json:"editor"`
	// Document databases return JSON documents instead of rows (Cosmos DB).
	Document bool `json:"document"`
	// Shell databases run mongo shell JavaScript instead of SQL (MongoDB, DocumentDB).
	// They are not SQLDrivers: their sessions are opened by internal/docsession.
	Shell bool `json:"shell"`
	// File databases open a local file instead of a server (SQLite).
	File bool `json:"file"`
	// Databases and Schemas tell which levels the object tree has, and how to call them.
	Databases     bool   `json:"databases"`
	Schemas       bool   `json:"schemas"`
	DatabaseLabel string `json:"databaseLabel"`
	SchemaLabel   string `json:"schemaLabel"`
	// HostLabel and HostPlaceholder describe the host setting (an endpoint URL for
	// Cosmos DB); NoPort hides the port, NoUser the user name, and PasswordLabel names
	// the secret (an account key).
	HostLabel       string `json:"hostLabel"`
	HostPlaceholder string `json:"hostPlaceholder"`
	NoPort          bool   `json:"noPort"`
	NoUser          bool   `json:"noUser"`
	PasswordLabel   string `json:"passwordLabel"`
	// URIPlaceholder shows the connection string format.
	URIPlaceholder string `json:"uriPlaceholder"`
	// QueryOnly databases only run queries: no editing, no read-only switch.
	QueryOnly bool `json:"queryOnly"`
	// PartitionKey shows a partition key setting for queries (Cosmos DB).
	PartitionKey bool `json:"partitionKey"`
	// DatabaseField labels the database setting of the connection dialog, when it is
	// not a default database (an Oracle service name).
	DatabaseField string `json:"databaseField"`
	// Folders lists the object kinds shown under a schema, in order.
	Folders []string `json:"folders"`
	// Transactions tells whether manual transactions are available.
	Transactions bool `json:"transactions"`
	// TransactionalDDL means CREATE/DROP INDEX can run inside a transaction.
	TransactionalDDL bool `json:"transactionalDdl"`
	// TLS lists the TLS modes the driver supports (empty: no TLS settings).
	TLS []string `json:"tls"`
	// TLSDefault is the TLS mode of new connections (empty: prefer, or the first mode).
	TLSDefault string `json:"tlsDefault"`
	// AWSBundle makes new connections trust the Amazon CA bundle (DocumentDB).
	AWSBundle bool `json:"awsBundle"`
	// SSH tells whether the connection can go through an SSH tunnel.
	SSH bool `json:"ssh"`
	// Fields are driver-specific connection settings.
	Fields []Field `json:"fields"`
	// Index describes the index editor options.
	Index IndexFeatures `json:"index"`
	// Upsert tells whether imports can upsert on the primary key.
	Upsert bool `json:"upsert"`
	// Explain is the label of the explain action, or empty when not available.
	Explain string `json:"explain"`
	// Keywords and Functions extend the editor completions.
	Keywords  []string `json:"keywords"`
	Functions []string `json:"functions"`
	// Snippets are completion templates (label → text with ${} placeholders).
	Snippets []Snippet `json:"snippets"`
	// Split are the script splitting rules, for the editor (set from Syntax.Split).
	Split *sqlsplit.Options `json:"split"`
	// Note is shown in the connection dialog (e.g. client library requirements).
	Note string `json:"note"`
	// Order sorts the database list in the UI.
	Order int `json:"-"`
}

// Snippet is a completion template.
type Snippet struct {
	Label  string `json:"label"`
	Detail string `json:"detail"`
	Body   string `json:"body"`
}

// Field is a driver-specific connection setting, rendered by the connection dialog and
// stored in Profile.Options (or SecretOptions when Secret).
type Field struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Hint    string `json:"hint"`
	Section string `json:"section"` // server | auth | tls | advanced
	// Type is text, number, password, bool, select or file.
	Type        string   `json:"type"`
	Options     []Choice `json:"options"`
	Default     string   `json:"default"`
	Placeholder string   `json:"placeholder"`
	Secret      bool     `json:"secret"`
}

// Choice is an option of a select field.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Object kinds, also the folder names of the tree.
const (
	KindTable     = "table"
	KindView      = "view"
	KindMatView   = "matview"
	KindFunction  = "function"
	KindProcedure = "procedure"
	KindSequence  = "sequence"
	KindTrigger   = "trigger"
	KindPackage   = "package"
	KindContainer = "container"
)

// Driver is a registered database kind.
type Driver interface {
	Info() Info
}

// Network is how a driver reaches the server.
type Network struct {
	// Dial dials the server, through the SSH tunnel when one is configured. Drivers
	// that accept a dialer use it; it is nil without a tunnel.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// Forward opens a local port forwarding to host:port through the SSH tunnel and
	// returns the local address, for drivers that cannot use Dial. It is nil without a
	// tunnel.
	Forward func(ctx context.Context, host string, port int) (localHost string, localPort int, err error)
}

// Querier runs statements: a *sql.DB, *sql.Conn or *sql.Tx.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SQLDriver is a database reached through database/sql.
type SQLDriver interface {
	Driver
	Syntax
	Catalog
	// Open creates a connection pool to database (the profile default when empty). The
	// pool applies the profile read-only mode on every connection where the server
	// supports it.
	Open(ctx context.Context, p profile.Profile, database string, n Network) (*sql.DB, error)
	// Version returns the server version for display.
	Version(ctx context.Context, q Querier) (string, error)
	// Current returns the database and schema a new connection starts in.
	Current(ctx context.Context, q Querier) (database, schema string, err error)
	// Transient reports whether err is a network-level failure worth retrying.
	Transient(err error) bool
}

// PerDatabase is implemented by drivers whose connections are bound to one database
// (PostgreSQL), so a pool is opened per database.
type PerDatabase interface {
	PerDatabasePools() bool
}

// Folder is implemented by drivers that fold unquoted identifiers to one case
// (PostgreSQL to lower, Oracle to upper).
type Folder interface {
	Fold(ident string) string
}

// Fold applies the driver's case folding to an unquoted identifier.
func Fold(d any, ident string, quoted bool) string {
	if f, ok := d.(Folder); ok && !quoted {
		return f.Fold(ident)
	}
	return ident
}

// Stateless is implemented by drivers whose connections keep no server session (HTTP
// APIs): they are not checked before use or after a cancelled statement.
type Stateless interface {
	Stateless() bool
}

// IsStateless reports whether d is a stateless driver.
func IsStateless(d any) bool {
	s, ok := d.(Stateless)
	return ok && s.Stateless()
}

// Partitioned is implemented by drivers whose queries can target one partition
// (Cosmos DB): the statements make key (JSON; empty for all partitions) the partition
// of the connection.
type Partitioned interface {
	UsePartition(key string) []string
}

// Generator is implemented by drivers that write the statement templates of the tree
// menus themselves (kind is select, insert, update or delete).
type Generator interface {
	GenerateSQL(table string, cols []Column, kind string) (string, error)
}

// TableOpener is implemented by drivers that write the query opening a table
// themselves (Cosmos DB names the properties and needs an alias).
type TableOpener interface {
	OpenTable(table string, cols []Column, limit int64) string
}

// TableLimiter is implemented by drivers whose query opening a table reads another
// number of rows than the default.
type TableLimiter interface {
	TableRowLimit() int64
}

// TxState is implemented by drivers that can tell whether a connection is inside a
// transaction started with a statement (BEGIN in a script).
type TxState interface {
	InTransaction(ctx context.Context, c *sql.Conn) (bool, error)
}

// Syntax is the SQL text a driver generates.
type Syntax interface {
	// Quote quotes an identifier.
	Quote(ident string) string
	// Placeholder is the n-th (1-based) bind parameter.
	Placeholder(n int) string
	// Limit wraps a query to return limit rows from offset, or returns "" when the
	// database cannot page an arbitrary query (the rows are then skipped).
	Limit(query string, limit, offset int64) string
	// LimitRows adds the native row limit to a simple SELECT (LIMIT n, TOP n, FETCH
	// FIRST n ROWS ONLY), keeping it a single-table query.
	LimitRows(selectSQL string, limit int64) string
	// Split are the script splitting rules.
	Split() sqlsplit.Options
	// Use returns the statements that make database/schema current on a connection.
	Use(database, schema string) []string
	// Explain returns how to show the plan of stmt; Query is empty when unsupported.
	Explain(stmt string) Plan
	// Upsert returns an INSERT that replaces rows with the same key, or "" when
	// unsupported. cols are the inserted columns, key the primary key columns.
	Upsert(table string, cols, key []string) string
	// Qualify joins the parts of an object name (empty parts are skipped).
	Qualify(database, schema, name string) string
}

// Catalog reads metadata. Drivers with per-database pools get the pool of database.
type Catalog interface {
	Databases(ctx context.Context, q Querier) ([]string, error)
	Schemas(ctx context.Context, q Querier, database string) ([]string, error)
	Objects(ctx context.Context, q Querier, database, schema string) ([]Object, error)
	Columns(ctx context.Context, q Querier, database, schema, table string) ([]Column, error)
	Indexes(ctx context.Context, q Querier, database, schema, table string) ([]Index, error)
	Constraints(ctx context.Context, q Querier, database, schema, table string) ([]Constraint, error)
	DDL(ctx context.Context, q Querier, database, schema string, obj Object) (string, error)
	TableStats(ctx context.Context, q Querier, database, schema string) ([]TableStat, error)
}

// Object is a table, view, routine... in a schema.
type Object struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Comment string `json:"comment,omitempty"`
	// Detail is extra text shown in the tree (e.g. a routine signature or a partition key).
	Detail string `json:"detail,omitempty"`
	// Table is the table a trigger belongs to.
	Table string `json:"table,omitempty"`
	// ID identifies overloaded routines (e.g. a PostgreSQL oid).
	ID string `json:"id,omitempty"`
}

// Column describes a table column.
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	Default  string `json:"default,omitempty"`
	// PK is the position in the primary key (1-based), 0 when not part of it.
	PK      int    `json:"pk"`
	Comment string `json:"comment,omitempty"`
	// Generated tells the column is computed or an identity that cannot be written.
	Generated bool `json:"generated,omitempty"`
}

// IndexColumn is a key part of an index: a column or an expression.
type IndexColumn struct {
	Name string `json:"name"`
	// Expression is set for expression keys; Name is then empty.
	Expression string `json:"expression,omitempty"`
	Desc       bool   `json:"desc"`
	// Nulls is "", "first" or "last".
	Nulls     string `json:"nulls,omitempty"`
	Collation string `json:"collation,omitempty"`
	// OpClass is a PostgreSQL operator class.
	OpClass string `json:"opclass,omitempty"`
	// Length is a MySQL prefix length.
	Length int `json:"length,omitempty"`
}

// Index describes an index.
type Index struct {
	Name    string        `json:"name"`
	Table   string        `json:"table"`
	Columns []IndexColumn `json:"columns"`
	Unique  bool          `json:"unique"`
	Primary bool          `json:"primary"`
	// Method is the access method (btree, hash, gin...).
	Method string `json:"method,omitempty"`
	// Where is the predicate of a partial index.
	Where string `json:"where,omitempty"`
	// Include lists covering columns.
	Include    []string `json:"include,omitempty"`
	Tablespace string   `json:"tablespace,omitempty"`
	FillFactor int      `json:"fillFactor,omitempty"`
	Invisible  bool     `json:"invisible,omitempty"`
	// Clustered is a SQL Server clustered index.
	Clustered bool `json:"clustered,omitempty"`
	// Definition is the server's own text of the index, when available.
	Definition string `json:"definition,omitempty"`
	// SizeBytes is -1 when unknown.
	SizeBytes int64 `json:"sizeBytes"`
	// Constraint is set when the index backs a constraint (primary key, unique).
	Constraint string `json:"constraint,omitempty"`
}

// IndexFeatures tells the index editor which options a driver supports.
type IndexFeatures struct {
	Methods      []string `json:"methods"`
	Desc         bool     `json:"desc"`
	Nulls        bool     `json:"nulls"`
	Partial      bool     `json:"partial"`
	Include      bool     `json:"include"`
	Concurrently bool     `json:"concurrently"`
	Tablespace   bool     `json:"tablespace"`
	FillFactor   bool     `json:"fillFactor"`
	Invisible    bool     `json:"invisible"`
	Collation    bool     `json:"collation"`
	Expressions  bool     `json:"expressions"`
	Length       bool     `json:"length"`
	Clustered    bool     `json:"clustered"`
	IfNotExists  bool     `json:"ifNotExists"`
	// Editable is false when the driver cannot change indexes (Cosmos DB).
	Editable bool `json:"editable"`
}

// Constraint describes a table constraint.
type Constraint struct {
	Name string `json:"name"`
	// Type is PRIMARY KEY, UNIQUE, FOREIGN KEY or CHECK.
	Type       string   `json:"type"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema,omitempty"`
	RefTable   string   `json:"refTable,omitempty"`
	RefColumns []string `json:"refColumns,omitempty"`
	OnUpdate   string   `json:"onUpdate,omitempty"`
	OnDelete   string   `json:"onDelete,omitempty"`
	Definition string   `json:"definition,omitempty"`
}

// TableStat is the estimated size of a table, for the export dialog.
type TableStat struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Rows and Bytes are -1 when unknown.
	Rows  int64 `json:"rows"`
	Bytes int64 `json:"bytes"`
}

// IndexWriter generates index DDL.
type IndexWriter interface {
	// CreateIndex returns the statement creating ix on schema.table.
	CreateIndex(database, schema string, ix Index, concurrently, ifNotExists bool) (string, error)
	// DropIndex returns the statement dropping index name of schema.table.
	DropIndex(database, schema, table, name string, concurrently bool) string
}

// ErrUnsupported is returned for operations a driver does not support.
var ErrUnsupported = errors.New("not supported for this database")

var (
	mu      sync.RWMutex
	drivers = map[string]Driver{}
)

// Register adds a driver; it panics on a duplicate id.
func Register(d Driver) {
	mu.Lock()
	defer mu.Unlock()
	id := d.Info().ID
	if _, dup := drivers[id]; dup {
		panic("dialect: duplicate driver " + id)
	}
	drivers[id] = d
}

// Get returns the driver with the given id.
func Get(id string) (Driver, bool) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := drivers[id]
	return d, ok
}

// All returns the registered drivers, in display order.
func All() []Driver {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Driver, 0, len(drivers))
	for _, d := range drivers {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Driver) int {
		if a.Info().Order != b.Info().Order {
			return a.Info().Order - b.Info().Order
		}
		if a.Info().Label < b.Info().Label {
			return -1
		}
		return 1
	})
	return out
}

// SchemaColumns is implemented by drivers that list every column of a schema in one
// query (for completion): table name → column names, in order.
type SchemaColumns interface {
	SchemaColumns(ctx context.Context, q Querier, database, schema string) (map[string][]string, error)
}

// ForeignKey links columns of a table to the key of another, for join completion.
type ForeignKey struct {
	Table      string   `json:"table"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema,omitempty"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
}

// ForeignKeys is implemented by drivers that list the foreign keys of a schema in one
// query.
type ForeignKeys interface {
	ForeignKeys(ctx context.Context, q Querier, database, schema string) ([]ForeignKey, error)
}

// ColumnPairs reads rows of (table, column) into a map, keeping the column order.
func ColumnPairs(ctx context.Context, q Querier, query string, args ...any) (map[string][]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var t, c string
		if err := rows.Scan(&t, &c); err != nil {
			return nil, err
		}
		out[t] = append(out[t], c)
	}
	return out, rows.Err()
}

// KeyRows reads rows of (constraint, table, column, ref schema, ref table, ref column),
// ordered by constraint and position, into foreign keys.
func KeyRows(ctx context.Context, q Querier, query string, args ...any) ([]ForeignKey, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ForeignKey
	last := ""
	for rows.Next() {
		var name, table, col, refTable, refCol string
		var refSchema sql.NullString
		if err := rows.Scan(&name, &table, &col, &refSchema, &refTable, &refCol); err != nil {
			return nil, err
		}
		key := table + "\x00" + name
		if key != last {
			out = append(out, ForeignKey{Table: table, RefSchema: refSchema.String, RefTable: refTable})
			last = key
		}
		fk := &out[len(out)-1]
		fk.Columns = append(fk.Columns, col)
		fk.RefColumns = append(fk.RefColumns, refCol)
	}
	return out, rows.Err()
}

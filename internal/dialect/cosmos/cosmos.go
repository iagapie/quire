// Package cosmos is the Azure Cosmos DB (NoSQL API) driver, built on azcosmos. It
// runs queries only.
//
// The driver is a database/sql driver of its own (see conn.go), so the editor gets the
// same cancellable operations, timeouts, retries and paging from an open cursor as the
// SQL databases: the cursor is the query's continuation.
//
// The container of a query is the name in its FROM clause when a container has that
// name ("SELECT * FROM orders o"), else the container chosen with USE, else the only
// container of the database. Names that are not identifiers are written ["orders-2024"].
//
// Without a partition key, queries span every partition. From this client Cosmos DB
// serves those only when they are simple (filters and projections); ORDER BY, TOP,
// OFFSET/LIMIT, DISTINCT, GROUP BY and aggregates need a partition key, set with the
// toolbar or with SET PARTITION KEY.
package cosmos

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/sqlsplit"
)

func init() {
	dialect.Register(&Driver{})
}

// Driver is the Cosmos DB driver.
type Driver struct{}

// EmulatorKey is the well-known key of the Cosmos DB emulator.
const EmulatorKey = "C2y6yDjf5/R+ob0N8A7Cgv30VRDJIWEHLM+4QDU5DE2nQ9nDuVTqobD4b8mGGyPMbIZnqyMsEcaGQy67XIw/Jw=="

// Info describes Cosmos DB.
func (d *Driver) Info() dialect.Info {
	return dialect.Info{
		ID:              "cosmos",
		Label:           "Cosmos DB",
		Description:     "Azure Cosmos DB for NoSQL, and the Cosmos DB emulator: SELECT queries",
		Editor:          "Cosmos",
		Document:        true,
		Databases:       true,
		DatabaseLabel:   "Database",
		Folders:         []string{dialect.KindContainer},
		SSH:             true,
		HostLabel:       "Account endpoint",
		HostPlaceholder: "https://myaccount.documents.azure.com:443/",
		NoPort:          true,
		NoUser:          true,
		PasswordLabel:   "Account key",
		URIPlaceholder:  "AccountEndpoint=https://myaccount.documents.azure.com:443/;AccountKey=…;",
		QueryOnly:       true,
		PartitionKey:    true,
		Fields: []dialect.Field{
			{Key: "insecure", Label: "Accept any certificate", Section: "advanced", Type: "bool",
				Hint: "For the emulator over HTTPS, whose certificate is self-signed."},
		},
		Index:     dialect.IndexFeatures{},
		Keywords:  keywords,
		Functions: functions,
		Snippets:  snippets,
		Note: "Queries only. Queries across partitions can only filter and project; ORDER BY, TOP, OFFSET/LIMIT, " +
			"DISTINCT, GROUP BY and aggregates need a partition key (toolbar, or SET PARTITION KEY 'value').",
		Order: 60,
	}
}

// Stateless reports that connections keep no server session (requests are HTTP):
// there is nothing to check or lose on them.
func (d *Driver) Stateless() bool { return true }

// PerDatabasePools reports that a pool reaches one database.
func (d *Driver) PerDatabasePools() bool { return true }

// Open creates a pool of (stateless) connections to database.
func (d *Driver) Open(_ context.Context, p profile.Profile, database string, n dialect.Network) (*sql.DB, error) {
	c, err := newConnector(p, database, n)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(8)
	return db, nil
}

// Version names the service.
func (d *Driver) Version(ctx context.Context, q dialect.Querier) (string, error) {
	return "NoSQL API", nil
}

// Current returns the profile database, or the first one.
func (d *Driver) Current(ctx context.Context, q dialect.Querier) (string, string, error) {
	var db string
	err := withConn(ctx, q, func(c *conn) error {
		db = c.c.database
		if db != "" {
			return nil
		}
		dbs, err := c.c.databases(ctx)
		if len(dbs) > 0 {
			db = dbs[0]
		}
		return err
	})
	return db, "", err
}

// Transient reports network failures, throttling and unavailable services.
func (d *Driver) Transient(err error) bool {
	if err == nil {
		return false
	}
	var re *azcore.ResponseError
	if errors.As(err, &re) {
		switch re.StatusCode {
		case http.StatusRequestTimeout, http.StatusGone, http.StatusTooManyRequests, 449, // 449: retry with
			http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		return false
	}
	return errors.Is(err, driver.ErrBadConn) || dialect.NetworkError(err)
}

var plain = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Quote writes a name that is not an identifier as ["name"].
func (d *Driver) Quote(ident string) string {
	if plain.MatchString(ident) && !keyword[strings.ToUpper(ident)] {
		return ident
	}
	return `["` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(ident) + `"]`
}

// Plain reports whether ident needs no quoting.
func (d *Driver) Plain(ident string) bool {
	return plain.MatchString(ident) && !keyword[strings.ToUpper(ident)]
}

// Placeholder is @pn.
func (d *Driver) Placeholder(n int) string { return "@p" + strconv.Itoa(n) }

// Limit returns "": across partitions, OFFSET/LIMIT is not served, so pages are
// rebuilt by skipping rows.
func (d *Driver) Limit(string, int64, int64) string { return "" }

// LimitRows leaves the query as it is: TOP needs a partition key, and results are
// read page by page anyway.
func (d *Driver) LimitRows(q string, _ int64) string {
	return strings.TrimRight(strings.TrimSpace(q), ";")
}

// Count counts the rows of a query (see conn.count).
func (d *Driver) Count(q string) string { return countPrefix + q }

// Split returns the script rules.
func (d *Driver) Split() sqlsplit.Options { return sqlsplit.Options{} }

// Use does nothing: pools are per database, and the container comes from the query.
func (d *Driver) Use(string, string) []string { return nil }

// UsePartition makes key (JSON) the partition of the connection's queries.
func (d *Driver) UsePartition(key string) []string {
	if strings.TrimSpace(key) == "" {
		return []string{"SET PARTITION KEY NONE"}
	}
	return []string{"SET PARTITION KEY " + key}
}

// Explain is not available.
func (d *Driver) Explain(string) dialect.Plan { return dialect.Plan{} }

// Upsert is not available.
func (d *Driver) Upsert(string, []string, []string) string { return "" }

// Qualify is the container name: the database is the pool's.
func (d *Driver) Qualify(_, _, name string) string { return d.Quote(name) }

// GenerateSQL writes a SELECT of the container; Cosmos DB connections only query.
func (d *Driver) GenerateSQL(table string, cols []dialect.Column, kind string) (string, error) {
	if kind != "select" {
		return "", errors.New("connections to Cosmos DB run queries only")
	}
	return "SELECT " + projection(cols) + "\nFROM " + table + " c\nWHERE ", nil
}

// OpenTable reads the first documents of a container, with its top-level
// properties: SELECT TOP 1000 c.id, c.name FROM orders c. Without a partition key
// the TOP is applied by Quire (see clientTop).
func (d *Driver) OpenTable(table string, cols []dialect.Column, limit int64) string {
	return "SELECT TOP " + strconv.FormatInt(limit, 10) + " " + projection(cols) + "\nFROM " + table + " c"
}

// projection lists the top-level properties seen in the sample (system properties
// left out), or * when there are none.
func projection(cols []dialect.Column) string {
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		if strings.HasPrefix(c.Name, "_") || strings.Contains(c.Name, ".") {
			continue
		}
		parts = append(parts, "c"+propertyPath(c.Name))
	}
	if len(parts) == 0 {
		return "*"
	}
	return strings.Join(parts, ", ")
}

// propertyPath is .name, or ["name"] for names that are not identifiers.
func propertyPath(name string) string {
	if plain.MatchString(name) {
		return "." + name
	}
	return `["` + strings.ReplaceAll(name, `"`, `\"`) + `"]`
}

var keyword = map[string]bool{}

func init() {
	for _, k := range keywords {
		keyword[k] = true
	}
}

var keywords = []string{
	"SELECT", "VALUE", "TOP", "DISTINCT", "FROM", "IN", "JOIN", "WHERE", "AND", "OR", "NOT", "BETWEEN", "LIKE", "ESCAPE",
	"ORDER", "BY", "ASC", "DESC", "GROUP", "OFFSET", "LIMIT", "AS", "ROOT", "TRUE", "FALSE", "NULL", "UNDEFINED",
	"EXISTS", "ARRAY", "IS", "USE", "SET", "PARTITION", "KEY", "NONE",
}

var functions = []string{
	"ABS", "ARRAY_CONCAT", "ARRAY_CONTAINS", "ARRAY_LENGTH", "ARRAY_SLICE", "AVG", "CEILING", "CONCAT", "CONTAINS",
	"COUNT", "DateTimeAdd", "DateTimeDiff", "DateTimePart", "ENDSWITH", "FLOOR", "GetCurrentDateTime",
	"GetCurrentTimestamp", "INDEX_OF", "IS_ARRAY", "IS_BOOL", "IS_DEFINED", "IS_NULL", "IS_NUMBER", "IS_OBJECT",
	"IS_STRING", "LEFT", "LENGTH", "LOWER", "LTRIM", "MAX", "MIN", "REGEXMATCH", "REPLACE", "REPLICATE", "REVERSE",
	"RIGHT", "ROUND", "RTRIM", "STARTSWITH", "StringEquals", "StringToArray", "StringToNumber", "SUBSTRING", "SUM",
	"ToString", "TRIM", "TRUNC", "UPPER",
}

var snippets = []dialect.Snippet{
	{Label: "sel", Detail: "SELECT … FROM … WHERE", Body: "SELECT *\nFROM ${container} c\nWHERE c.${property} = ${value}"},
	{Label: "val", Detail: "SELECT VALUE", Body: "SELECT VALUE c.${property}\nFROM ${container} c"},
	{Label: "join", Detail: "JOIN an array", Body: "SELECT c.id, t\nFROM ${container} c\nJOIN t IN c.${tags}"},
	{Label: "pk", Detail: "SET PARTITION KEY", Body: "SET PARTITION KEY \"${value}\""},
	{Label: "use", Detail: "USE container", Body: "USE ${container}"},
}

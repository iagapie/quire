package cosmos

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/sqlsplit"
)

// countPrefix marks the statement counting the rows of the query that follows.
const countPrefix = "QUIRE COUNT "

// pageSize is how many items a query page asks for.
const pageSize = 200

// connector is a Cosmos DB client bound to a database. Connections are views of it
// that keep the session settings (USE, SET PARTITION KEY).
type connector struct {
	client *azcosmos.Client

	mu         sync.Mutex
	database   string
	containers []string // cached names
}

func newConnector(p profile.Profile, database string, n dialect.Network) (*connector, error) {
	endpoint, key := strings.TrimSpace(p.Host), p.Password
	if p.URI != "" {
		var err error
		if endpoint, key, err = parseConnectionString(p.URI); err != nil {
			return nil, err
		}
	}
	if endpoint == "" {
		return nil, errors.New("the account endpoint is missing")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	if key == "" {
		return nil, errors.New("the account key is missing")
	}
	cred, err := azcosmos.NewKeyCredential(key)
	if err != nil {
		return nil, fmt.Errorf("invalid account key: %w", err)
	}
	dial := n.Dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 15 * time.Second}
		dial = dialer.DialContext
	}
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         dial,
		TLSHandshakeTimeout: 15 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 16,
		ForceAttemptHTTP2:   true,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
	}
	if p.Options["insecure"] == "true" {
		transport.TLSClientConfig.InsecureSkipVerify = true
	}
	opts := &azcosmos.ClientOptions{ClientOptions: azcore.ClientOptions{
		Transport: &http.Client{Transport: accountTimeout{transport}},
		Retry:     policy.RetryOptions{MaxRetries: 3, RetryDelay: 500 * time.Millisecond, MaxRetryDelay: 5 * time.Second},
		Telemetry: policy.TelemetryOptions{ApplicationID: "Quire"},
		// The local emulator speaks plain HTTP.
		InsecureAllowCredentialWithHTTP: strings.HasPrefix(strings.ToLower(endpoint), "http://"),
	}}
	client, err := azcosmos.NewClientWithKey(endpoint, cred, opts)
	if err != nil {
		return nil, fmt.Errorf("invalid account endpoint: %w", err)
	}
	if database == "" {
		database = strings.TrimSpace(p.Database)
	}
	return &connector{client: client, database: database}, nil
}

// await runs f and returns when it ends or when ctx is done, whichever comes first:
// azcosmos runs part of some requests (the account refresh) without the caller's
// deadline, and a server that stopped answering must not hold the caller longer.
func await[T any](ctx context.Context, f func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := f()
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// accountRefreshTimeout bounds the account read that azcosmos makes now and then
// before a request (to learn the regions). It runs without the caller's deadline and
// would wait up to a minute on a server that stopped answering.
const accountRefreshTimeout = 8 * time.Second

type accountTimeout struct{ next http.RoundTripper }

func (a accountTimeout) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || (req.URL.Path != "" && req.URL.Path != "/") {
		return a.next.RoundTrip(req)
	}
	ctx, cancel := context.WithTimeout(req.Context(), accountRefreshTimeout)
	resp, err := a.next.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelBody releases the request context once the body is closed.
type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// parseConnectionString reads AccountEndpoint and AccountKey.
func parseConnectionString(s string) (endpoint, key string, err error) {
	for part := range strings.SplitSeq(s, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch strings.ToLower(k) {
		case "accountendpoint":
			endpoint = v
		case "accountkey":
			key = v
		}
	}
	if endpoint == "" || key == "" {
		return "", "", errors.New("invalid connection string: it needs AccountEndpoint=…;AccountKey=…;")
	}
	return endpoint, key, nil
}

func (c *connector) Connect(context.Context) (driver.Conn, error) { return &conn{c: c}, nil }

func (c *connector) Driver() driver.Driver { return cosmosDriver{} }

type cosmosDriver struct{}

func (cosmosDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("open Cosmos DB connections with the connector")
}

// db returns the database of the connector: the configured one, or the first.
func (c *connector) db(ctx context.Context) (*azcosmos.DatabaseClient, error) {
	c.mu.Lock()
	name := c.database
	c.mu.Unlock()
	if name == "" {
		dbs, err := c.databases(ctx)
		if err != nil {
			return nil, err
		}
		if len(dbs) == 0 {
			return nil, errors.New("the account has no databases")
		}
		name = dbs[0]
		c.mu.Lock()
		c.database = name
		c.mu.Unlock()
	}
	return c.client.NewDatabase(name)
}

func (c *connector) databases(ctx context.Context) ([]string, error) {
	pager := c.client.NewQueryDatabasesPager("SELECT * FROM root r", nil)
	var out []string
	for pager.More() {
		page, err := await(ctx, func() (azcosmos.QueryDatabasesResponse, error) { return pager.NextPage(ctx) })
		if err != nil {
			return nil, friendly(err)
		}
		for _, d := range page.Databases {
			out = append(out, d.ID)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (c *connector) containerProps(ctx context.Context) ([]azcosmos.ContainerProperties, error) {
	db, err := c.db(ctx)
	if err != nil {
		return nil, err
	}
	pager := db.NewQueryContainersPager("SELECT * FROM root r", nil)
	var out []azcosmos.ContainerProperties
	for pager.More() {
		page, err := await(ctx, func() (azcosmos.QueryContainersResponse, error) { return pager.NextPage(ctx) })
		if err != nil {
			return nil, friendly(err)
		}
		out = append(out, page.Containers...)
	}
	slices.SortFunc(out, func(a, b azcosmos.ContainerProperties) int { return strings.Compare(a.ID, b.ID) })
	names := make([]string, len(out))
	for i, p := range out {
		names[i] = p.ID
	}
	c.mu.Lock()
	c.containers = names
	c.mu.Unlock()
	return out, nil
}

// containerNames returns the cached container names, reading them when asked to.
func (c *connector) containerNames(ctx context.Context, refresh bool) ([]string, error) {
	c.mu.Lock()
	names := c.containers
	c.mu.Unlock()
	if names != nil && !refresh {
		return names, nil
	}
	if _, err := c.containerProps(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.containers, nil
}

// conn is a connection: the session settings of an editor tab.
type conn struct {
	c         *connector
	container string
	partition *azcosmos.PartitionKey
}

var (
	_ driver.QueryerContext = (*conn)(nil)
	_ driver.ExecerContext  = (*conn)(nil)
	_ driver.Pinger         = (*conn)(nil)
)

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("connections to Cosmos DB do not prepare statements")
}

func (c *conn) Close() error { return nil }

func (c *conn) Begin() (driver.Tx, error) {
	return nil, errors.New("connections to Cosmos DB have no transactions")
}

// Ping reads the database.
func (c *conn) Ping(ctx context.Context) error {
	db, err := c.c.db(ctx)
	if err != nil {
		return err
	}
	_, err = await(ctx, func() (azcosmos.DatabaseResponse, error) { return db.Read(ctx, nil) })
	return friendly(err)
}

// ExecContext runs the session statements: USE container and SET PARTITION KEY.
func (c *conn) ExecContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	toks := dialect.Lex(query, sqlsplit.Options{})
	switch {
	case len(toks) >= 2 && toks[0].Is("USE"):
		name := tokenName(toks[1:])
		names, err := c.c.containerNames(ctx, true)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(names, name) {
			return nil, fmt.Errorf("there is no container %q in this database (containers: %s)", name, strings.Join(names, ", "))
		}
		c.container = name
		return driver.ResultNoRows, nil
	case len(toks) >= 4 && toks[0].Is("SET") && toks[1].Is("PARTITION") && toks[2].Is("KEY"):
		rest := strings.TrimSpace(query[toks[3].Pos:])
		if strings.EqualFold(strings.TrimRight(rest, ";"), "NONE") {
			c.partition = nil
			return driver.ResultNoRows, nil
		}
		pk, err := ParsePartitionKey(strings.TrimRight(rest, "; "))
		if err != nil {
			return nil, err
		}
		c.partition = &pk
		return driver.ResultNoRows, nil
	}
	return nil, errors.New("connections to Cosmos DB run queries only: SELECT, USE container and SET PARTITION KEY")
}

// tokenName reads a name: an identifier, a quoted name or ["name"].
func tokenName(toks []dialect.Token) string {
	if len(toks) >= 3 && toks[0].Value == "[" && (toks[1].Kind == dialect.TokIdent || toks[1].Kind == dialect.TokString) {
		return toks[1].Value
	}
	if toks[0].Kind == dialect.TokIdent || toks[0].Kind == dialect.TokString {
		return toks[0].Value
	}
	return toks[0].Text
}

// ParsePartitionKey reads a partition key value written as JSON: "text", 12, true,
// null, or an array of those for hierarchical keys. A bare word is taken as text.
func ParsePartitionKey(s string) (azcosmos.PartitionKey, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'") && len(s) >= 2 {
		return azcosmos.NewPartitionKeyString(strings.ReplaceAll(s[1:len(s)-1], "''", "'")), nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return azcosmos.NewPartitionKeyString(s), nil //nolint:nilerr // a bare word is text
	}
	values, ok := v.([]any)
	if !ok {
		values = []any{v}
	}
	pk := azcosmos.NewPartitionKey()
	for _, x := range values {
		switch x := x.(type) {
		case string:
			pk = pk.AppendString(x)
		case float64:
			pk = pk.AppendNumber(x)
		case bool:
			pk = pk.AppendBool(x)
		case nil:
			pk = pk.AppendNull()
		default:
			return pk, errors.New("a partition key is text, a number, true/false or null, or an array of those")
		}
	}
	return pk, nil
}

// QueryContext runs a query: the first page is read here, so errors come back with
// the query; later pages are read as the rows are.
func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if rest, ok := strings.CutPrefix(query, countPrefix); ok {
		return c.count(ctx, rest)
	}
	limit := int64(-1)
	if c.partition == nil {
		query, limit = clientTop(query)
	}
	container, text, err := c.resolve(ctx, query)
	if err != nil {
		return nil, err
	}
	db, err := c.c.db(ctx)
	if err != nil {
		return nil, err
	}
	cc, err := db.NewContainer(container)
	if err != nil {
		return nil, err
	}
	opts := &azcosmos.QueryOptions{PageSizeHint: pageSize}
	for _, a := range args {
		name := a.Name
		if name == "" {
			name = "p" + strconv.Itoa(a.Ordinal)
		}
		opts.QueryParameters = append(opts.QueryParameters, azcosmos.QueryParameter{Name: "@" + strings.TrimPrefix(name, "@"), Value: a.Value})
	}
	pk := azcosmos.NewPartitionKey()
	if c.partition != nil {
		pk = *c.partition
	}
	r := &rows{ctx: ctx, pager: cc.NewQueryItemsPager(text, pk, opts), limited: limit >= 0, left: limit}
	if err := r.fetch(); err != nil {
		return nil, err
	}
	return r, nil
}

// resolve finds the container of a query (see the package documentation) and
// rewrites a ["name"] source to ROOT.
func (c *conn) resolve(ctx context.Context, query string) (string, string, error) {
	toks := dialect.Lex(query, sqlsplit.Options{})
	depth := 0
	for i, t := range toks {
		switch t.Value {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth != 0 || !t.Is("FROM") || i+1 >= len(toks) {
			continue
		}
		src := toks[i+1:]
		name, end := "", 0
		switch {
		case len(src) >= 3 && src[0].Value == "[" && (src[1].Kind == dialect.TokIdent || src[1].Kind == dialect.TokString) && src[2].Value == "]":
			name, end = src[1].Value, src[2].End
		case src[0].Kind == dialect.TokIdent:
			name, end = src[0].Value, src[0].End
		case src[0].Kind == dialect.TokWord && !src[0].Is("ROOT"):
			name = src[0].Text
		}
		// "FROM t IN c.tags" iterates an array: no container there.
		if len(src) > 1 && src[1].Is("IN") {
			break
		}
		if name != "" {
			names, err := c.c.containerNames(ctx, false)
			if err != nil {
				return "", "", err
			}
			if !slices.Contains(names, name) {
				if names, err = c.c.containerNames(ctx, true); err != nil {
					return "", "", err
				}
			}
			if slices.Contains(names, name) {
				if end > 0 {
					query = query[:src[0].Pos] + "ROOT" + query[end:]
				}
				return name, query, nil
			}
		}
		break
	}
	if c.container != "" {
		return c.container, query, nil
	}
	names, err := c.c.containerNames(ctx, false)
	if err != nil {
		return "", "", err
	}
	switch len(names) {
	case 0:
		return "", "", errors.New("the database has no containers")
	case 1:
		return names[0], query, nil
	}
	return "", "", fmt.Errorf("name the container in FROM (SELECT * FROM %s c) or choose one with USE %s; the containers are %s",
		names[0], names[0], strings.Join(names, ", "))
}

// count counts the rows of a query by reading them: aggregates are not served across
// partitions, plain projections are. A query with a top-level ORDER BY, TOP, OFFSET
// or a projection is reduced to SELECT VALUE 1 FROM … WHERE ….
func (c *conn) count(ctx context.Context, query string) (driver.Rows, error) {
	limit := int64(-1)
	if c.partition == nil {
		query, limit = clientTop(query)
	}
	q := countable(query)
	container, text, err := c.resolve(ctx, q)
	if err != nil {
		return nil, err
	}
	db, err := c.c.db(ctx)
	if err != nil {
		return nil, err
	}
	cc, err := db.NewContainer(container)
	if err != nil {
		return nil, err
	}
	pk := azcosmos.NewPartitionKey()
	if c.partition != nil {
		pk = *c.partition
	}
	pager := cc.NewQueryItemsPager(text, pk, &azcosmos.QueryOptions{PageSizeHint: -1})
	var n int64
	for pager.More() {
		page, err := await(ctx, func() (azcosmos.QueryItemsResponse, error) { return pager.NextPage(ctx) })
		if err != nil {
			return nil, friendly(err)
		}
		n += int64(len(page.Items))
		if limit >= 0 && n >= limit {
			n = limit
			break
		}
	}
	return &rows{count: true, items: [][]byte{[]byte(strconv.FormatInt(n, 10))}}, nil
}

// clientTop takes a top-level TOP n out of a query that runs across partitions:
// Cosmos DB serves TOP there only through a query plan, which this client has no
// engine for. Without ORDER BY, DISTINCT, GROUP BY or OFFSET, TOP n is any n
// documents, so Quire reads the first n itself. It returns the query and n, or the
// query unchanged and -1.
func clientTop(query string) (string, int64) {
	o := sqlsplit.Options{}
	for _, w := range [][]string{{"ORDER", "BY"}, {"DISTINCT"}, {"GROUP", "BY"}, {"OFFSET"}} {
		if dialect.TopLevel(query, o, w...) {
			return query, -1
		}
	}
	toks := dialect.Lex(query, o)
	if len(toks) < 3 || !toks[0].Is("SELECT") || !toks[1].Is("TOP") {
		return query, -1
	}
	// TOP n, or TOP (n).
	i, end := 2, 3
	if toks[2].Value == "(" {
		if len(toks) < 5 || toks[4].Value != ")" {
			return query, -1
		}
		i, end = 3, 5
	}
	if toks[i].Kind != dialect.TokNumber {
		return query, -1
	}
	n, err := strconv.ParseInt(toks[i].Text, 10, 64)
	if err != nil || n < 0 || end >= len(toks) {
		return query, -1
	}
	return query[:toks[1].Pos] + query[toks[end].Pos:], n
}

// countable reduces a query to SELECT VALUE 1 FROM … WHERE … when it has no DISTINCT
// or GROUP BY (whose rows depend on the projection).
func countable(query string) string {
	o := sqlsplit.Options{}
	if dialect.TopLevel(query, o, "DISTINCT") || dialect.TopLevel(query, o, "GROUP", "BY") {
		return query
	}
	toks := dialect.Lex(query, o)
	depth, from, cut := 0, -1, len(query)
	for _, t := range toks {
		switch t.Value {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth != 0 {
			continue
		}
		if from < 0 && t.Is("FROM") {
			from = t.Pos
		}
		if from >= 0 && (t.Is("ORDER") || t.Is("OFFSET")) && t.Pos < cut {
			cut = t.Pos
		}
	}
	if from < 0 {
		return query
	}
	return "SELECT VALUE 1 " + strings.TrimRight(strings.TrimSpace(query[from:cut]), ";")
}

// rows reads query pages as the database/sql rows are read. Each item is one row
// with one JSON column.
type rows struct {
	ctx   context.Context
	pager *runtime.Pager[azcosmos.QueryItemsResponse]
	items [][]byte
	count bool
	// limited rows stop after left more rows (a TOP applied here).
	limited bool
	left    int64
}

func (r *rows) fetch() error {
	for len(r.items) == 0 && r.pager != nil && r.pager.More() {
		pager := r.pager
		page, err := await(r.ctx, func() (azcosmos.QueryItemsResponse, error) { return pager.NextPage(r.ctx) })
		if err != nil {
			return friendly(err)
		}
		r.items = page.Items
	}
	return nil
}

func (r *rows) Columns() []string {
	if r.count {
		return []string{"count"}
	}
	return []string{"document"}
}

// ColumnTypeDatabaseTypeName is JSON, or BIGINT for a count.
func (r *rows) ColumnTypeDatabaseTypeName(int) string {
	if r.count {
		return "BIGINT"
	}
	return "JSON"
}

func (r *rows) Close() error {
	r.items, r.pager = nil, nil
	return nil
}

func (r *rows) Next(dest []driver.Value) error {
	if r.limited && r.left <= 0 {
		return io.EOF // without reading another page
	}
	if err := r.fetch(); err != nil {
		return err
	}
	if len(r.items) == 0 {
		return io.EOF
	}
	if r.limited {
		r.left--
	}
	if r.count {
		n, _ := strconv.ParseInt(string(r.items[0]), 10, 64)
		dest[0] = n
	} else {
		dest[0] = r.items[0]
	}
	r.items = r.items[1:]
	return nil
}

var cosmosErrors = regexp.MustCompile(`"Errors":\s*\[\s*"((?:[^"\\]|\\.)*)"`)

var errCrossPartition = errors.New("queries across partitions are served only when they filter and project: " +
	"ORDER BY, TOP, OFFSET/LIMIT, DISTINCT, GROUP BY and aggregates need a partition key. " +
	"Set one in the toolbar, or run SET PARTITION KEY \"value\" first")

// friendly turns service errors into short messages.
func friendly(err error) error {
	if err == nil {
		return nil
	}
	var re *azcore.ResponseError
	if !errors.As(err, &re) {
		return err
	}
	msg := err.Error()
	switch {
	case re.StatusCode == http.StatusBadRequest && (strings.Contains(msg, "cross partition") || strings.Contains(msg, "can not be directly served") ||
		strings.Contains(msg, "cannot be directly served")):
		return fmt.Errorf("%w (%w)", errCrossPartition, err)
	case re.StatusCode == http.StatusUnauthorized || re.StatusCode == http.StatusForbidden:
		return fmt.Errorf("the account key was refused (HTTP %d): %w", re.StatusCode, err)
	case re.StatusCode == http.StatusNotFound:
		return fmt.Errorf("not found (the database or container may have been deleted): %w", err)
	}
	if text := serviceMessage(msg); text != "" {
		return &serviceError{msg: text, err: err}
	}
	return err
}

// serviceMessage extracts the error text from the response body that azcore prints
// in its error: {"code": …, "message": "Message: {\"Errors\":[…]}\r\nActivityId: …"}.
func serviceMessage(msg string) string {
	start, end := strings.Index(msg, "\n{"), strings.LastIndex(msg, "}")
	if start < 0 || end < start {
		return ""
	}
	var body struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(msg[start+1:end+1]), &body) != nil || body.Message == "" {
		return ""
	}
	if m := cosmosErrors.FindStringSubmatch(body.Message); m != nil {
		var text string
		if json.Unmarshal([]byte(`"`+m[1]+`"`), &text) == nil {
			return text
		}
	}
	first, _, _ := strings.Cut(body.Message, "\r\n")
	return strings.TrimPrefix(first, "Message: ")
}

// serviceError is a Cosmos DB error with its message extracted from the response.
type serviceError struct {
	msg string
	err error
}

func (e *serviceError) Error() string { return e.msg }
func (e *serviceError) Unwrap() error { return e.err }

// withConn runs f on a connection of q (a pool or a connection).
func withConn(ctx context.Context, q dialect.Querier, f func(*conn) error) error {
	switch x := q.(type) {
	case *sql.DB:
		c, err := x.Conn(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		return rawConn(c, f)
	case *sql.Conn:
		return rawConn(x, f)
	}
	return dialect.ErrUnsupported
}

func rawConn(c *sql.Conn, f func(*conn) error) error {
	return c.Raw(func(dc any) error {
		cc, ok := dc.(*conn)
		if !ok {
			return dialect.ErrUnsupported
		}
		return f(cc)
	})
}

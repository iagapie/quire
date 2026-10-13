// Package docsession manages live MongoDB and DocumentDB connections.
//
// A Session wraps one mongo.Client that lives as long as the connection is open in the
// UI. The driver keeps reconnecting in the background, so a flaky network never forces
// the user to reconnect by hand: every operation gets its own cancellable context and
// read operations are retried automatically on transient network errors.
package docsession

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/connstring"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/topology"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/dialect/mongodb"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/tunnel"
)

// Health is the connectivity state reported to the UI.
type Health string

const (
	HealthOnline       Health = "online"
	HealthReconnecting Health = "reconnecting"
	HealthClosed       Health = "closed"
)

// Session is an open connection.
type Session struct {
	ID      string
	Profile profile.Profile
	Info    dialect.Info
	Client  *mongo.Client
	// Host is the server shown in the UI.
	Host    string
	Version string

	tunnel   *tunnel.Dialer
	onHealth func(Health)
	health   atomic.Value
	failures atomic.Int32

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

// Options for Open.
type Options struct {
	// OnHealth is invoked when connectivity changes.
	OnHealth func(Health)
	// OnHostKey is invoked when an SSH host key is accepted on first use.
	OnHostKey func(fingerprint string)
}

// Open creates the client and verifies the server can be reached.
func Open(ctx context.Context, id string, p profile.Profile, o Options) (*Session, error) {
	drv, ok := dialect.Get(p.Driver)
	if !ok || !mongodb.IsShell(p.Driver) {
		return nil, fmt.Errorf("%q is not a MongoDB database", p.Driver)
	}
	s := &Session{ID: id, Profile: p, Info: drv.Info(), onHealth: o.OnHealth, running: map[string]context.CancelFunc{}}
	s.health.Store(HealthOnline)

	copts, host, err := clientOptions(p)
	if err != nil {
		return nil, err
	}
	s.Host = host

	if p.SSH.Enabled {
		s.tunnel = tunnel.New(tunnel.Config{
			Host: p.SSH.Host, Port: p.SSH.Port, User: p.SSH.User, Auth: p.SSH.Auth,
			Password: p.SSH.Password, PrivateKeyPath: p.SSH.PrivateKeyPath, Passphrase: p.SSH.Passphrase,
			HostKey: p.SSH.HostKey, OnNewHostKey: o.OnHostKey,
		})
		if err := s.tunnel.Check(ctx); err != nil {
			return nil, err
		}
		copts.SetDialer(s.tunnel)
	} else {
		copts.SetDialer(&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 15 * time.Second})
	}

	copts.SetServerMonitor(&event.ServerMonitor{
		ServerHeartbeatFailed: func(*event.ServerHeartbeatFailedEvent) {
			s.failures.Add(1)
			s.setHealth(HealthReconnecting)
		},
		ServerHeartbeatSucceeded: func(*event.ServerHeartbeatSucceededEvent) {
			s.failures.Store(0)
			s.setHealth(HealthOnline)
		},
	})

	client, err := mongo.Connect(copts)
	if err != nil {
		s.closeTunnel()
		return nil, fmt.Errorf("connect: %w", err)
	}
	s.Client = client

	// "ping" needs no privileges, so this works for users without access to admin.
	pingCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := client.Database(s.pingDB()).RunCommand(pingCtx, bson.D{{Key: "ping", Value: 1}}).Err(); err != nil {
		_ = client.Disconnect(context.Background())
		s.closeTunnel()
		return nil, fmt.Errorf("server not reachable: %w", err)
	}
	s.Version = s.serverVersion(pingCtx)
	return s, nil
}

// DefaultDatabase is the database new shells start in.
func (s *Session) DefaultDatabase() string { return s.Profile.Database }

func (s *Session) pingDB() string {
	if s.Profile.Database != "" {
		return s.Profile.Database
	}
	return "admin"
}

func (s *Session) serverVersion(ctx context.Context) string {
	var res struct {
		Version string `bson:"version"`
	}
	if err := s.Client.Database(s.pingDB()).RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&res); err != nil {
		return ""
	}
	return res.Version
}

// Health returns the current connectivity state.
func (s *Session) Health() Health { return s.health.Load().(Health) }

func (s *Session) setHealth(h Health) {
	if prev := s.health.Swap(h); prev != h && s.onHealth != nil {
		s.onHealth(h)
	}
}

// Close disconnects and cancels running operations.
func (s *Session) Close() {
	s.mu.Lock()
	for _, cancel := range s.running {
		cancel()
	}
	s.running = map[string]context.CancelFunc{}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s.Client != nil {
		_ = s.Client.Disconnect(ctx)
	}
	s.closeTunnel()
	s.setHealth(HealthClosed)
}

func (s *Session) closeTunnel() {
	if s.tunnel != nil {
		_ = s.tunnel.Close()
	}
}

// Timeout is the profile operation timeout.
func (s *Session) Timeout() time.Duration {
	return time.Duration(s.Profile.QueryTimeoutMs) * time.Millisecond
}

// Begin registers a cancellable operation. The returned context carries the profile's
// query timeout; call the returned func when the operation finishes.
func (s *Session) Begin(parent context.Context, opID string) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(parent, s.Timeout())
	if opID == "" {
		return ctx, cancel
	}
	s.mu.Lock()
	if prev, ok := s.running[opID]; ok {
		prev()
	}
	s.running[opID] = cancel
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.running, opID)
		s.mu.Unlock()
	}
}

// Cancel aborts a running operation. It returns false if nothing was running.
func (s *Session) Cancel(opID string) bool {
	s.mu.Lock()
	cancel, ok := s.running[opID]
	delete(s.running, opID)
	s.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// retryBackoff is the base delay between read retries (multiplied by the attempt).
var retryBackoff = 750 * time.Millisecond

// RetryRead runs fn, retrying on transient network errors while ctx allows it. The
// driver re-establishes connections (and the SSH tunnel re-dials) between attempts.
func RetryRead[T any](ctx context.Context, onRetry func(attempt int, err error), fn func(context.Context) (T, error)) (T, error) {
	const maxAttempts = 4
	var zero T
	for attempt := 1; ; attempt++ {
		v, err := fn(ctx)
		if err == nil || attempt >= maxAttempts || !IsTransient(err) || ctx.Err() != nil {
			return v, err
		}
		if onRetry != nil {
			onRetry(attempt, err)
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(time.Duration(attempt) * retryBackoff):
		}
	}
}

// IsTransient reports whether err is a network-level failure worth retrying.
func IsTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if mongo.IsNetworkError(err) {
		return true
	}
	var sse topology.ServerSelectionError
	if errors.As(err, &sse) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var le mongo.LabeledError
	if errors.As(err, &le) && (le.HasErrorLabel("RetryableReadError") || le.HasErrorLabel("NetworkError")) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "connection reset") || strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "EOF") || strings.Contains(msg, "ssh tunnel")
}

// hosts splits the host setting ("a:27017,b" or one host per line) into host:port
// seeds; a seed without a port gets the profile port.
func hosts(p profile.Profile) []string {
	port := p.Port
	if port <= 0 {
		port = 27017
	}
	var out []string
	for _, h := range strings.FieldsFunc(p.Host, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' || r == ';' }) {
		if _, _, err := net.SplitHostPort(h); err != nil {
			h = net.JoinHostPort(strings.Trim(h, "[]"), strconv.Itoa(port))
		}
		out = append(out, h)
	}
	return out
}

func clientOptions(p profile.Profile) (*options.ClientOptions, string, error) {
	o := options.Client()
	opt := func(k string) string { return strings.TrimSpace(p.Options[k]) }
	// A connection string replaces the host fields; the other settings override it.
	uri := p.URI
	var seeds []string
	if uri == "" {
		if seeds = hosts(p); len(seeds) == 0 {
			seeds = []string{"localhost:27017"}
		}
		uri = "mongodb://" + strings.Join(seeds, ",") + "/"
	}
	cs, err := connstring.ParseAndValidate(uri)
	if err != nil {
		return nil, "", fmt.Errorf("invalid connection string: %w", err)
	}
	o.ApplyURI(uri)
	if err := o.Validate(); err != nil {
		return nil, "", err
	}
	if len(seeds) > 0 {
		o.SetHosts(seeds)
	}
	host := strings.Join(o.Hosts, ",")
	if len(o.Hosts) == 0 {
		host = strings.Join(cs.Hosts, ",")
	}

	o.SetAppName("quire")
	// DocumentDB does not support retryable writes.
	if p.Driver == mongodb.DocumentDB {
		o.SetRetryWrites(false)
	}
	o.SetRetryReads(true)
	o.SetServerSelectionTimeout(15 * time.Second)
	o.SetConnectTimeout(15 * time.Second)
	o.SetHeartbeatInterval(10 * time.Second)
	// Idle sockets silently die on unstable networks; recycle them early.
	o.SetMaxConnIdleTime(45 * time.Second)
	o.SetMaxPoolSize(16)

	if rs := opt(mongodb.OptReplicaSet); rs != "" {
		o.SetReplicaSet(rs)
	}
	if opt(mongodb.OptDirect) == "true" {
		o.SetDirect(true)
	}
	if rp := opt(mongodb.OptReadPreference); rp != "" {
		mode, err := readpref.ModeFromString(rp)
		if err != nil {
			return nil, "", fmt.Errorf("read preference: %w", err)
		}
		pref, err := readpref.New(mode)
		if err != nil {
			return nil, "", err
		}
		o.SetReadPreference(pref)
	}

	mechanism := opt(mongodb.OptAuthMechanism)
	if p.User != "" || mechanism == "MONGODB-X509" {
		cred := options.Credential{Username: p.User}
		if mechanism != "MONGODB-X509" {
			cred.Password, cred.PasswordSet = p.Password, true
		}
		if o.Auth != nil {
			cred.AuthMechanism = o.Auth.AuthMechanism
			cred.AuthSource = o.Auth.AuthSource
		}
		if mechanism != "" {
			cred.AuthMechanism = mechanism
		}
		if src := opt(mongodb.OptAuthSource); src != "" {
			cred.AuthSource = src
		}
		o.SetAuth(cred)
	}

	if tlsOn(p.TLS.Mode) {
		name := ""
		if len(o.Hosts) == 1 {
			name, _, _ = net.SplitHostPort(o.Hosts[0])
		}
		tc, err := dialect.TLSConfig(p.TLS, name)
		if err != nil {
			return nil, "", err
		}
		// Each replica set member is checked against its own name.
		if len(o.Hosts) != 1 && p.TLS.ServerName == "" {
			tc.ServerName = ""
		}
		o.SetTLSConfig(tc)
	}
	return o, host, nil
}

// tlsOn reports whether a TLS mode encrypts. MongoDB has no opportunistic TLS, so
// "prefer" (what profile.Normalize sets when no mode is given) leaves it to the
// connection string.
func tlsOn(mode string) bool {
	return mode == profile.TLSRequire || mode == profile.TLSVerifyCA || mode == profile.TLSVerifyFull
}

// ExtraDatabases are the databases of the profile shown in the tree even when the user
// cannot list databases.
func (s *Session) extraDatabases() []string {
	var out []string
	for _, d := range strings.Split(s.Profile.Options[mongodb.OptExtraDatabases], ",") {
		if d = strings.TrimSpace(d); d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// ListDatabases returns the databases visible to the user. Users without the
// listDatabases privilege get their role databases plus the configured ones.
func (s *Session) ListDatabases(ctx context.Context) ([]string, error) {
	names, err := s.Client.ListDatabaseNames(ctx, bson.D{}, options.ListDatabases().SetAuthorizedDatabases(true))
	if err != nil {
		names, err = s.Client.ListDatabaseNames(ctx, bson.D{})
	}
	if err != nil {
		if IsTransient(err) {
			return nil, err
		}
		names = s.rolesDatabases(ctx)
	}
	for _, d := range append([]string{s.Profile.Database, s.authSource()}, s.extraDatabases()...) {
		if d != "" && !slices.Contains(names, d) {
			names = append(names, d)
		}
	}
	slices.Sort(names)
	return names, nil
}

func (s *Session) authSource() string {
	if src := strings.TrimSpace(s.Profile.Options[mongodb.OptAuthSource]); src != "" && src != "admin" {
		return src
	}
	return ""
}

func (s *Session) rolesDatabases(ctx context.Context) []string {
	var res struct {
		AuthInfo struct {
			Roles []struct {
				DB string `bson:"db"`
			} `bson:"authenticatedUserRoles"`
			Privileges []struct {
				Resource struct {
					DB string `bson:"db"`
				} `bson:"resource"`
			} `bson:"authenticatedUserPrivileges"`
		} `bson:"authInfo"`
	}
	cmd := bson.D{{Key: "connectionStatus", Value: 1}, {Key: "showPrivileges", Value: true}}
	if err := s.Client.Database(s.pingDB()).RunCommand(ctx, cmd).Decode(&res); err != nil {
		return nil
	}
	var out []string
	add := func(d string) {
		if d != "" && d != "admin" && d != "local" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	for _, r := range res.AuthInfo.Privileges {
		add(r.Resource.DB)
	}
	for _, r := range res.AuthInfo.Roles {
		add(r.DB)
	}
	return out
}

// Collection describes a collection or view.
type Collection struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// ListCollections returns collections and views in db.
func (s *Session) ListCollections(ctx context.Context, db string) ([]Collection, error) {
	d := s.Client.Database(db)
	specs, err := d.ListCollectionSpecifications(ctx, bson.D{}, options.ListCollections().SetNameOnly(true).SetAuthorizedCollections(true))
	if err != nil && !IsTransient(err) {
		specs, err = d.ListCollectionSpecifications(ctx, bson.D{})
	}
	if err != nil {
		return nil, err
	}
	out := make([]Collection, 0, len(specs))
	for _, sp := range specs {
		t := sp.Type
		if t == "" {
			t = "collection"
		}
		out = append(out, Collection{Name: sp.Name, Type: t})
	}
	slices.SortFunc(out, func(a, b Collection) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Index describes an index.
type Index struct {
	Name   string `json:"name"`
	Keys   string `json:"keys"`
	Unique bool   `json:"unique"`
	Sparse bool   `json:"sparse"`
	TTL    *int32 `json:"ttl"`
}

// ListIndexes returns the indexes of a collection.
func (s *Session) ListIndexes(ctx context.Context, db, coll string) ([]Index, error) {
	specs, err := s.Client.Database(db).Collection(coll).Indexes().ListSpecifications(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Index, 0, len(specs))
	for _, sp := range specs {
		keys, _ := bson.MarshalExtJSON(sp.KeysDocument, false, false)
		ix := Index{Name: sp.Name, Keys: string(keys), TTL: sp.ExpireAfterSeconds}
		if sp.Unique != nil {
			ix.Unique = *sp.Unique
		}
		if sp.Sparse != nil {
			ix.Sparse = *sp.Sparse
		}
		out = append(out, ix)
	}
	return out, nil
}

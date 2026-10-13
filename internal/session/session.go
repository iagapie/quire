// Package session manages live database connections.
//
// A Session is an open connection profile: one connection pool per database (drivers
// whose connections are bound to a database get one pool each), the SSH tunnel and a
// health monitor. database/sql re-establishes pooled connections on its own, so a
// flaky network never forces the user to reconnect by hand: the monitor pings the
// server and reports online / reconnecting, every operation gets its own cancellable
// context with the profile timeout, and reads are retried on transient errors.
package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iagapie/quire/internal/dialect"
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
	Driver  dialect.SQLDriver
	Info    dialect.Info
	// Host is the server shown in the UI.
	Host string
	// Database and Schema are where a new connection starts.
	Database string
	Schema   string
	Version  string

	tunnel   *tunnel.Dialer
	network  dialect.Network
	onHealth func(Health)
	health   atomic.Value
	stop     chan struct{}
	stopOnce sync.Once

	mu      sync.Mutex
	pools   map[string]*sql.DB // by database; "" is the default pool
	running map[string]context.CancelFunc
}

// Options for Open.
type Options struct {
	// OnHealth is invoked when connectivity changes.
	OnHealth func(Health)
	// OnHostKey is invoked when an SSH host key is accepted on first use.
	OnHostKey func(fingerprint string)
	// PingInterval is how often the server is pinged (default 10s).
	PingInterval time.Duration
}

// ErrNotSQL is returned for profiles whose driver is not reached through database/sql.
var ErrNotSQL = errors.New("this database is not opened through SQL connections")

// Open creates the connection pool and verifies the server can be reached.
func Open(ctx context.Context, id string, p profile.Profile, o Options) (*Session, error) {
	drv, ok := dialect.Get(p.Driver)
	if !ok {
		return nil, fmt.Errorf("unknown database type %q", p.Driver)
	}
	sd, ok := drv.(dialect.SQLDriver)
	if !ok {
		return nil, ErrNotSQL
	}
	s := &Session{
		ID: id, Profile: p, Driver: sd, Info: drv.Info(), onHealth: o.OnHealth,
		pools: map[string]*sql.DB{}, running: map[string]context.CancelFunc{}, stop: make(chan struct{}),
	}
	s.health.Store(HealthOnline)
	s.Host = DisplayHost(p, s.Info)

	if p.SSH.Enabled && s.Info.SSH {
		s.tunnel = tunnel.New(tunnel.Config{
			Host: p.SSH.Host, Port: p.SSH.Port, User: p.SSH.User, Auth: p.SSH.Auth,
			Password: p.SSH.Password, PrivateKeyPath: p.SSH.PrivateKeyPath, Passphrase: p.SSH.Passphrase,
			HostKey: p.SSH.HostKey, OnNewHostKey: o.OnHostKey,
		})
		if err := s.tunnel.Check(ctx); err != nil {
			return nil, err
		}
		s.network = dialect.Network{Dial: s.tunnel.DialContext, Forward: s.tunnel.Forward}
	}

	db, err := sd.Open(ctx, p, "", s.network)
	if err != nil {
		s.closeTunnel()
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		s.closeTunnel()
		if sd.Transient(err) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("server not reachable: %w", err)
		}
		return nil, err
	}
	s.pools[""] = db
	if v, err := sd.Version(pingCtx, db); err == nil {
		s.Version = v
	}
	if d, sc, err := sd.Current(pingCtx, db); err == nil {
		s.Database, s.Schema = d, sc
	}
	interval := o.PingInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	go s.monitor(interval)
	return s, nil
}

// DisplayHost is the server name shown in the UI.
func DisplayHost(p profile.Profile, info dialect.Info) string {
	switch {
	case info.File:
		if p.Database == "" {
			return ":memory:"
		}
		return p.Database
	case p.URI != "" && p.Host == "":
		return "connection string"
	case p.Port != 0 && p.Port != info.DefaultPort:
		return net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	case p.Host == "":
		return "localhost"
	}
	return p.Host
}

// Health returns the current connectivity state.
func (s *Session) Health() Health { return s.health.Load().(Health) }

func (s *Session) setHealth(h Health) {
	if prev := s.health.Swap(h); prev != h && s.onHealth != nil {
		s.onHealth(h)
	}
}

// monitor pings the default pool. A failed ping marks the session reconnecting; the
// pool dials a new connection on the next ping (and the SSH tunnel re-dials).
func (s *Session) monitor(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.Ping()
		}
	}
}

// Ping checks the server now and updates the health.
func (s *Session) Ping() {
	db := s.defaultPool()
	if db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		s.setHealth(HealthReconnecting)
		return
	}
	s.setHealth(HealthOnline)
}

// Failed reports an operation error; network failures mark the session reconnecting
// at once instead of waiting for the next ping.
func (s *Session) Failed(err error) {
	if err != nil && s.Driver.Transient(err) && dialect.NetworkError(err) {
		s.setHealth(HealthReconnecting)
		go s.Ping()
	}
}

func (s *Session) defaultPool() *sql.DB {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pools[""]
}

// DB returns the pool for database: the default pool, or for drivers bound to one
// database per connection, a pool opened on first use.
func (s *Session) DB(ctx context.Context, database string) (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	def := s.pools[""]
	if def == nil {
		return nil, errors.New("connection is closed")
	}
	pd, ok := s.Driver.(dialect.PerDatabase)
	if !ok || !pd.PerDatabasePools() || database == "" || database == s.Database {
		return def, nil
	}
	if db := s.pools[database]; db != nil {
		return db, nil
	}
	db, err := s.Driver.Open(ctx, s.Profile, database, s.network)
	if err != nil {
		return nil, err
	}
	s.pools[database] = db
	return db, nil
}

// Close disconnects and cancels running operations.
func (s *Session) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.mu.Lock()
	for _, cancel := range s.running {
		cancel()
	}
	s.running = map[string]context.CancelFunc{}
	pools := s.pools
	s.pools = map[string]*sql.DB{}
	s.mu.Unlock()
	for _, db := range pools {
		_ = db.Close()
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
// timeout; call the returned func when the operation finishes.
func (s *Session) Begin(parent context.Context, opID string) (context.Context, func()) {
	return s.BeginWithTimeout(parent, opID, s.Timeout())
}

// BeginWithTimeout is Begin with an explicit timeout (0: none).
func (s *Session) BeginWithTimeout(parent context.Context, opID string, timeout time.Duration) (context.Context, func()) {
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
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

// RetryBackoff is the base delay between read retries (multiplied by the attempt).
var RetryBackoff = 750 * time.Millisecond

// MaxAttempts is how many times a read is tried.
const MaxAttempts = 4

// RetryRead runs fn, retrying on transient errors while ctx allows it. The pool
// opens new connections between attempts (and the SSH tunnel re-dials).
func RetryRead[T any](ctx context.Context, s *Session, onRetry func(attempt int, err error), fn func(context.Context) (T, error)) (T, error) {
	var zero T
	for attempt := 1; ; attempt++ {
		v, err := fn(ctx)
		if err == nil {
			if attempt > 1 {
				s.setHealth(HealthOnline)
			}
			return v, nil
		}
		if attempt >= MaxAttempts || !s.Driver.Transient(err) || ctx.Err() != nil {
			s.Failed(err)
			return v, err
		}
		s.Failed(err)
		if onRetry != nil {
			onRetry(attempt, err)
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(time.Duration(attempt) * RetryBackoff):
		}
	}
}

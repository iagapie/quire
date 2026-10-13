// Package app contains the services bound to the frontend.
package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/dialect/mongodb"
	"github.com/iagapie/quire/internal/docsession"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/query"
	"github.com/iagapie/quire/internal/session"
)

// Event names emitted to the frontend.
const (
	EventHealth = "session:health"
	EventRetry  = "op:retry"
)

// HealthEvent reports connectivity changes of a session.
type HealthEvent struct {
	SessionID string `json:"sessionId"`
	Health    string `json:"health"`
}

// RetryEvent reports that an operation is being retried after a network error.
type RetryEvent struct {
	OpID    string `json:"opId"`
	Attempt int    `json:"attempt"`
	Error   string `json:"error"`
}

func init() {
	application.RegisterEvent[HealthEvent](EventHealth)
	application.RegisterEvent[RetryEvent](EventRetry)
}

// SessionInfo describes an open connection.
type SessionInfo struct {
	ID            string `json:"id"`
	ProfileID     string `json:"profileId"`
	Name          string `json:"name"`
	Color         string `json:"color"`
	Driver        string `json:"driver"`
	Host          string `json:"host"`
	User          string `json:"user"`
	ReadOnly      bool   `json:"readOnly"`
	Database      string `json:"database"`
	Schema        string `json:"schema"`
	Health        string `json:"health"`
	ServerVersion string `json:"serverVersion"`
	SSH           bool   `json:"ssh"`
}

// TestResult is the outcome of a connection test.
type TestResult struct {
	ServerVersion string `json:"serverVersion"`
	LatencyMs     int64  `json:"latencyMs"`
	Databases     int    `json:"databases"`
}

// Connections manages profiles, open sessions and metadata. SQL sessions live here;
// MongoDB and DocumentDB sessions are opened here too but kept in docs, shared with
// the services of internal/docapp.
type Connections struct {
	store  *profile.Store
	engine *query.Engine
	docs   *docsession.Registry

	mu       sync.RWMutex
	sessions map[string]*session.Session
}

// NewConnections creates the service.
func NewConnections(store *profile.Store, engine *query.Engine, docs *docsession.Registry) *Connections {
	return &Connections{store: store, engine: engine, docs: docs, sessions: map[string]*session.Session{}}
}

// ServiceShutdown closes all sessions.
func (c *Connections) ServiceShutdown() error {
	c.engine.Close()
	for _, s := range c.docs.All() {
		c.docs.Remove(s.ID)
		s.Close()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, s := range c.sessions {
		s.Close()
		delete(c.sessions, id)
	}
	return nil
}

// Drivers describes the supported databases.
func (c *Connections) Drivers() []dialect.Info {
	all := dialect.All()
	out := make([]dialect.Info, len(all))
	for i, d := range all {
		out[i] = d.Info()
		if sd, ok := d.(dialect.SQLDriver); ok {
			o := sd.Split()
			out[i].Split = &o
		}
	}
	return out
}

// ListProfiles returns saved connections (without secrets).
func (c *Connections) ListProfiles() []profile.Profile {
	return c.store.List()
}

// GetProfile returns a saved connection without its secrets.
func (c *Connections) GetProfile(id string) (profile.Profile, error) {
	p, err := c.store.Get(id)
	if err != nil {
		return p, err
	}
	p.HideSecrets()
	return p, nil
}

// SaveProfile creates or updates a connection. Empty secrets keep their stored values.
func (c *Connections) SaveProfile(p profile.Profile) (profile.Profile, error) {
	p.Normalize()
	if err := p.Validate(); err != nil {
		return p, err
	}
	saved, err := c.store.Save(p, true)
	if err != nil {
		return saved, err
	}
	saved.HideSecrets()
	return saved, nil
}

// DuplicateProfile copies a connection, secrets included.
func (c *Connections) DuplicateProfile(id string) (profile.Profile, error) {
	p, err := c.store.Get(id)
	if err != nil {
		return p, err
	}
	p.ID = ""
	p.Name += " (copy)"
	saved, err := c.store.Save(p, false)
	saved.HideSecrets()
	return saved, err
}

// DeleteProfile removes a connection, closing it first if open.
func (c *Connections) DeleteProfile(id string) error {
	c.Disconnect(id)
	return c.store.Delete(id)
}

// ReorderProfiles persists the display order.
func (c *Connections) ReorderProfiles(ids []string) error {
	return c.store.Reorder(ids)
}

// withSecrets fills empty secrets of an edited (maybe unsaved) profile from the store.
func (c *Connections) withSecrets(p profile.Profile) profile.Profile {
	if p.ID == "" {
		return p
	}
	if old, err := c.store.Get(p.ID); err == nil {
		p.KeepSecrets(old)
	}
	return p
}

// TestConnection opens a temporary connection with the given (possibly unsaved) settings.
func (c *Connections) TestConnection(ctx context.Context, p profile.Profile) (TestResult, error) {
	p.Normalize()
	if err := p.Validate(); err != nil {
		return TestResult{}, err
	}
	p = c.withSecrets(p)
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	start := time.Now()
	if mongodb.IsShell(p.Driver) {
		s, err := docsession.Open(ctx, "test", p, docsession.Options{})
		if err != nil {
			return TestResult{}, err
		}
		defer s.Close()
		res := TestResult{LatencyMs: time.Since(start).Milliseconds(), ServerVersion: s.Version}
		if dbs, err := s.ListDatabases(ctx); err == nil {
			res.Databases = len(dbs)
		}
		return res, nil
	}
	s, err := session.Open(ctx, "test", p, session.Options{})
	if err != nil {
		return TestResult{}, friendlyConnect(err)
	}
	defer s.Close()
	res := TestResult{LatencyMs: time.Since(start).Milliseconds(), ServerVersion: s.Version}
	if db, err := s.DB(ctx, ""); err == nil {
		if dbs, err := s.Driver.Databases(ctx, db); err == nil {
			res.Databases = len(dbs)
		}
	}
	return res, nil
}

// Connect opens the connection of a profile. The session id equals the profile id, so
// a profile has at most one live session, which every tab shares.
func (c *Connections) Connect(ctx context.Context, profileID string) (SessionInfo, error) {
	c.mu.RLock()
	if s, ok := c.sessions[profileID]; ok {
		c.mu.RUnlock()
		return info(s), nil
	}
	c.mu.RUnlock()
	if s, err := c.docs.Get(profileID); err == nil {
		return docInfo(s), nil
	}

	p, err := c.store.Get(profileID)
	if err != nil {
		return SessionInfo{}, err
	}
	p.Normalize()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if mongodb.IsShell(p.Driver) {
		return c.connectDoc(ctx, p)
	}
	s, err := session.Open(ctx, profileID, p, session.Options{
		OnHealth: func(h session.Health) {
			emit(EventHealth, HealthEvent{SessionID: profileID, Health: string(h)})
		},
		OnHostKey: func(fp string) { _ = c.store.SetHostKey(profileID, fp) },
	})
	if err != nil {
		return SessionInfo{}, friendlyConnect(err)
	}
	c.mu.Lock()
	if existing, ok := c.sessions[profileID]; ok {
		c.mu.Unlock()
		s.Close()
		return info(existing), nil
	}
	c.sessions[profileID] = s
	c.mu.Unlock()
	return info(s), nil
}

func (c *Connections) connectDoc(ctx context.Context, p profile.Profile) (SessionInfo, error) {
	id := p.ID
	s, err := docsession.Open(ctx, id, p, docsession.Options{
		OnHealth: func(h docsession.Health) {
			emit(EventHealth, HealthEvent{SessionID: id, Health: string(h)})
		},
		OnHostKey: func(fp string) { _ = c.store.SetHostKey(id, fp) },
	})
	if err != nil {
		return SessionInfo{}, err
	}
	if cur, added := c.docs.Add(s); !added {
		s.Close()
		return docInfo(cur), nil
	}
	return docInfo(s), nil
}

// Disconnect closes a session.
func (c *Connections) Disconnect(sessionID string) {
	if s, ok := c.docs.Remove(sessionID); ok {
		s.Close()
		return
	}
	c.mu.Lock()
	s, ok := c.sessions[sessionID]
	delete(c.sessions, sessionID)
	c.mu.Unlock()
	if ok {
		c.engine.DropSession(s)
		s.Close()
	}
}

// Sessions lists open sessions.
func (c *Connections) Sessions() []SessionInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]SessionInfo, 0, len(c.sessions))
	for _, s := range c.sessions {
		out = append(out, info(s))
	}
	for _, s := range c.docs.All() {
		out = append(out, docInfo(s))
	}
	return out
}

func docInfo(s *docsession.Session) SessionInfo {
	p := s.Profile
	return SessionInfo{
		ID: s.ID, ProfileID: p.ID, Name: p.Name, Color: p.Color, Driver: p.Driver, Host: s.Host, User: p.User,
		ReadOnly: p.ReadOnly, Database: s.DefaultDatabase(), Health: string(s.Health()),
		ServerVersion: s.Version, SSH: p.SSH.Enabled,
	}
}

func info(s *session.Session) SessionInfo {
	p := s.Profile
	return SessionInfo{
		ID: s.ID, ProfileID: p.ID, Name: p.Name, Color: p.Color, Driver: p.Driver, Host: s.Host, User: p.User,
		ReadOnly: p.ReadOnly, Database: s.Database, Schema: s.Schema, Health: string(s.Health()),
		ServerVersion: s.Version, SSH: p.SSH.Enabled,
	}
}

// ErrNotConnected is returned when a session id is unknown.
var ErrNotConnected = docsession.ErrNotConnected

func (c *Connections) get(sessionID string) (*session.Session, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.sessions[sessionID]
	if !ok {
		return nil, ErrNotConnected
	}
	return s, nil
}

func emit(name string, data any) {
	if app := application.Get(); app != nil {
		app.Event.Emit(name, data)
	}
}

var errReadOnly = errors.New("this connection is read-only")

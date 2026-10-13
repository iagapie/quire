package docsession

import (
	"errors"
	"sync"
)

// ErrNotConnected is returned when a session id is unknown.
var ErrNotConnected = errors.New("connection is closed; connect again")

// Registry holds the open sessions, shared by the services that use them.
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{sessions: map[string]*Session{}}
}

// Get returns the open session with the given id.
func (r *Registry) Get(id string) (*Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[id]
	if !ok {
		return nil, ErrNotConnected
	}
	return s, nil
}

// Add stores s unless a session with its id is open; it returns the open one.
func (r *Registry) Add(s *Session) (*Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.sessions[s.ID]; ok {
		return cur, false
	}
	r.sessions[s.ID] = s
	return s, true
}

// Remove forgets the session with the given id and returns it.
func (r *Registry) Remove(id string) (*Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	delete(r.sessions, id)
	return s, ok
}

// All returns the open sessions.
func (r *Registry) All() []*Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	return out
}

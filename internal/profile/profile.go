// Package profile stores saved connection profiles. Non-secret settings live in a
// JSON file under the user config dir; passwords live in the OS keychain.
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
)

const keyringService = "com.iagapie.quire"

// ErrNotFound is returned when a profile id does not exist.
var ErrNotFound = errors.New("connection profile not found")

// SSH holds the SSH tunnel settings of a profile.
type SSH struct {
	Enabled bool   `json:"enabled"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	User    string `json:"user"`
	// Auth is one of "password", "key", "agent".
	Auth           string `json:"auth"`
	Password       string `json:"password,omitempty"`
	PrivateKeyPath string `json:"privateKeyPath"`
	Passphrase     string `json:"passphrase,omitempty"`
	// HostKey is the SHA256 fingerprint accepted on first connect (trust on first use).
	HostKey string `json:"hostKey"`
}

// TLS holds the TLS settings of a profile. Each driver maps them onto its own options.
type TLS struct {
	// Mode is "disable", "prefer", "require", "verify-ca" or "verify-full".
	Mode string `json:"mode"`
	// UseAWSBundle trusts the embedded Amazon RDS CA bundle.
	UseAWSBundle bool   `json:"useAwsBundle"`
	CAFile       string `json:"caFile"`
	CertFile     string `json:"certFile"`
	KeyFile      string `json:"keyFile"`
	// ServerName overrides the name checked against the server certificate.
	ServerName string `json:"serverName"`
}

// TLS modes.
const (
	TLSDisable    = "disable"
	TLSPrefer     = "prefer"
	TLSRequire    = "require"
	TLSVerifyCA   = "verify-ca"
	TLSVerifyFull = "verify-full"
)

// Profile is a saved connection.
type Profile struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
	// Driver is the id of the database kind ("postgres", "mysql", ...).
	Driver string `json:"driver"`

	Host string `json:"host"`
	Port int    `json:"port"`
	// Database is the default database; for file databases (SQLite) the file path.
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password,omitempty"`
	// URI, when set, is the raw connection string used instead of the fields above. It
	// may hold credentials, so it is stored with the secrets.
	URI string `json:"uri,omitempty"`
	// HasURI tells the UI a connection string is stored (the URI itself is secret).
	HasURI bool `json:"hasUri"`

	// Options are driver-specific settings (see dialect.Field).
	Options map[string]string `json:"options"`
	// SecretOptions are driver-specific settings kept in the keychain.
	SecretOptions map[string]string `json:"secretOptions,omitempty"`

	ReadOnly       bool `json:"readOnly"`
	QueryTimeoutMs int  `json:"queryTimeoutMs"`

	TLS TLS `json:"tls"`
	SSH SSH `json:"ssh"`
}

// Normalize fills defaults and trims whitespace.
func (p *Profile) Normalize() {
	p.Name = strings.TrimSpace(p.Name)
	p.Host = strings.TrimSpace(p.Host)
	p.Database = strings.TrimSpace(p.Database)
	p.User = strings.TrimSpace(p.User)
	p.URI = strings.TrimSpace(p.URI)
	if p.Options == nil {
		p.Options = map[string]string{}
	}
	for k, v := range p.Options {
		if v = strings.TrimSpace(v); v == "" {
			delete(p.Options, k)
		} else {
			p.Options[k] = v
		}
	}
	if p.QueryTimeoutMs <= 0 {
		p.QueryTimeoutMs = 120_000
	}
	if p.TLS.Mode == "" {
		p.TLS.Mode = TLSPrefer
	}
	if p.SSH.Port == 0 {
		p.SSH.Port = 22
	}
	if p.SSH.Auth == "" {
		p.SSH.Auth = "key"
	}
}

// Validate reports user-facing problems with the profile. Driver-specific checks are
// done by the driver when connecting.
func (p *Profile) Validate() error {
	if p.Name == "" {
		return errors.New("name is required")
	}
	if p.Driver == "" {
		return errors.New("database type is required")
	}
	if p.SSH.Enabled && (p.SSH.Host == "" || p.SSH.User == "") {
		return errors.New("SSH host and user are required")
	}
	return nil
}

// HideSecrets blanks every secret field.
func (p *Profile) HideSecrets() {
	p.HasURI = p.HasURI || p.URI != ""
	p.Password, p.URI, p.SSH.Password, p.SSH.Passphrase = "", "", "", ""
	if len(p.SecretOptions) > 0 {
		hidden := make(map[string]string, len(p.SecretOptions))
		for k := range p.SecretOptions {
			hidden[k] = ""
		}
		p.SecretOptions = hidden
	}
}

// KeepSecrets fills empty secrets of p from old, the stored version of the profile.
func (p *Profile) KeepSecrets(old Profile) {
	if p.Password == "" {
		p.Password = old.Password
	}
	if p.URI == "" && p.HasURI {
		p.URI = old.URI
	}
	if p.SSH.Password == "" {
		p.SSH.Password = old.SSH.Password
	}
	if p.SSH.Passphrase == "" {
		p.SSH.Passphrase = old.SSH.Passphrase
	}
	if p.SSH.HostKey == "" {
		p.SSH.HostKey = old.SSH.HostKey
	}
	for k, v := range old.SecretOptions {
		if cur, ok := p.SecretOptions[k]; ok && cur == "" {
			p.SecretOptions[k] = v
		}
	}
}

type secrets struct {
	Password      string            `json:"p,omitempty"`
	URI           string            `json:"u,omitempty"`
	SSHPassword   string            `json:"sp,omitempty"`
	SSHPassphrase string            `json:"spp,omitempty"`
	Options       map[string]string `json:"o,omitempty"`
}

// Store persists profiles.
type Store struct {
	mu       sync.Mutex
	path     string
	profiles []Profile
	// useKeyring is false when the OS keychain is unavailable; secrets then stay in the file.
	useKeyring bool
}

// NewStore loads profiles from dir/connections.json, keeping secrets in the OS keychain
// when it is available.
func NewStore(dir string) (*Store, error) {
	return newStore(dir, keyringAvailable())
}

// NewFileStore is like NewStore but keeps secrets in the JSON file (mode 0600).
func NewFileStore(dir string) (*Store, error) {
	return newStore(dir, false)
}

func newStore(dir string, useKeyring bool) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	s := &Store{path: filepath.Join(dir, "connections.json"), useKeyring: useKeyring}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read profiles: %w", err)
	}
	if err := json.Unmarshal(data, &s.profiles); err != nil {
		return nil, fmt.Errorf("parse profiles: %w", err)
	}
	return s, nil
}

func keyringAvailable() bool {
	const probe = "__probe__"
	if err := keyring.Set(keyringService, probe, "1"); err != nil {
		return false
	}
	_ = keyring.Delete(keyringService, probe)
	return true
}

// List returns profiles without secrets.
func (s *Store) List() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Profile, len(s.profiles))
	for i, p := range s.profiles {
		p.Options = maps.Clone(p.Options)
		p.HideSecrets()
		out[i] = p
	}
	return out
}

// Get returns a profile including its secrets.
func (s *Store) Get(id string) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return Profile{}, ErrNotFound
	}
	p := s.profiles[i]
	p.Options = maps.Clone(p.Options)
	p.SecretOptions = maps.Clone(p.SecretOptions)
	if s.useKeyring {
		if raw, err := keyring.Get(keyringService, id); err == nil {
			var sec secrets
			if json.Unmarshal([]byte(raw), &sec) == nil {
				p.Password, p.URI, p.SSH.Password, p.SSH.Passphrase = sec.Password, sec.URI, sec.SSHPassword, sec.SSHPassphrase
				if len(sec.Options) > 0 {
					p.SecretOptions = sec.Options
				}
			}
		}
	}
	return p, nil
}

// Save inserts or updates a profile. Empty secret fields keep the stored value when
// keepSecrets is set.
func (s *Store) Save(p Profile, keepSecrets bool) (Profile, error) {
	if keepSecrets && p.ID != "" {
		if old, err := s.Get(p.ID); err == nil {
			p.KeepSecrets(old)
		}
	}
	p.HasURI = p.URI != ""
	for k, v := range p.SecretOptions {
		if v == "" {
			delete(p.SecretOptions, k)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		p.ID = newID()
	}
	stored := p
	if s.useKeyring {
		//nolint:gosec // G117: secrets are serialized on purpose, to be stored in the OS keychain.
		sec, _ := json.Marshal(secrets{
			Password: p.Password, URI: p.URI, SSHPassword: p.SSH.Password, SSHPassphrase: p.SSH.Passphrase, Options: p.SecretOptions,
		})
		if err := keyring.Set(keyringService, p.ID, string(sec)); err != nil {
			return Profile{}, fmt.Errorf("store secrets in keychain: %w", err)
		}
		stored.Password, stored.URI, stored.SSH.Password, stored.SSH.Passphrase = "", "", "", ""
		if len(stored.SecretOptions) > 0 {
			keys := map[string]string{}
			for k := range stored.SecretOptions {
				keys[k] = ""
			}
			stored.SecretOptions = keys
		}
	}
	if i := s.index(p.ID); i >= 0 {
		s.profiles[i] = stored
	} else {
		s.profiles = append(s.profiles, stored)
	}
	return p, s.flush()
}

// SetHostKey records an accepted SSH host key fingerprint.
func (s *Store) SetHostKey(id, fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return ErrNotFound
	}
	s.profiles[i].SSH.HostKey = fingerprint
	return s.flush()
}

// Delete removes a profile and its secrets.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return ErrNotFound
	}
	s.profiles = slices.Delete(s.profiles, i, i+1)
	if s.useKeyring {
		_ = keyring.Delete(keyringService, id)
	}
	return s.flush()
}

// Reorder sets the display order of profiles.
func (s *Store) Reorder(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pos := make(map[string]int, len(ids))
	for i, id := range ids {
		pos[id] = i
	}
	slices.SortStableFunc(s.profiles, func(a, b Profile) int {
		pa, oka := pos[a.ID]
		pb, okb := pos[b.ID]
		switch {
		case oka && okb:
			return pa - pb
		case oka:
			return -1
		case okb:
			return 1
		}
		return 0
	})
	return s.flush()
}

func (s *Store) index(id string) int {
	return slices.IndexFunc(s.profiles, func(p Profile) bool { return p.ID == id })
}

func (s *Store) flush() error {
	// Secrets are blanked above when the keychain is used; otherwise the file (0600)
	// is the documented fallback store.
	data, err := json.MarshalIndent(s.profiles, "", "  ") //nolint:gosec // G117: see above.
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write profiles: %w", err)
	}
	return os.Rename(tmp, s.path)
}

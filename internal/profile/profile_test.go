package profile

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestNormalize(t *testing.T) {
	p := Profile{Name: "  Prod  ", Host: " db ", Database: " app ", URI: " postgres://x ", Options: map[string]string{"a": " 1 ", "b": "  "}}
	p.Normalize()
	if p.Name != "Prod" || p.Host != "db" || p.Database != "app" || p.URI != "postgres://x" {
		t.Errorf("trim: %+v", p)
	}
	if !maps.Equal(p.Options, map[string]string{"a": "1"}) {
		t.Errorf("options = %v", p.Options)
	}
	if p.QueryTimeoutMs != 120_000 || p.SSH.Port != 22 || p.SSH.Auth != "key" || p.TLS.Mode != TLSPrefer {
		t.Errorf("defaults: %+v", p)
	}

	custom := Profile{QueryTimeoutMs: 5000, SSH: SSH{Port: 2222, Auth: "agent"}, TLS: TLS{Mode: TLSDisable}}
	custom.Normalize()
	if custom.QueryTimeoutMs != 5000 || custom.SSH.Port != 2222 || custom.SSH.Auth != "agent" || custom.TLS.Mode != TLSDisable {
		t.Errorf("explicit values overwritten: %+v", custom)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		p    Profile
		ok   bool
	}{
		{"missing name", Profile{Driver: "postgres"}, false},
		{"missing driver", Profile{Name: "n"}, false},
		{"complete", Profile{Name: "n", Driver: "postgres"}, true},
		{"ssh without host", Profile{Name: "n", Driver: "postgres", SSH: SSH{Enabled: true, User: "u"}}, false},
		{"ssh without user", Profile{Name: "n", Driver: "postgres", SSH: SSH{Enabled: true, Host: "b"}}, false},
		{"ssh complete", Profile{Name: "n", Driver: "postgres", SSH: SSH{Enabled: true, Host: "b", User: "u"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.p.Validate(); (err == nil) != tt.ok {
				t.Errorf("Validate() = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestStoreSaveGetList(t *testing.T) {
	s, dir := newTestStore(t)
	saved, err := s.Save(Profile{Name: "a", Driver: "postgres", Password: "pw", SSH: SSH{Password: "sp", Passphrase: "pp"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" {
		t.Fatal("no id assigned")
	}

	got, err := s.Get(saved.ID)
	if err != nil || got.Password != "pw" || got.SSH.Password != "sp" || got.SSH.Passphrase != "pp" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	list := s.List()
	if len(list) != 1 || list[0].Password != "" || list[0].SSH.Password != "" || list[0].SSH.Passphrase != "" {
		t.Fatalf("List must not expose secrets: %+v", list)
	}

	info, err := os.Stat(filepath.Join(dir, "connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permission bits: Perm reports 0666 for any writable file.
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Errorf("connections.json mode = %o, want 600", perm)
	}

	// A new store reads what the first one wrote.
	again, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := again.Get(saved.ID); err != nil || p.Name != "a" || p.Password != "pw" {
		t.Fatalf("reloaded = %+v, %v", p, err)
	}
}

func TestStoreKeepSecrets(t *testing.T) {
	s, _ := newTestStore(t)
	saved, err := s.Save(Profile{Name: "a", Driver: "postgres", Password: "pw", SSH: SSH{Password: "sp", Passphrase: "pp"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	// An edit from the UI arrives without secrets: they must be kept.
	if _, err := s.Save(Profile{ID: saved.ID, Name: "renamed", Driver: "postgres"}, true); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(saved.ID)
	if got.Name != "renamed" || got.Password != "pw" || got.SSH.Password != "sp" || got.SSH.Passphrase != "pp" {
		t.Fatalf("secrets lost on edit: %+v", got)
	}
	// A new value replaces the old one.
	if _, err := s.Save(Profile{ID: saved.ID, Name: "renamed", Driver: "postgres", Password: "new"}, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(saved.ID); got.Password != "new" {
		t.Fatalf("password = %q, want new", got.Password)
	}
	// Without keepSecrets, empty means empty.
	if _, err := s.Save(Profile{ID: saved.ID, Name: "renamed", Driver: "postgres"}, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(saved.ID); got.Password != "" {
		t.Fatalf("password = %q, want empty", got.Password)
	}
}

func TestStoreSecretOptionsAndURI(t *testing.T) {
	s, _ := newTestStore(t)
	saved, err := s.Save(Profile{Name: "a", Driver: "cosmos", URI: "AccountKey=k", SecretOptions: map[string]string{"key": "s3cret"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if !list[0].HasURI || list[0].URI != "" || list[0].SecretOptions["key"] != "" {
		t.Fatalf("List must hide the URI and secret options: %+v", list[0])
	}
	// The UI sends back blank secrets for an unchanged profile.
	edited := list[0]
	edited.Name = "b"
	if _, err := s.Save(edited, true); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(saved.ID)
	if got.URI != "AccountKey=k" || got.SecretOptions["key"] != "s3cret" || got.Name != "b" {
		t.Fatalf("secrets lost: %+v", got)
	}
	// Clearing the connection string removes it.
	got.URI, got.HasURI = "", false
	if _, err := s.Save(got, true); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Get(saved.ID); again.URI != "" || again.HasURI {
		t.Fatalf("URI kept after clearing: %+v", again)
	}
}

func TestStoreDeleteReorderHostKey(t *testing.T) {
	s, _ := newTestStore(t)
	var ids []string
	for _, n := range []string{"a", "b", "c"} {
		p, err := s.Save(Profile{Name: n, Driver: "postgres"}, false)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}

	if err := s.Reorder([]string{ids[2], ids[0]}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range s.List() {
		names = append(names, p.Name)
	}
	// Listed ids come first in the given order, the rest keep their relative order.
	if !slices.Equal(names, []string{"c", "a", "b"}) {
		t.Errorf("order = %v", names)
	}

	if err := s.SetHostKey(ids[1], "SHA256:abc"); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Get(ids[1]); p.SSH.HostKey != "SHA256:abc" {
		t.Errorf("host key = %q", p.SSH.HostKey)
	}

	if err := s.Delete(ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ids[0]); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get deleted = %v, want ErrNotFound", err)
	}
	for _, err := range []error{s.Delete("missing"), s.SetHostKey("missing", "x")} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	}
}

func TestNewStoreErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "connections.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(dir); err == nil {
		t.Error("expected an error for a corrupt file")
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(filepath.Join(file, "sub")); err == nil {
		t.Error("expected an error when the directory cannot be created")
	}
}

func TestNewID(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := newID()
		if len(id) != 16 || seen[id] {
			t.Fatalf("bad or duplicate id %q", id)
		}
		seen[id] = true
	}
}

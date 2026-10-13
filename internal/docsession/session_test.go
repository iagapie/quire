package docsession

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/iagapie/quire/internal/dialect/mongodb"
	"github.com/iagapie/quire/internal/profile"
)

func mongoProfile(p profile.Profile) profile.Profile {
	if p.Driver == "" {
		p.Driver = mongodb.MongoDB
	}
	if p.Options == nil {
		p.Options = map[string]string{}
	}
	return p
}

func TestClientOptions(t *testing.T) {
	p := mongoProfile(profile.Profile{
		Driver:   mongodb.DocumentDB,
		Host:     "a.example.com:27017, b.example.com",
		User:     "u",
		Password: "pw",
		Options: map[string]string{
			mongodb.OptReplicaSet: "rs0", mongodb.OptReadPreference: "secondaryPreferred",
			mongodb.OptAuthSource: "shop", mongodb.OptAuthMechanism: "SCRAM-SHA-1", mongodb.OptDirect: "false",
		},
	})
	o, host, err := clientOptions(p)
	if err != nil {
		t.Fatal(err)
	}
	if host != "a.example.com:27017,b.example.com:27017" {
		t.Errorf("host = %q", host)
	}
	if !slices.Equal(o.Hosts, []string{"a.example.com:27017", "b.example.com:27017"}) {
		t.Errorf("hosts = %v", o.Hosts)
	}
	if o.ReplicaSet == nil || *o.ReplicaSet != "rs0" {
		t.Errorf("replica set = %v", o.ReplicaSet)
	}
	if o.RetryWrites == nil || *o.RetryWrites {
		t.Error("retryable writes must be disabled for DocumentDB")
	}
	if o.ReadPreference == nil || o.ReadPreference.Mode() != readpref.SecondaryPreferredMode {
		t.Errorf("read preference = %v", o.ReadPreference)
	}
	if o.Auth == nil || o.Auth.Username != "u" || o.Auth.Password != "pw" || o.Auth.AuthSource != "shop" || o.Auth.AuthMechanism != "SCRAM-SHA-1" {
		t.Errorf("auth = %+v", o.Auth)
	}
	if o.Direct != nil && *o.Direct {
		t.Error("direct connection must not be set")
	}
	if o.TLSConfig != nil {
		t.Error("TLS must be off unless enabled")
	}

	// MongoDB keeps retryable writes; a host without a port gets the profile port.
	o, host, err = clientOptions(mongoProfile(profile.Profile{Host: "db.local", Port: 27018}))
	if err != nil || host != "db.local:27018" || o.RetryWrites != nil {
		t.Errorf("mongodb: host=%q retryWrites=%v err=%v", host, o.RetryWrites, err)
	}
	if _, host, _ := clientOptions(mongoProfile(profile.Profile{})); host != "localhost:27017" {
		t.Errorf("default host = %q", host)
	}
}

// uriWithCredentials holds fake credentials used only to test URI parsing.
const uriWithCredentials = "mongodb://u2:p2@uri-host:27018/?authSource=admin&authMechanism=SCRAM-SHA-256" //nolint:gosec // G101: fake test credentials.

func TestClientOptionsURI(t *testing.T) {
	// A connection string replaces the host fields.
	o, host, err := clientOptions(mongoProfile(profile.Profile{
		URI: uriWithCredentials, Host: "ignored", Options: map[string]string{mongodb.OptDirect: "true"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if host != "uri-host:27018" {
		t.Errorf("host = %q", host)
	}
	if o.Direct == nil || !*o.Direct {
		t.Error("direct connection not applied")
	}
	if o.Auth == nil || o.Auth.Username != "u2" {
		t.Errorf("auth from URI = %+v", o.Auth)
	}

	// The user of the form overrides the URI, keeping its source and mechanism.
	o, _, err = clientOptions(mongoProfile(profile.Profile{URI: uriWithCredentials, User: "form", Password: "pw"}))
	if err != nil {
		t.Fatal(err)
	}
	if o.Auth.Username != "form" || o.Auth.AuthSource != "admin" || o.Auth.AuthMechanism != "SCRAM-SHA-256" {
		t.Errorf("override: auth=%+v", o.Auth)
	}
}

func TestClientOptionsX509(t *testing.T) {
	o, _, err := clientOptions(mongoProfile(profile.Profile{Options: map[string]string{mongodb.OptAuthMechanism: "MONGODB-X509"}}))
	if err != nil || o.Auth == nil || o.Auth.AuthMechanism != "MONGODB-X509" || o.Auth.PasswordSet {
		t.Errorf("x509 auth = %+v, %v", o.Auth, err)
	}
}

func TestClientOptionsErrors(t *testing.T) {
	tests := []profile.Profile{
		{URI: "http://not-mongo"},
		{Host: "h:1", Options: map[string]string{mongodb.OptReadPreference: "sometimes"}},
		{Host: "h:1", TLS: profile.TLS{Mode: profile.TLSVerifyFull, CAFile: "/does/not/exist.pem"}},
	}
	for i, p := range tests {
		if _, _, err := clientOptions(mongoProfile(p)); err == nil {
			t.Errorf("case %d: expected an error", i)
		}
	}
}

func TestClientOptionsTLS(t *testing.T) {
	tests := []struct {
		mode               string
		on, skip, verifyFn bool
	}{
		{profile.TLSDisable, false, false, false},
		{profile.TLSPrefer, false, false, false},
		{profile.TLSRequire, true, true, false},
		{profile.TLSVerifyCA, true, true, true},
		{profile.TLSVerifyFull, true, false, false},
	}
	for _, tt := range tests {
		o, _, err := clientOptions(mongoProfile(profile.Profile{Host: "h.example.com", TLS: profile.TLS{Mode: tt.mode, UseAWSBundle: true}}))
		if err != nil {
			t.Fatalf("%s: %v", tt.mode, err)
		}
		tc := o.TLSConfig
		if (tc != nil) != tt.on {
			t.Errorf("%s: TLS on = %v", tt.mode, tc != nil)
			continue
		}
		if tc == nil {
			continue
		}
		if tc.InsecureSkipVerify != tt.skip || (tc.VerifyConnection != nil) != tt.verifyFn || tc.RootCAs == nil || tc.ServerName != "h.example.com" {
			t.Errorf("%s: config = %+v", tt.mode, tc)
		}
	}
	// Replica set members are checked against their own names.
	o, _, err := clientOptions(mongoProfile(profile.Profile{Host: "a:1,b:2", TLS: profile.TLS{Mode: profile.TLSVerifyFull}}))
	if err != nil || o.TLSConfig.ServerName != "" {
		t.Errorf("seed list server name = %q, %v", o.TLSConfig.ServerName, err)
	}
}

func TestIsTransient(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{context.Canceled, false},
		{context.DeadlineExceeded, false},
		{fmt.Errorf("wrapped: %w", context.Canceled), false},
		{&net.OpError{Op: "read", Err: errors.New("boom")}, true},
		{errors.New("read tcp: connection reset by peer"), true},
		{errors.New("write: broken pipe"), true},
		{errors.New("unexpected EOF"), true},
		{errors.New("ssh tunnel to h:1: closed"), true},
		{errors.New("(Unauthorized) not authorized"), false},
	}
	for _, tt := range tests {
		if got := IsTransient(tt.err); got != tt.want {
			t.Errorf("IsTransient(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestRetryRead(t *testing.T) {
	old := retryBackoff
	retryBackoff = time.Millisecond
	t.Cleanup(func() { retryBackoff = old })
	transient := errors.New("connection reset")

	t.Run("succeeds after retries", func(t *testing.T) {
		calls, retries := 0, 0
		v, err := RetryRead(t.Context(), func(int, error) { retries++ }, func(context.Context) (int, error) {
			calls++
			if calls < 3 {
				return 0, transient
			}
			return 42, nil
		})
		if err != nil || v != 42 || calls != 3 || retries != 2 {
			t.Errorf("v=%d err=%v calls=%d retries=%d", v, err, calls, retries)
		}
	})

	t.Run("gives up after max attempts", func(t *testing.T) {
		calls := 0
		_, err := RetryRead(t.Context(), nil, func(context.Context) (int, error) {
			calls++
			return 0, transient
		})
		if !errors.Is(err, transient) || calls != 4 {
			t.Errorf("err=%v calls=%d", err, calls)
		}
	})

	t.Run("does not retry other errors", func(t *testing.T) {
		calls := 0
		permanent := errors.New("not authorized")
		_, err := RetryRead(t.Context(), nil, func(context.Context) (int, error) {
			calls++
			return 0, permanent
		})
		if !errors.Is(err, permanent) || calls != 1 {
			t.Errorf("err=%v calls=%d", err, calls)
		}
	})

	t.Run("stops when cancelled while waiting", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		start := time.Now()
		_, err := RetryRead(ctx, func(int, error) { cancel() }, func(context.Context) (int, error) {
			return 0, transient
		})
		if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
			t.Errorf("err=%v after %v", err, time.Since(start))
		}
	})
}

func TestBeginCancel(t *testing.T) {
	s := &Session{Profile: profile.Profile{QueryTimeoutMs: 60_000}, running: map[string]context.CancelFunc{}}

	ctx, done := s.Begin(t.Context(), "op")
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Minute {
		t.Errorf("deadline = %v, %v", deadline, ok)
	}
	if !s.Cancel("op") {
		t.Fatal("Cancel returned false for a running operation")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("ctx.Err() = %v", ctx.Err())
	}
	done()
	if s.Cancel("op") || s.Cancel("unknown") {
		t.Error("Cancel returned true for a finished/unknown operation")
	}

	// Starting an operation with the same id cancels the previous one.
	first, done1 := s.Begin(t.Context(), "page")
	_, done2 := s.Begin(t.Context(), "page")
	if first.Err() == nil {
		t.Error("previous operation was not cancelled")
	}
	done1()
	done2()

	// Operations without an id are not tracked.
	_, done3 := s.Begin(t.Context(), "")
	if len(s.running) != 0 {
		t.Errorf("running = %v", s.running)
	}
	done3()
}

func TestHealth(t *testing.T) {
	var got []Health
	s := &Session{onHealth: func(h Health) { got = append(got, h) }}
	s.health.Store(HealthOnline)
	s.setHealth(HealthOnline)
	s.setHealth(HealthReconnecting)
	s.setHealth(HealthReconnecting)
	s.setHealth(HealthOnline)
	if !slices.Equal(got, []Health{HealthReconnecting, HealthOnline}) || s.Health() != HealthOnline {
		t.Errorf("transitions = %v, health = %v", got, s.Health())
	}
}

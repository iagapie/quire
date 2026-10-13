package tunnel

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshServer is a minimal SSH server supporting password and public key auth and
// direct-tcpip forwarding (what `ssh -L` and our dialer use).
type sshServer struct {
	host string
	port int

	mu      sync.Mutex
	conns   []net.Conn
	accepts int
}

// dropAll kills every SSH session, like a network failure would.
func (s *sshServer) dropAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.conns = nil
}

func (s *sshServer) sessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepts
}

func startSSHServer(t *testing.T, authorized ...ssh.PublicKey) *sshServer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	conf := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == "pw" {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			for _, k := range authorized {
				if bytes.Equal(k.Marshal(), key.Marshal()) {
					return nil, nil
				}
			}
			return nil, errors.New("unknown key")
		},
	}
	conf.AddHostKey(signer)
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := &sshServer{}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			srv.mu.Lock()
			srv.conns = append(srv.conns, nc)
			srv.accepts++
			srv.mu.Unlock()
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(nc, conf)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nch := range chans {
					if nch.ChannelType() != "direct-tcpip" {
						_ = nch.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					var p struct {
						Host     string
						Port     uint32
						OrigHost string
						OrigPort uint32
					}
					if err := ssh.Unmarshal(nch.ExtraData(), &p); err != nil {
						_ = nch.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					var nd net.Dialer
					target, err := nd.DialContext(t.Context(), "tcp", net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
					if err != nil {
						_ = nch.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					ch, creqs, err := nch.Accept()
					if err != nil {
						_ = target.Close()
						continue
					}
					go ssh.DiscardRequests(creqs)
					go func() { _, _ = io.Copy(ch, target); _ = ch.Close() }()
					go func() { _, _ = io.Copy(target, ch); _ = target.Close() }()
				}
			}()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	srv.host, srv.port = addr.IP.String(), addr.Port
	return srv
}

func startEcho(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return ln.Addr().String()
}

func TestDialerSupportsDeadlines(t *testing.T) {
	srv := startSSHServer(t)
	host, port := srv.host, srv.port
	echo := startEcho(t)
	var accepted string
	d := New(Config{Host: host, Port: port, User: "u", Auth: "password", Password: "pw", OnNewHostKey: func(fp string) { accepted = fp }})
	t.Cleanup(func() { _ = d.Close() })

	conn, err := d.DialContext(t.Context(), "tcp", echo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if accepted == "" {
		t.Error("host key was not reported")
	}
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo = %q, %v", buf, err)
	}
	// A read with nothing to receive must time out instead of hanging.
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var ne net.Error
	if _, err := conn.Read(buf); !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestDialerHostKeyMismatch(t *testing.T) {
	srv := startSSHServer(t)
	host, port := srv.host, srv.port
	d := New(Config{Host: host, Port: port, User: "u", Auth: "password", Password: "pw", HostKey: "SHA256:wrong"})
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Check(t.Context()); !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("err = %v", err)
	}
}

func TestForwardRelaysThroughTunnel(t *testing.T) {
	srv := startSSHServer(t)
	echo := startEcho(t)
	d := New(Config{Host: srv.host, Port: srv.port, User: "u", Auth: "password", Password: "pw"})
	t.Cleanup(func() { _ = d.Close() })

	h, p, _ := net.SplitHostPort(echo)
	port, _ := strconv.Atoi(p)
	lhost, lport, err := d.Forward(t.Context(), h, port)
	if err != nil {
		t.Fatal(err)
	}
	if lhost != "127.0.0.1" || lport == 0 {
		t.Fatalf("forward = %s:%d", lhost, lport)
	}
	roundTrip := func() {
		t.Helper()
		var nd net.Dialer
		c, err := nd.DialContext(t.Context(), "tcp", net.JoinHostPort(lhost, strconv.Itoa(lport)))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Write([]byte("hi")); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 2)
		if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hi" {
			t.Fatalf("echo = %q, %v", buf, err)
		}
	}
	roundTrip()
	// The same forward keeps working after the SSH session drops.
	srv.dropAll()
	time.Sleep(50 * time.Millisecond)
	roundTrip()

	_ = d.Close()
	var nd net.Dialer
	if c, err := nd.DialContext(t.Context(), "tcp", net.JoinHostPort(lhost, strconv.Itoa(lport))); err == nil {
		_ = c.Close()
		t.Error("the forward must stop listening when the tunnel closes")
	}
}

func ping(t *testing.T, d *Dialer, addr string) {
	t.Helper()
	conn, err := d.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
}

func TestDialerReconnectsAfterDrop(t *testing.T) {
	srv := startSSHServer(t)
	echo := startEcho(t)
	d := New(Config{Host: srv.host, Port: srv.port, User: "u", Auth: "password", Password: "pw"})
	t.Cleanup(func() { _ = d.Close() })

	ping(t, d, echo)
	ping(t, d, echo)
	if n := srv.sessions(); n != 1 {
		t.Fatalf("dials share one SSH session, got %d", n)
	}

	// Kill the SSH session: the next dial must transparently open a new one.
	srv.dropAll()
	time.Sleep(50 * time.Millisecond)
	ping(t, d, echo)
	if n := srv.sessions(); n != 2 {
		t.Errorf("sessions after drop = %d, want 2", n)
	}
}

func TestDeadlineConnClosesTunnel(t *testing.T) {
	srv := startSSHServer(t)
	echo := startEcho(t)
	d := New(Config{Host: srv.host, Port: srv.port, User: "u", Auth: "password", Password: "pw"})
	t.Cleanup(func() { _ = d.Close() })

	conn, err := d.DialContext(t.Context(), "tcp", echo)
	if err != nil {
		t.Fatal(err)
	}
	if conn.LocalAddr() == nil || conn.RemoteAddr() == nil {
		t.Error("addresses must be reported")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("x")); err == nil {
		t.Error("write after close must fail")
	}

	// When the tunnel dies the local side sees EOF instead of hanging.
	conn, err = d.DialContext(t.Context(), "tcp", echo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	srv.dropAll()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = conn.Read(make([]byte, 1))
	var ne net.Error
	if err == nil || (errors.As(err, &ne) && ne.Timeout()) {
		t.Errorf("read after tunnel drop = %v, want EOF/closed", err)
	}
}

func TestDialerAuth(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	srv := startSSHServer(t, signer.PublicKey())
	echo := startEcho(t)

	dir := t.TempDir()
	plain, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	plainPath, encPath := filepath.Join(dir, "id"), filepath.Join(dir, "id_enc")
	if err := os.WriteFile(plainPath, pem.EncodeToMemory(plain), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(encPath, pem.EncodeToMemory(encrypted), 0o600); err != nil {
		t.Fatal(err)
	}

	ok := []Config{
		{Auth: "key", PrivateKeyPath: plainPath},
		{Auth: "key", PrivateKeyPath: encPath, Passphrase: "secret"},
		{Auth: "password", Password: "pw"},
	}
	for _, cfg := range ok {
		cfg.Host, cfg.Port, cfg.User = srv.host, srv.port, "u"
		d := New(cfg)
		ping(t, d, echo)
		_ = d.Close()
	}

	t.Setenv("SSH_AUTH_SOCK", "")
	bad := []struct {
		cfg  Config
		want string
	}{
		{Config{Auth: "password", Password: "wrong"}, "handshake"},
		{Config{Auth: "key", PrivateKeyPath: encPath, Passphrase: "nope"}, "parse SSH key"},
		{Config{Auth: "key", PrivateKeyPath: encPath}, "parse SSH key"},
		{Config{Auth: "key", PrivateKeyPath: filepath.Join(dir, "missing")}, "read SSH key"},
		{Config{Auth: "key"}, "private key path is required"},
		{Config{Auth: "agent"}, "SSH agent not available"},
	}
	for _, b := range bad {
		b.cfg.Host, b.cfg.Port, b.cfg.User = srv.host, srv.port, "u"
		d := New(b.cfg)
		if err := d.Check(t.Context()); err == nil || !strings.Contains(err.Error(), b.want) {
			t.Errorf("%+v: err = %v, want %q", b.cfg, err, b.want)
		}
		_ = d.Close()
	}

	d := New(Config{Host: srv.host, Port: srv.port, User: "u", Auth: "password", Password: "pw"})
	_ = d.Close()
	if _, err := d.DialContext(t.Context(), "tcp", echo); err == nil {
		t.Error("dial on a closed dialer must fail")
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := expandHome(" ~/.ssh/id "); got != filepath.Join(home, ".ssh/id") {
		t.Errorf("expandHome = %q", got)
	}
	if got := expandHome("/abs/key"); got != "/abs/key" {
		t.Errorf("expandHome = %q", got)
	}
}

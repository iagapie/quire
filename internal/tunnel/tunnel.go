// Package tunnel provides a self-healing SSH dialer. Drivers that accept a dialer dial
// every connection through the SSH client directly, without a local port, and a
// dropped SSH session is re-established transparently on the next dial. For drivers
// that cannot take a dialer, Forward opens a local port whose connections go through
// the same self-healing client.
package tunnel

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Config describes how to reach the SSH server.
type Config struct {
	Host           string
	Port           int
	User           string
	Auth           string // password | key | agent
	Password       string
	PrivateKeyPath string
	Passphrase     string
	// HostKey is the expected SHA256 fingerprint; empty means trust on first use.
	HostKey string
	// OnNewHostKey is called with the fingerprint accepted on first use.
	OnNewHostKey func(fingerprint string)
}

// ErrHostKeyMismatch means the server presented a different key than the stored one.
var ErrHostKeyMismatch = errors.New("SSH host key does not match the stored fingerprint")

const (
	keepAliveInterval = 15 * time.Second
	keepAliveTimeout  = 10 * time.Second
	dialTimeout       = 15 * time.Second
)

// Dialer dials TCP connections through an SSH client, reconnecting on demand.
type Dialer struct {
	cfg Config

	mu        sync.Mutex
	client    *ssh.Client
	done      chan struct{}
	closed    bool
	listeners []net.Listener
}

// New creates a dialer. No network activity happens until the first dial.
func New(cfg Config) *Dialer {
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	return &Dialer{cfg: cfg}
}

// DialContext dials address through the SSH server.
func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	for attempt := 0; ; attempt++ {
		c, err := d.sshClient(ctx)
		if err != nil {
			return nil, err
		}
		conn, err := dialVia(ctx, c, network, address)
		if err == nil {
			return withDeadlines(conn), nil
		}
		// A failure on the SSH transport itself means the session is dead: drop it and
		// retry once with a fresh one. Failures reported by the remote side are returned.
		var openErr *ssh.OpenChannelError
		if errors.As(err, &openErr) || attempt > 0 || ctx.Err() != nil {
			return nil, fmt.Errorf("ssh tunnel to %s: %w", address, err)
		}
		d.drop(c)
	}
}

func dialVia(ctx context.Context, c *ssh.Client, network, address string) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := c.Dial(network, address)
		ch <- result{conn, err}
	}()
	select {
	case r := <-ch:
		return r.conn, r.err
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.conn != nil {
				_ = r.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// Check opens the SSH session, returning an error if it cannot be established.
func (d *Dialer) Check(ctx context.Context) error {
	_, err := d.sshClient(ctx)
	return err
}

// Close tears down the SSH session.
func (d *Dialer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	for _, l := range d.listeners {
		_ = l.Close()
	}
	d.listeners = nil
	if d.client != nil {
		close(d.done)
		err := d.client.Close()
		d.client = nil
		return err
	}
	return nil
}

func (d *Dialer) sshClient(ctx context.Context) (*ssh.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, errors.New("ssh tunnel closed")
	}
	if d.client != nil {
		return d.client, nil
	}
	c, err := d.connect(ctx)
	if err != nil {
		return nil, err
	}
	d.client = c
	d.done = make(chan struct{})
	go d.keepAlive(c, d.done)
	return c, nil
}

// drop discards c if it is still the active client.
func (d *Dialer) drop(c *ssh.Client) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client == c {
		close(d.done)
		d.client = nil
	}
	_ = c.Close()
}

func (d *Dialer) keepAlive(c *ssh.Client, done chan struct{}) {
	t := time.NewTicker(keepAliveInterval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			errc := make(chan error, 1)
			go func() {
				_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
				errc <- err
			}()
			select {
			case err := <-errc:
				if err != nil {
					d.drop(c)
					return
				}
			case <-time.After(keepAliveTimeout):
				d.drop(c)
				return
			case <-done:
				return
			}
		}
	}
}

func (d *Dialer) connect(ctx context.Context) (*ssh.Client, error) {
	auth, err := d.authMethods(ctx)
	if err != nil {
		return nil, err
	}
	conf := &ssh.ClientConfig{
		User:            d.cfg.User,
		Auth:            auth,
		HostKeyCallback: d.hostKeyCallback,
		Timeout:         dialTimeout,
	}
	addr := net.JoinHostPort(d.cfg.Host, strconv.Itoa(d.cfg.Port))
	nd := net.Dialer{Timeout: dialTimeout, KeepAlive: keepAliveInterval}
	conn, err := nd.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("ssh connect %s: %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, conf)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ssh handshake %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(cc, chans, reqs), nil
}

func (d *Dialer) hostKeyCallback(_ string, _ net.Addr, key ssh.PublicKey) error {
	fp := ssh.FingerprintSHA256(key)
	if d.cfg.HostKey == "" {
		d.cfg.HostKey = fp
		if d.cfg.OnNewHostKey != nil {
			d.cfg.OnNewHostKey(fp)
		}
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(fp), []byte(d.cfg.HostKey)) != 1 {
		return fmt.Errorf("%w (expected %s, got %s)", ErrHostKeyMismatch, d.cfg.HostKey, fp)
	}
	return nil
}

func (d *Dialer) authMethods(ctx context.Context) ([]ssh.AuthMethod, error) {
	switch d.cfg.Auth {
	case "password":
		return []ssh.AuthMethod{
			ssh.Password(d.cfg.Password),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = d.cfg.Password
				}
				return answers, nil
			}),
		}, nil
	case "agent":
		sock := os.Getenv("SSH_AUTH_SOCK")
		if sock == "" {
			return nil, errors.New("SSH agent not available (SSH_AUTH_SOCK is empty)")
		}
		var nd net.Dialer
		conn, err := nd.DialContext(ctx, "unix", sock)
		if err != nil {
			return nil, fmt.Errorf("connect to SSH agent: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeysCallback(agent.NewClient(conn).Signers)}, nil
	default:
		path := expandHome(d.cfg.PrivateKeyPath)
		if path == "" {
			return nil, errors.New("SSH private key path is required")
		}
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read SSH key: %w", err)
		}
		var signer ssh.Signer
		if d.cfg.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(d.cfg.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(pem)
		}
		if err != nil {
			return nil, fmt.Errorf("parse SSH key: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
}

func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// Forward listens on a free local port and relays every accepted connection to
// host:port through the SSH server. It returns the local address. The listener stays
// open until the dialer is closed; each connection dials through the SSH session, so
// a dropped tunnel is re-established on the next connection.
func (d *Dialer) Forward(ctx context.Context, host string, port int) (string, int, error) {
	if _, err := d.sshClient(ctx); err != nil {
		return "", 0, err
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", 0, fmt.Errorf("local port forward: %w", err)
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		_ = ln.Close()
		return "", 0, errors.New("ssh tunnel closed")
	}
	d.listeners = append(d.listeners, ln)
	d.mu.Unlock()
	target := net.JoinHostPort(host, strconv.Itoa(port))
	go func() {
		for {
			local, err := ln.Accept()
			if err != nil {
				return
			}
			go d.relay(local, target)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, nil
}

func (d *Dialer) relay(local net.Conn, target string) {
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	remote, err := d.DialContext(ctx, "tcp", target)
	cancel()
	if err != nil {
		_ = local.Close()
		return
	}
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = local.Close()
			_ = remote.Close()
		})
	}
	go func() {
		_, _ = io.Copy(remote, local)
		closeBoth()
	}()
	_, _ = io.Copy(local, remote)
	closeBoth()
}

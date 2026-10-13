package oracle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	goora "github.com/sijms/go-ora/v2"
	"github.com/sijms/go-ora/v2/network"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
)

// Clients of an Oracle connection (the "client" option).
const (
	ClientInstant = ""        // godror over the Oracle Instant Client
	ClientBuiltin = "builtin" // go-ora, written in Go: nothing to install
)

func builtin(p profile.Profile) bool { return p.Options["client"] == ClientBuiltin }

// dialFunc adapts a dial function to go-ora's dialer.
type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

func (f dialFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

// openBuiltin connects with go-ora. It speaks the Oracle protocol itself, so an SSH
// tunnel goes through the SSH client with no local port.
func openBuiltin(p profile.Profile, n dialect.Network) (*sql.DB, error) {
	u, err := builtinURL(p)
	if err != nil {
		return nil, err
	}
	c, ok := goora.NewConnector(u).(*goora.OracleConnector)
	if !ok {
		return nil, errors.New("go-ora: unexpected connector")
	}
	dial := n.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 15 * time.Second}).DialContext
	}
	c.Dialer(watchedDial(dial))
	db := sql.OpenDB(watchedConnector{c})
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(45 * time.Second)
	return db, nil
}

// builtinURL builds the go-ora URL from the profile: host and port, or a connection
// string (an oracle:// URL, an Easy Connect string or a descriptor).
func builtinURL(p profile.Profile) (string, error) {
	opts := map[string]string{"PROGRAM": "Quire"}
	if role := p.Options["role"]; role != "" {
		opts["DBA PRIVILEGE"] = strings.ToUpper(role)
	}
	// A wallet in the configuration folder means TCPS (Autonomous Database).
	if dir := expand(p.Options["configDir"]); dir != "" && hasWallet(dir) {
		opts["SSL"], opts["SSL VERIFY"], opts["WALLET"] = "true", "true", dir
	}
	uri := strings.TrimSpace(p.URI)
	switch {
	case strings.HasPrefix(strings.ToLower(uri), "oracle://"):
		return uri, nil
	case strings.HasPrefix(uri, "("):
		return goora.BuildJDBC(p.User, p.Password, uri, opts), nil
	case uri != "":
		host, port, service, ok := easyConnect(uri)
		if !ok {
			return "", fmt.Errorf("%q looks like a TNS alias: the built-in client cannot read tnsnames.ora. "+
				"Use host:port/service, a (DESCRIPTION=…) descriptor, or the Oracle Instant Client", uri)
		}
		return goora.BuildUrl(host, port, service, p.User, p.Password, opts), nil
	}
	host, port := p.Host, p.Port
	if host == "" {
		host = "localhost"
	}
	if port == 0 {
		port = 1521
	}
	service := p.Database
	if p.Options["sid"] == "true" {
		opts["SID"], service = p.Database, ""
	}
	return goora.BuildUrl(host, port, service, p.User, p.Password, opts), nil
}

// easyConnect reads [//]host[:port][/service].
func easyConnect(s string) (string, int, string, bool) {
	s = strings.TrimPrefix(s, "//")
	addr, service, _ := strings.Cut(s, "/")
	if addr == "" || strings.ContainsAny(addr, " ()=") {
		return "", 0, "", false
	}
	host, port := addr, 1521
	if h, ps, err := net.SplitHostPort(addr); err == nil {
		n, err := strconv.Atoi(ps)
		if err != nil {
			return "", 0, "", false
		}
		host, port = h, n
	} else if service == "" {
		return "", 0, "", false // a bare word: a TNS alias
	}
	return host, port, service, true
}

func hasWallet(dir string) bool {
	for _, f := range []string{"cwallet.sso", "ewallet.p12"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			return true
		}
	}
	return false
}

// isBuiltin reports whether c is a go-ora connection.
func isBuiltin(c *sql.Conn) bool {
	builtin := false
	_ = c.Raw(func(dc any) error {
		switch dc.(type) {
		case *watchedConn, *goora.Connection:
			builtin = true
		}
		return nil
	})
	return builtin
}

// readOutputPLSQL reads DBMS_OUTPUT one line at a time (go-ora has no helper).
func readOutputPLSQL(ctx context.Context, c *sql.Conn) ([]string, error) {
	var out []string
	for range 100_000 {
		var line string
		var status int64
		_, err := c.ExecContext(ctx, `BEGIN DBMS_OUTPUT.GET_LINE(:1, :2); END;`,
			goora.Out{Dest: &line, Size: 32767}, goora.Out{Dest: &status})
		if err != nil {
			return out, err
		}
		if status != 0 {
			break
		}
		out = append(out, line)
	}
	return out, nil
}

// builtinTransient reports go-ora's network and server errors worth retrying.
func builtinTransient(err error) (transient, known bool) {
	if errors.Is(err, network.ErrConnReset) {
		return true, true
	}
	var oe *network.OracleError
	if errors.As(err, &oe) {
		return transientCode(oe.ErrCode) || oe.Bad(), true
	}
	if errors.Is(err, driver.ErrBadConn) {
		return true, true
	}
	return false, false
}

package dialect

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
)

// NetworkError reports whether err comes from the network rather than the server: a
// dropped connection, a timeout, a refused dial or a broken SSH tunnel. Drivers use it
// as the base of Transient.
func NetworkError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// sql.ErrConnDone: the driver gave up on a connection held with *sql.Conn.
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"connection reset", "broken pipe", "connection refused", "unexpected eof", "i/o timeout",
		"use of closed network connection", "ssh tunnel", "conn closed", "connection closed", "bad connection",
		"no route to host", "network is unreachable", "server closed the connection",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

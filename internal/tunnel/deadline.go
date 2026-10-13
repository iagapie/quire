package tunnel

import (
	"io"
	"net"
	"sync"
)

// withDeadlines adapts an SSH channel connection, which rejects SetDeadline, into a
// net.Conn with full deadline support (database drivers set a deadline on every
// read and write). Data is relayed through an in-memory pipe; closing either side
// closes both.
func withDeadlines(remote net.Conn) net.Conn {
	local, inner := net.Pipe()
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = inner.Close()
			_ = remote.Close()
		})
	}
	go func() {
		_, _ = io.Copy(remote, inner)
		closeBoth()
	}()
	go func() {
		_, _ = io.Copy(inner, remote)
		closeBoth()
	}()
	return &pipeConn{Conn: local, remote: remote}
}

// pipeConn reports the tunnel endpoints as its addresses.
type pipeConn struct {
	net.Conn
	remote net.Conn
}

func (c *pipeConn) LocalAddr() net.Addr  { return c.remote.LocalAddr() }
func (c *pipeConn) RemoteAddr() net.Addr { return c.remote.RemoteAddr() }

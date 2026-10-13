package oracle

import (
	"context"
	"database/sql/driver"
	"net"
	"sync"
	"sync/atomic"
	"time"

	goora "github.com/sijms/go-ora/v2"
)

// go-ora answers a cancelled context with a break, then waits for the server without
// a deadline: when the server is gone or frozen, that wait never ends. The watchdog
// closes the network connection once the context has been done for watchdogGrace
// while a call is still running, as pgx does, so the call fails and the pool drops
// the connection.
var watchdogGrace = 5 * time.Second

type netSlotKey struct{}

// netSlot receives the network connection go-ora dials for one driver connection.
type netSlot struct {
	mu   sync.Mutex
	conn net.Conn
}

func (s *netSlot) set(c net.Conn) {
	s.mu.Lock()
	s.conn = c
	s.mu.Unlock()
}

func (s *netSlot) close() {
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// watchedDial records the dialed connection in the slot of the Connect context.
func watchedDial(dial dialFunc) dialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		c, err := dial(ctx, network, address)
		if slot, ok := ctx.Value(netSlotKey{}).(*netSlot); ok && err == nil {
			slot.set(c)
		}
		return c, err
	}
}

// watchedConnector wraps go-ora's connector.
type watchedConnector struct {
	*goora.OracleConnector
}

func (w watchedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	slot := &netSlot{}
	c, err := w.OracleConnector.Connect(context.WithValue(ctx, netSlotKey{}, slot))
	if err != nil {
		return nil, err
	}
	oc, ok := c.(*goora.Connection)
	if !ok {
		return c, nil
	}
	return &watchedConn{Connection: oc, slot: slot}, nil
}

// watchedConn is a go-ora connection whose calls are guarded by the watchdog.
type watchedConn struct {
	*goora.Connection
	slot   *netSlot
	killed atomic.Bool
}

// guard watches ctx during one call; the returned function ends the call. Only that
// call, still running watchdogGrace after its context is done, closes the connection.
func (c *watchedConn) guard(ctx context.Context) func() {
	var running atomic.Bool
	running.Store(true)
	var mu sync.Mutex
	var timer *time.Timer
	stop := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		timer = time.AfterFunc(watchdogGrace, func() {
			if running.Load() {
				c.killed.Store(true)
				c.slot.close()
			}
		})
	})
	return func() {
		running.Store(false)
		stop()
		mu.Lock()
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
	}
}

func (c *watchedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	defer c.guard(ctx)()
	return c.Connection.ExecContext(ctx, query, args)
}

func (c *watchedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	end := c.guard(ctx)
	rows, err := c.Connection.QueryContext(ctx, query, args)
	end()
	if err != nil {
		return nil, err
	}
	if ds, ok := rows.(*goora.DataSet); ok {
		// Fetching more rows reads from the server too: keep watching the query
		// context until the rows are closed.
		return &watchedRows{DataSet: ds, conn: c, ctx: ctx}, nil
	}
	return rows, nil
}

func (c *watchedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	defer c.guard(ctx)()
	return c.Connection.BeginTx(ctx, opts)
}

func (c *watchedConn) Ping(ctx context.Context) error {
	defer c.guard(ctx)()
	return c.Connection.Ping(ctx)
}

// ResetSession and IsValid drop a connection the watchdog closed.
func (c *watchedConn) ResetSession(ctx context.Context) error {
	if c.killed.Load() {
		return driver.ErrBadConn
	}
	return c.Connection.ResetSession(ctx)
}

func (c *watchedConn) IsValid() bool { return !c.killed.Load() }

// watchedRows guards each fetch with the context of its query.
type watchedRows struct {
	*goora.DataSet
	conn *watchedConn
	ctx  context.Context //nolint:containedctx // the query context, as database/sql keeps it for the rows
}

func (r *watchedRows) Next(dest []driver.Value) error {
	defer r.conn.guard(r.ctx)()
	return r.DataSet.Next(dest)
}

var (
	_ driver.QueryerContext     = (*watchedConn)(nil)
	_ driver.ExecerContext      = (*watchedConn)(nil)
	_ driver.ConnBeginTx        = (*watchedConn)(nil)
	_ driver.Pinger             = (*watchedConn)(nil)
	_ driver.SessionResetter    = (*watchedConn)(nil)
	_ driver.Validator          = (*watchedConn)(nil)
	_ driver.NamedValueChecker  = (*watchedConn)(nil)
	_ driver.ConnPrepareContext = (*watchedConn)(nil)
)

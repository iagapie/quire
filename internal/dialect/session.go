package dialect

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Plan is how a driver shows the execution plan of a statement.
type Plan struct {
	// Before runs first on the connection (SET SHOWPLAN_ALL ON, EXPLAIN PLAN FOR …).
	Before []string
	// Query returns the plan rows; empty when the driver cannot explain.
	Query string
	// After restores the session, even when Query failed.
	After []string
}

// Interrupter is implemented by drivers whose client gives up on a cancelled
// statement by closing the connection (MySQL). The statement is then stopped from
// another connection instead, so the tab connection and its session survive.
type Interrupter interface {
	// SessionID returns the server id of the connection.
	SessionID(ctx context.Context, c *sql.Conn) (string, error)
	// Interrupt stops the statement running on the connection with that id.
	Interrupt(ctx context.Context, q Querier, id string) error
}

// ServerOutput is implemented by drivers whose server code prints through a buffer
// read by the client (Oracle DBMS_OUTPUT).
type ServerOutput interface {
	// EnableOutput turns the buffer on for a new connection.
	EnableOutput(ctx context.Context, c *sql.Conn) error
	// ReadOutput returns and clears the buffered lines.
	ReadOutput(ctx context.Context, c *sql.Conn) ([]string, error)
}

type messagesKey struct{}

// Messages collects what the server prints while a statement runs (PRINT, RAISERROR
// with a low severity, notices, row counts of a batch).
type Messages struct {
	mu    sync.Mutex
	lines []string
}

// WithMessages returns a context that collects server messages into m.
func WithMessages(ctx context.Context, m *Messages) context.Context {
	return context.WithValue(ctx, messagesKey{}, m)
}

// Notify records a server message for the statement running with ctx.
func Notify(ctx context.Context, msg string) {
	if m, ok := ctx.Value(messagesKey{}).(*Messages); ok {
		m.mu.Lock()
		m.lines = append(m.lines, msg)
		m.mu.Unlock()
	}
}

// Take returns the collected messages and clears them.
func (m *Messages) Take() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.lines
	m.lines = nil
	return out
}

// InitConnector runs statements on every new connection of a pool.
type InitConnector struct {
	driver.Connector
	Init []string
}

// Connect opens a connection and runs the init statements on it.
func (c *InitConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil || len(c.Init) == 0 {
		return conn, err
	}
	ex, ok := conn.(driver.ExecerContext)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("the driver cannot run session settings")
	}
	for _, stmt := range c.Init {
		if _, err := ex.ExecContext(ctx, stmt, nil); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return conn, nil
}

// Strings reads the first column of every row.
func Strings(ctx context.Context, q Querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s.String)
	}
	return out, rows.Err()
}

// Records reads every row into a map keyed by lower case column name; NULL reads as
// "". It suits catalog views whose columns differ between server versions.
func Records(ctx context.Context, q Querier, query string, args ...any) ([]map[string]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]string
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]string, len(cols))
		for i, c := range cols {
			m[strings.ToLower(c)] = vals[i].String
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

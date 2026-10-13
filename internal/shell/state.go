package shell

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/dop251/goja"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/iagapie/quire/internal/docsession"
)

// ErrReadOnly is raised by write operations on read-only connections.
var ErrReadOnly = errors.New("this connection is read-only")

// Env is what a script runs against.
type Env struct {
	Session  *docsession.Session
	Database string
	// OnRetry is called when a read is retried after a transient network error.
	OnRetry func(attempt int, err error)
}

// State is one script execution: a fresh JS runtime bound to a session.
type State struct {
	rt     *goja.Runtime
	ctx    context.Context
	env    Env
	client *mongo.Client
	dbName string

	wrapped     map[*goja.Object]any
	cursors     map[*goja.Object]*cursorSpec
	dbs         map[*goja.Object]string
	collections map[*goja.Object][2]string
	open        []*mongo.Cursor
	output      []string
}

// NewState prepares a runtime with the shell API installed.
func NewState(ctx context.Context, env Env) *State {
	s := &State{
		rt:          goja.New(),
		ctx:         ctx,
		env:         env,
		dbName:      env.Database,
		wrapped:     map[*goja.Object]any{},
		cursors:     map[*goja.Object]*cursorSpec{},
		dbs:         map[*goja.Object]string{},
		collections: map[*goja.Object][2]string{},
	}
	if env.Session != nil {
		s.client = env.Session.Client
	}
	if s.dbName == "" {
		s.dbName = "test"
	}
	s.installTypes()
	s.installGlobals()
	_ = s.rt.Set("db", s.newDB(s.dbName))
	return s
}

// Close releases cursors opened by the script.
func (s *State) Close() {
	for _, c := range s.open {
		_ = c.Close(context.Background())
	}
	s.open = nil
}

// Database returns the current database (it changes with use()).
func (s *State) Database() string { return s.dbName }

func (s *State) readOnly() bool {
	return s.env.Session != nil && s.env.Session.Profile.ReadOnly
}

func (s *State) requireWrite() {
	if s.readOnly() {
		s.throw(ErrReadOnly)
	}
}

func (s *State) requireClient() {
	if s.client == nil {
		s.throw(errors.New("not connected"))
	}
}

// throw raises err as a JS Error.
func (s *State) throw(err error) {
	msg := err.Error()
	switch {
	case errors.Is(err, context.Canceled):
		msg = "operation cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		msg = "operation timed out"
	}
	e, _ := s.rt.New(s.rt.Get("Error"), s.rt.ToValue(msg))
	panic(e)
}

func (s *State) print(text string) {
	s.output = append(s.output, text)
}

func (s *State) display(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) {
		return "undefined"
	}
	if o, ok := v.(*goja.Object); ok {
		if name, ok := s.dbs[o]; ok {
			return name
		}
		if c, ok := s.collections[o]; ok {
			return c[0] + "." + c[1]
		}
		if _, ok := s.cursors[o]; ok {
			return "Cursor"
		}
		if _, ok := s.wrapped[o]; !ok && o.ClassName() == "Error" {
			return o.String()
		}
	}
	if str, ok := v.Export().(string); ok {
		return str
	}
	return Format(s.toBSON(v), "  ")
}

func (s *State) installGlobals() {
	rt := s.rt
	printFn := func(c goja.FunctionCall) goja.Value {
		parts := make([]string, len(c.Arguments))
		for i, a := range c.Arguments {
			parts[i] = s.display(a)
		}
		s.print(strings.Join(parts, " "))
		return goja.Undefined()
	}
	_ = rt.Set("print", printFn)
	_ = rt.Set("printjson", printFn)
	console := rt.NewObject()
	for _, k := range []string{"log", "info", "warn", "error", "debug"} {
		_ = console.Set(k, printFn)
	}
	_ = rt.Set("console", console)
	_ = rt.Set("tojson", func(c goja.FunctionCall) goja.Value {
		return rt.ToValue(Format(s.toBSON(c.Argument(0)), "  "))
	})
	_ = rt.Set("sleep", func(c goja.FunctionCall) goja.Value {
		select {
		case <-time.After(time.Duration(c.Argument(0).ToInteger()) * time.Millisecond):
		case <-s.ctx.Done():
			s.throw(s.ctx.Err())
		}
		return goja.Undefined()
	})
	_ = rt.Set("use", func(c goja.FunctionCall) goja.Value {
		name := strings.TrimSpace(c.Argument(0).String())
		if name == "" || goja.IsUndefined(c.Argument(0)) {
			panic(rt.NewTypeError("use() requires a database name"))
		}
		s.dbName = name
		_ = rt.Set("db", s.newDB(name))
		s.print("switched to db " + name)
		return goja.Undefined()
	})
}

// opts converts an optional JS options argument into a document.
func (s *State) opts(v goja.Value) bson.D {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return bson.D{}
	}
	return s.toDoc(v, "options")
}

func lookup(d bson.D, key string) (any, bool) {
	for _, e := range d {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

func optBool(d bson.D, key string) (bool, bool) {
	v, ok := lookup(d, key)
	if !ok {
		return false, false
	}
	b, isBool := v.(bool)
	return b && isBool, true
}

func optInt(d bson.D, key string) (int64, bool) {
	v, ok := lookup(d, key)
	if !ok {
		return 0, false
	}
	return toInt64(v)
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	}
	return 0, false
}

package shell

import (
	"github.com/dop251/goja"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// cursorSpec is a lazy cursor. A cursor that is the value of a top-level statement is
// never iterated by the script; the runner pages it instead, so the result grid can
// re-query, page and refine it without re-running the script.
type cursorSpec struct {
	q      QuerySpec
	iter   *mongo.Cursor
	peeked bool
	done   bool
}

func (s *State) newCursor(spec *cursorSpec) goja.Value {
	obj := s.rt.NewObject()
	chain := func(apply func(c goja.FunctionCall)) method {
		return func(c goja.FunctionCall) goja.Value {
			if spec.iter != nil {
				panic(s.rt.NewTypeError("cursor is already being iterated"))
			}
			apply(c)
			return obj
		}
	}
	noop := chain(func(goja.FunctionCall) {})
	m := map[string]method{
		"sort":       chain(func(c goja.FunctionCall) { spec.q.Sort = s.toDoc(c.Argument(0), "sort") }),
		"limit":      chain(func(c goja.FunctionCall) { spec.q.Limit = abs(c.Argument(0).ToInteger()) }),
		"skip":       chain(func(c goja.FunctionCall) { spec.q.Skip = c.Argument(0).ToInteger() }),
		"projection": chain(func(c goja.FunctionCall) { spec.q.Projection = s.toDoc(c.Argument(0), "projection") }),
		"project":    chain(func(c goja.FunctionCall) { spec.q.Projection = s.toDoc(c.Argument(0), "projection") }),
		"hint":       chain(func(c goja.FunctionCall) { spec.q.Hint = s.toBSON(c.Argument(0)) }),
		"filter":     chain(func(c goja.FunctionCall) { spec.q.Filter = s.toDoc(c.Argument(0), "filter") }),
		"batchSize":  noop, "maxTimeMS": noop, "comment": noop, "pretty": noop, "readPref": noop,
		"noCursorTimeout": noop, "collation": noop, "allowDiskUse": noop, "readConcern": noop,
		"count": func(c goja.FunctionCall) goja.Value {
			q := spec.q
			if apply, _ := c.Argument(0).Export().(bool); !apply {
				q.Skip, q.Limit = 0, 0
			}
			return s.rt.ToValue(readRetry(s, func() (int64, error) { return Count(s.ctx, s.client, q) }))
		},
		"size": func(goja.FunctionCall) goja.Value {
			return s.rt.ToValue(readRetry(s, func() (int64, error) { return Count(s.ctx, s.client, spec.q) }))
		},
		"itcount": func(goja.FunctionCall) goja.Value {
			return s.rt.ToValue(len(s.all(spec)))
		},
		"toArray": func(goja.FunctionCall) goja.Value { return s.docsToJS(s.all(spec)) },
		"forEach": func(c goja.FunctionCall) goja.Value {
			fn, ok := goja.AssertFunction(c.Argument(0))
			if !ok {
				panic(s.rt.NewTypeError("forEach requires a function"))
			}
			for s.hasNext(spec) {
				if _, err := fn(goja.Undefined(), s.next(spec)); err != nil {
					panic(err)
				}
			}
			return goja.Undefined()
		},
		"map": func(c goja.FunctionCall) goja.Value {
			fn, ok := goja.AssertFunction(c.Argument(0))
			if !ok {
				panic(s.rt.NewTypeError("map requires a function"))
			}
			var items []any
			for s.hasNext(spec) {
				v, err := fn(goja.Undefined(), s.next(spec))
				if err != nil {
					panic(err)
				}
				items = append(items, v)
			}
			return s.rt.NewArray(items...)
		},
		"hasNext": func(goja.FunctionCall) goja.Value { return s.rt.ToValue(s.hasNext(spec)) },
		"next": func(goja.FunctionCall) goja.Value {
			if !s.hasNext(spec) {
				return goja.Null()
			}
			return s.next(spec)
		},
		"tryNext": func(goja.FunctionCall) goja.Value {
			if !s.hasNext(spec) {
				return goja.Null()
			}
			return s.next(spec)
		},
		"isExhausted": func(goja.FunctionCall) goja.Value { return s.rt.ToValue(!s.hasNext(spec)) },
		"close": func(goja.FunctionCall) goja.Value {
			if spec.iter != nil {
				_ = spec.iter.Close(s.ctx)
			}
			spec.done = true
			return goja.Undefined()
		},
		"explain": func(c goja.FunctionCall) goja.Value {
			verbosity := "queryPlanner"
			if v, ok := c.Argument(0).Export().(string); ok && v != "" {
				verbosity = v
			}
			var inner bson.D
			if spec.q.Kind == "aggregate" {
				inner = bson.D{{Key: "aggregate", Value: spec.q.Collection}, {Key: "pipeline", Value: spec.q.Pipeline}, {Key: "cursor", Value: bson.D{}}}
			} else {
				inner = bson.D{{Key: "find", Value: spec.q.Collection}, {Key: "filter", Value: spec.q.Filter}}
				if len(spec.q.Sort) > 0 {
					inner = append(inner, bson.E{Key: "sort", Value: spec.q.Sort})
				}
				if len(spec.q.Projection) > 0 {
					inner = append(inner, bson.E{Key: "projection", Value: spec.q.Projection})
				}
				if spec.q.Skip > 0 {
					inner = append(inner, bson.E{Key: "skip", Value: spec.q.Skip})
				}
				if spec.q.Limit > 0 {
					inner = append(inner, bson.E{Key: "limit", Value: spec.q.Limit})
				}
			}
			return s.command(spec.q.Database, bson.D{{Key: "explain", Value: inner}, {Key: "verbosity", Value: verbosity}})
		},
	}
	for k, fn := range m {
		_ = obj.Set(k, fn)
	}
	s.cursors[obj] = spec
	return obj
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func (s *State) openCursor(spec *cursorSpec) {
	if spec.iter != nil || spec.done {
		return
	}
	q := spec.q
	cur := readRetry(s, func() (*mongo.Cursor, error) {
		coll := s.client.Database(q.Database).Collection(q.Collection)
		if q.Kind == "aggregate" {
			pipe := append(bson.A{}, q.Pipeline...)
			if q.Skip > 0 {
				pipe = append(pipe, bson.D{{Key: "$skip", Value: q.Skip}})
			}
			if q.Limit > 0 {
				pipe = append(pipe, bson.D{{Key: "$limit", Value: q.Limit}})
			}
			return coll.Aggregate(s.ctx, pipe)
		}
		o := options.Find()
		if q.Skip > 0 {
			o.SetSkip(q.Skip)
		}
		if q.Limit > 0 {
			o.SetLimit(q.Limit)
		}
		if len(q.Sort) > 0 {
			o.SetSort(q.Sort)
		}
		if len(q.Projection) > 0 {
			o.SetProjection(q.Projection)
		}
		if q.Hint != nil {
			o.SetHint(q.Hint)
		}
		filter := q.Filter
		if filter == nil {
			filter = bson.D{}
		}
		return coll.Find(s.ctx, filter, o)
	})
	spec.iter = cur
	s.open = append(s.open, cur)
}

func (s *State) hasNext(spec *cursorSpec) bool {
	if spec.done {
		return false
	}
	if spec.peeked {
		return true
	}
	s.openCursor(spec)
	if spec.iter.Next(s.ctx) {
		spec.peeked = true
		return true
	}
	if err := spec.iter.Err(); err != nil {
		s.throw(err)
	}
	spec.done = true
	return false
}

// next returns the document hasNext advanced to.
func (s *State) next(spec *cursorSpec) goja.Value {
	if !s.hasNext(spec) {
		return goja.Null()
	}
	spec.peeked = false
	return s.toJS(s.current(spec))
}

func (s *State) current(spec *cursorSpec) bson.D {
	var d bson.D
	if err := bson.Unmarshal(spec.iter.Current, &d); err != nil {
		s.throw(err)
	}
	return d
}

func (s *State) all(spec *cursorSpec) []bson.D {
	out := []bson.D{}
	for s.hasNext(spec) {
		spec.peeked = false
		out = append(out, s.current(spec))
	}
	return out
}

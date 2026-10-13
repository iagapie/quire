package shell

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/dop251/goja"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/iagapie/quire/internal/docsession"
)

type method = func(goja.FunctionCall) goja.Value

// dynamic is a JS object whose unknown properties resolve to child collections,
// which is what makes `db.orders.find()` and `db.system.profile` work.
type dynamic struct {
	methods map[string]goja.Value
	child   func(name string) goja.Value
}

func (d *dynamic) Get(key string) goja.Value {
	if m, ok := d.methods[key]; ok {
		return m
	}
	// Properties probed by the runtime itself must not turn into collections.
	switch key {
	case "toJSON", "then", "constructor", "inspect", "valueOf":
		return nil
	}
	return d.child(key)
}
func (d *dynamic) Set(string, goja.Value) bool { return false }
func (d *dynamic) Has(key string) bool         { _, ok := d.methods[key]; return ok }
func (d *dynamic) Delete(string) bool          { return false }
func (d *dynamic) Keys() []string              { return nil }

func (s *State) methods(m map[string]method) map[string]goja.Value {
	out := make(map[string]goja.Value, len(m))
	for k, fn := range m {
		out[k] = s.rt.ToValue(fn)
	}
	return out
}

// readRetry runs a read with automatic retries on transient network errors.
func readRetry[T any](s *State, fn func() (T, error)) T {
	s.requireClient()
	v, err := docsession.RetryRead(s.ctx, s.env.OnRetry, func(context.Context) (T, error) { return fn() })
	if err != nil {
		s.throw(err)
	}
	return v
}

func (s *State) newDB(name string) *goja.Object {
	var obj *goja.Object
	d := &dynamic{child: func(coll string) goja.Value { return s.newCollection(name, coll) }}
	d.methods = s.methods(map[string]method{
		"getName":  func(goja.FunctionCall) goja.Value { return s.rt.ToValue(name) },
		"toString": func(goja.FunctionCall) goja.Value { return s.rt.ToValue(name) },
		"getCollection": func(c goja.FunctionCall) goja.Value {
			return s.newCollection(name, c.Argument(0).String())
		},
		"getSiblingDB": func(c goja.FunctionCall) goja.Value { return s.newDB(c.Argument(0).String()) },
		"getMongo":     func(goja.FunctionCall) goja.Value { return obj },
		"runCommand": func(c goja.FunctionCall) goja.Value {
			return s.runCommand(name, c.Argument(0))
		},
		"adminCommand": func(c goja.FunctionCall) goja.Value {
			return s.runCommand("admin", c.Argument(0))
		},
		"getCollectionNames": func(goja.FunctionCall) goja.Value {
			names := readRetry(s, func() ([]string, error) {
				return s.client.Database(name).ListCollectionNames(s.ctx, bson.D{}, options.ListCollections().SetAuthorizedCollections(true))
			})
			sort.Strings(names)
			items := make([]any, len(names))
			for i, n := range names {
				items[i] = n
			}
			return s.rt.NewArray(items...)
		},
		"getCollectionInfos": func(c goja.FunctionCall) goja.Value {
			filter := s.opts(c.Argument(0))
			specs := readRetry(s, func() ([]bson.D, error) {
				cur, err := s.client.Database(name).ListCollections(s.ctx, filter)
				if err != nil {
					return nil, err
				}
				var out []bson.D
				err = cur.All(s.ctx, &out)
				return out, err
			})
			return s.docsToJS(specs)
		},
		"createCollection": func(c goja.FunctionCall) goja.Value {
			s.requireWrite()
			cmd := append(bson.D{{Key: "create", Value: c.Argument(0).String()}}, s.opts(c.Argument(1))...)
			return s.command(name, cmd)
		},
		"createView": func(c goja.FunctionCall) goja.Value {
			s.requireWrite()
			cmd := bson.D{
				{Key: "create", Value: c.Argument(0).String()},
				{Key: "viewOn", Value: c.Argument(1).String()},
				{Key: "pipeline", Value: s.pipeline(c.Arguments[2:])},
			}
			return s.command(name, cmd)
		},
		"dropDatabase": func(goja.FunctionCall) goja.Value {
			s.requireWrite()
			return s.command(name, bson.D{{Key: "dropDatabase", Value: 1}})
		},
		"stats": func(goja.FunctionCall) goja.Value {
			return s.command(name, bson.D{{Key: "dbStats", Value: 1}})
		},
		"serverStatus": func(goja.FunctionCall) goja.Value {
			return s.command("admin", bson.D{{Key: "serverStatus", Value: 1}})
		},
		"hostInfo": func(goja.FunctionCall) goja.Value {
			return s.command("admin", bson.D{{Key: "hostInfo", Value: 1}})
		},
		"version": func(goja.FunctionCall) goja.Value {
			res := s.commandDoc(name, bson.D{{Key: "buildInfo", Value: 1}})
			v, _ := lookup(res, "version")
			return s.toJS(v)
		},
		"currentOp": func(c goja.FunctionCall) goja.Value {
			cmd := append(bson.D{{Key: "currentOp", Value: 1}}, s.opts(c.Argument(0))...)
			return s.command("admin", cmd)
		},
		"killOp": func(c goja.FunctionCall) goja.Value {
			return s.command("admin", bson.D{{Key: "killOp", Value: 1}, {Key: "op", Value: s.toBSON(c.Argument(0))}})
		},
	})
	obj = s.rt.NewDynamicObject(d)
	s.dbs[obj] = name
	return obj
}

func (s *State) commandDoc(db string, cmd bson.D) bson.D {
	return readRetry(s, func() (bson.D, error) {
		var out bson.D
		err := s.client.Database(db).RunCommand(s.ctx, cmd).Decode(&out)
		return out, err
	})
}

func (s *State) command(db string, cmd bson.D) goja.Value {
	return s.toJS(s.commandDoc(db, cmd))
}

func (s *State) runCommand(db string, arg goja.Value) goja.Value {
	var cmd bson.D
	if str, ok := arg.Export().(string); ok {
		cmd = bson.D{{Key: str, Value: 1}}
	} else {
		cmd = s.toDoc(arg, "command")
	}
	if len(cmd) == 0 {
		panic(s.rt.NewTypeError("runCommand requires a command document"))
	}
	if s.readOnly() && isWriteCommand(cmd[0].Key) {
		s.throw(ErrReadOnly)
	}
	return s.command(db, cmd)
}

func isWriteCommand(name string) bool {
	switch strings.ToLower(name) {
	case "insert", "update", "delete", "findandmodify", "create", "drop", "dropdatabase", "createindexes",
		"dropindexes", "renamecollection", "collmod", "createuser", "dropuser", "updateuser":
		return true
	}
	return false
}

// pipeline accepts either an array argument or stages as varargs.
func (s *State) pipeline(args []goja.Value) bson.A {
	if len(args) == 0 {
		return bson.A{}
	}
	if arr, ok := s.toBSON(args[0]).(bson.A); ok {
		return arr
	}
	out := bson.A{}
	for _, a := range args {
		out = append(out, s.toBSON(a))
	}
	return out
}

func (s *State) newCollection(db, name string) goja.Value {
	if name == "" {
		panic(s.rt.NewTypeError("collection name must not be empty"))
	}
	coll := func() *mongo.Collection {
		s.requireClient()
		return s.client.Database(db).Collection(name)
	}
	d := &dynamic{child: func(sub string) goja.Value { return s.newCollection(db, name+"."+sub) }}
	d.methods = s.methods(map[string]method{
		"getName":     func(goja.FunctionCall) goja.Value { return s.rt.ToValue(name) },
		"getFullName": func(goja.FunctionCall) goja.Value { return s.rt.ToValue(db + "." + name) },
		"toString":    func(goja.FunctionCall) goja.Value { return s.rt.ToValue(db + "." + name) },
		"getDB":       func(goja.FunctionCall) goja.Value { return s.newDB(db) },
		"find": func(c goja.FunctionCall) goja.Value {
			spec := &cursorSpec{q: QuerySpec{Kind: "find", Database: db, Collection: name,
				Filter: s.toDoc(c.Argument(0), "filter"), Projection: s.optionalDoc(c.Argument(1))}}
			return s.newCursor(spec)
		},
		"findOne": func(c goja.FunctionCall) goja.Value {
			filter := s.toDoc(c.Argument(0), "filter")
			o := options.FindOne()
			if p := s.optionalDoc(c.Argument(1)); len(p) > 0 {
				o.SetProjection(p)
			}
			doc := readRetry(s, func() (bson.D, error) {
				var out bson.D
				err := coll().FindOne(s.ctx, filter, o).Decode(&out)
				if errors.Is(err, mongo.ErrNoDocuments) {
					return nil, nil
				}
				return out, err
			})
			if doc == nil {
				return goja.Null()
			}
			return s.toJS(doc)
		},
		"aggregate": func(c goja.FunctionCall) goja.Value {
			spec := &cursorSpec{q: QuerySpec{Kind: "aggregate", Database: db, Collection: name, Pipeline: s.pipeline(c.Arguments)}}
			return s.newCursor(spec)
		},
		"countDocuments": func(c goja.FunctionCall) goja.Value {
			filter := s.toDoc(c.Argument(0), "filter")
			o := s.opts(c.Argument(1))
			q := QuerySpec{Kind: "find", Database: db, Collection: name, Filter: filter}
			q.Skip, _ = optInt(o, "skip")
			q.Limit, _ = optInt(o, "limit")
			n := readRetry(s, func() (int64, error) {
				return coll().CountDocuments(s.ctx, filter, countOpts(q))
			})
			return s.rt.ToValue(n)
		},
		"count": func(c goja.FunctionCall) goja.Value {
			filter := s.toDoc(c.Argument(0), "filter")
			n := readRetry(s, func() (int64, error) {
				if len(filter) == 0 {
					return coll().EstimatedDocumentCount(s.ctx)
				}
				return coll().CountDocuments(s.ctx, filter)
			})
			return s.rt.ToValue(n)
		},
		"estimatedDocumentCount": func(goja.FunctionCall) goja.Value {
			n := readRetry(s, func() (int64, error) { return coll().EstimatedDocumentCount(s.ctx) })
			return s.rt.ToValue(n)
		},
		"distinct": func(c goja.FunctionCall) goja.Value {
			field := c.Argument(0).String()
			filter := s.toDoc(c.Argument(1), "filter")
			vals := readRetry(s, func() (bson.A, error) {
				res := coll().Distinct(s.ctx, field, filter)
				if res.Err() != nil {
					return nil, res.Err()
				}
				var out bson.A
				err := res.Decode(&out)
				return out, err
			})
			return s.toJS(vals)
		},
		"insertOne": func(c goja.FunctionCall) goja.Value {
			s.requireWrite()
			doc := s.toDoc(c.Argument(0), "document")
			doc = ensureID(doc)
			_, err := coll().InsertOne(s.ctx, doc)
			if err != nil {
				s.throw(err)
			}
			id, _ := lookup(doc, "_id")
			return s.toJS(bson.D{{Key: "acknowledged", Value: true}, {Key: "insertedId", Value: id}})
		},
		"insertMany": func(c goja.FunctionCall) goja.Value {
			s.requireWrite()
			arr, ok := s.toBSON(c.Argument(0)).(bson.A)
			if !ok {
				panic(s.rt.NewTypeError("insertMany requires an array of documents"))
			}
			docs := make([]any, len(arr))
			ids := bson.D{}
			for i, v := range arr {
				d, ok := v.(bson.D)
				if !ok {
					panic(s.rt.NewTypeError("insertMany requires an array of documents"))
				}
				d = ensureID(d)
				id, _ := lookup(d, "_id")
				ids = append(ids, bson.E{Key: itoa(i), Value: id})
				docs[i] = d
			}
			o := options.InsertMany()
			if ordered, ok := optBool(s.opts(c.Argument(1)), "ordered"); ok {
				o.SetOrdered(ordered)
			}
			if _, err := coll().InsertMany(s.ctx, docs, o); err != nil {
				s.throw(err)
			}
			return s.toJS(bson.D{{Key: "acknowledged", Value: true}, {Key: "insertedIds", Value: ids}})
		},
		"updateOne":  s.updateFn(coll, false),
		"updateMany": s.updateFn(coll, true),
		"replaceOne": func(c goja.FunctionCall) goja.Value {
			s.requireWrite()
			filter := s.toDoc(c.Argument(0), "filter")
			doc := s.toDoc(c.Argument(1), "replacement")
			o := options.Replace()
			if up, ok := optBool(s.opts(c.Argument(2)), "upsert"); ok {
				o.SetUpsert(up)
			}
			res, err := coll().ReplaceOne(s.ctx, filter, doc, o)
			if err != nil {
				s.throw(err)
			}
			return s.updateResult(res)
		},
		"deleteOne":  s.deleteFn(coll, false),
		"deleteMany": s.deleteFn(coll, true),
		"remove": func(c goja.FunctionCall) goja.Value {
			justOne, _ := optBool(s.opts(c.Argument(1)), "justOne")
			return s.deleteFn(coll, !justOne)(c)
		},
		"findOneAndUpdate": func(c goja.FunctionCall) goja.Value {
			s.requireWrite()
			filter := s.toDoc(c.Argument(0), "filter")
			update := s.toBSON(c.Argument(1))
			od := s.opts(c.Argument(2))
			o := options.FindOneAndUpdate()
			if up, ok := optBool(od, "upsert"); ok {
				o.SetUpsert(up)
			}
			if returnAfter(od) {
				o.SetReturnDocument(options.After)
			}
			if p, ok := lookup(od, "projection"); ok {
				o.SetProjection(p)
			}
			if so, ok := lookup(od, "sort"); ok {
				o.SetSort(so)
			}
			return s.singleResult(coll().FindOneAndUpdate(s.ctx, filter, update, o))
		},
		"findOneAndReplace": func(c goja.FunctionCall) goja.Value {
			s.requireWrite()
			filter := s.toDoc(c.Argument(0), "filter")
			doc := s.toDoc(c.Argument(1), "replacement")
			od := s.opts(c.Argument(2))
			o := options.FindOneAndReplace()
			if up, ok := optBool(od, "upsert"); ok {
				o.SetUpsert(up)
			}
			if returnAfter(od) {
				o.SetReturnDocument(options.After)
			}
			return s.singleResult(coll().FindOneAndReplace(s.ctx, filter, doc, o))
		},
		"findOneAndDelete": func(c goja.FunctionCall) goja.Value {
			s.requireWrite()
			filter := s.toDoc(c.Argument(0), "filter")
			od := s.opts(c.Argument(1))
			o := options.FindOneAndDelete()
			if so, ok := lookup(od, "sort"); ok {
				o.SetSort(so)
			}
			return s.singleResult(coll().FindOneAndDelete(s.ctx, filter, o))
		},
		"createIndex": func(c goja.FunctionCall) goja.Value {
			s.requireWrite()
			keys := s.toDoc(c.Argument(0), "index keys")
			spec := append(bson.D{{Key: "key", Value: keys}}, s.opts(c.Argument(1))...)
			if _, ok := lookup(spec, "name"); !ok {
				spec = append(spec, bson.E{Key: "name", Value: indexName(keys)})
			}
			s.commandDoc(db, bson.D{{Key: "createIndexes", Value: name}, {Key: "indexes", Value: bson.A{spec}}})
			n, _ := lookup(spec, "name")
			return s.toJS(n)
		},
		"getIndexes": func(goja.FunctionCall) goja.Value {
			specs := readRetry(s, func() ([]bson.D, error) {
				cur, err := coll().Indexes().List(s.ctx)
				if err != nil {
					return nil, err
				}
				var out []bson.D
				err = cur.All(s.ctx, &out)
				return out, err
			})
			return s.docsToJS(specs)
		},
		"dropIndex": func(c goja.FunctionCall) goja.Value {
			s.requireWrite()
			return s.command(db, bson.D{{Key: "dropIndexes", Value: name}, {Key: "index", Value: s.toBSON(c.Argument(0))}})
		},
		"dropIndexes": func(goja.FunctionCall) goja.Value {
			s.requireWrite()
			return s.command(db, bson.D{{Key: "dropIndexes", Value: name}, {Key: "index", Value: "*"}})
		},
		"drop": func(goja.FunctionCall) goja.Value {
			s.requireWrite()
			if err := coll().Drop(s.ctx); err != nil {
				s.throw(err)
			}
			return s.rt.ToValue(true)
		},
		"stats": func(goja.FunctionCall) goja.Value {
			return s.command(db, bson.D{{Key: "collStats", Value: name}})
		},
		"renameCollection": func(c goja.FunctionCall) goja.Value {
			s.requireWrite()
			cmd := bson.D{
				{Key: "renameCollection", Value: db + "." + name},
				{Key: "to", Value: db + "." + c.Argument(0).String()},
			}
			if drop, ok := c.Argument(1).Export().(bool); ok {
				cmd = append(cmd, bson.E{Key: "dropTarget", Value: drop})
			}
			return s.command("admin", cmd)
		},
	})
	obj := s.rt.NewDynamicObject(d)
	s.collections[obj] = [2]string{db, name}
	return obj
}

func (s *State) optionalDoc(v goja.Value) bson.D {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	return s.toDoc(v, "projection")
}

func (s *State) updateFn(coll func() *mongo.Collection, many bool) method {
	return func(c goja.FunctionCall) goja.Value {
		s.requireWrite()
		filter := s.toDoc(c.Argument(0), "filter")
		update := s.toBSON(c.Argument(1))
		od := s.opts(c.Argument(2))
		var (
			res *mongo.UpdateResult
			err error
		)
		if many {
			o := options.UpdateMany()
			if up, ok := optBool(od, "upsert"); ok {
				o.SetUpsert(up)
			}
			if af, ok := lookup(od, "arrayFilters"); ok {
				if arr, ok := af.(bson.A); ok {
					o.SetArrayFilters([]any(arr))
				}
			}
			res, err = coll().UpdateMany(s.ctx, filter, update, o)
		} else {
			o := options.UpdateOne()
			if up, ok := optBool(od, "upsert"); ok {
				o.SetUpsert(up)
			}
			if af, ok := lookup(od, "arrayFilters"); ok {
				if arr, ok := af.(bson.A); ok {
					o.SetArrayFilters([]any(arr))
				}
			}
			res, err = coll().UpdateOne(s.ctx, filter, update, o)
		}
		if err != nil {
			s.throw(err)
		}
		return s.updateResult(res)
	}
}

func (s *State) updateResult(res *mongo.UpdateResult) goja.Value {
	return s.toJS(bson.D{
		{Key: "acknowledged", Value: res.Acknowledged},
		{Key: "insertedId", Value: res.UpsertedID},
		{Key: "matchedCount", Value: jsNumber(res.MatchedCount)},
		{Key: "modifiedCount", Value: jsNumber(res.ModifiedCount)},
		{Key: "upsertedCount", Value: jsNumber(res.UpsertedCount)},
	})
}

func (s *State) deleteFn(coll func() *mongo.Collection, many bool) method {
	return func(c goja.FunctionCall) goja.Value {
		s.requireWrite()
		filter := s.toDoc(c.Argument(0), "filter")
		var (
			res *mongo.DeleteResult
			err error
		)
		if many {
			res, err = coll().DeleteMany(s.ctx, filter)
		} else {
			res, err = coll().DeleteOne(s.ctx, filter)
		}
		if err != nil {
			s.throw(err)
		}
		return s.toJS(bson.D{{Key: "acknowledged", Value: res.Acknowledged}, {Key: "deletedCount", Value: jsNumber(res.DeletedCount)}})
	}
}

func (s *State) singleResult(r *mongo.SingleResult) goja.Value {
	var out bson.D
	if err := r.Decode(&out); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return goja.Null()
		}
		s.throw(err)
	}
	return s.toJS(out)
}

func returnAfter(od bson.D) bool {
	if v, ok := lookup(od, "returnDocument"); ok {
		return v == "after"
	}
	if v, ok := optBool(od, "returnNewDocument"); ok {
		return v
	}
	if v, ok := optBool(od, "new"); ok {
		return v
	}
	return false
}

func countOpts(q QuerySpec) *options.CountOptionsBuilder {
	o := options.Count()
	if q.Skip > 0 {
		o.SetSkip(q.Skip)
	}
	if q.Limit > 0 {
		o.SetLimit(q.Limit)
	}
	return o
}

func ensureID(d bson.D) bson.D {
	if _, ok := lookup(d, "_id"); ok {
		return d
	}
	return append(bson.D{{Key: "_id", Value: bson.NewObjectID()}}, d...)
}

func indexName(keys bson.D) string {
	parts := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		parts = append(parts, k.Key, strings.Trim(Format(k.Value, ""), `"`))
	}
	return strings.Join(parts, "_")
}

func itoa(i int) string { return strconv.Itoa(i) }

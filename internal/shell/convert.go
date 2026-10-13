package shell

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// toBSON converts a JS value into a BSON-compatible Go value. Objects keep key order.
func (s *State) toBSON(v goja.Value) any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	switch ev := v.Export().(type) {
	case bool, string:
		return ev
	case int64:
		if ev >= math.MinInt32 && ev <= math.MaxInt32 {
			return int32(ev)
		}
		return ev
	case float64:
		if ev == math.Trunc(ev) && ev >= math.MinInt32 && ev <= math.MaxInt32 && !(ev == 0 && math.Signbit(ev)) {
			return int32(ev)
		}
		return ev
	case time.Time:
		return bson.NewDateTimeFromTime(ev)
	}
	obj, ok := v.(*goja.Object)
	if !ok {
		return v.Export()
	}
	if w, ok := s.wrapped[obj]; ok {
		return w
	}
	switch obj.ClassName() {
	case "Date":
		if t, ok := obj.Export().(time.Time); ok {
			return bson.NewDateTimeFromTime(t)
		}
		return nil
	case "RegExp":
		return bson.Regex{Pattern: unescapeSlashes(obj.Get("source").String()), Options: regexOptions(obj.Get("flags").String())}
	case "Array":
		n := int(obj.Get("length").ToInteger())
		arr := make(bson.A, 0, n)
		for i := range n {
			arr = append(arr, s.toBSON(obj.Get(strconv.Itoa(i))))
		}
		return arr
	case "Function":
		return nil
	}
	if _, ok := s.cursors[obj]; ok {
		panic(s.rt.NewTypeError("a cursor cannot be used as a value; call toArray() first"))
	}
	keys := obj.Keys()
	doc := make(bson.D, 0, len(keys))
	for _, k := range keys {
		pv := obj.Get(k)
		if goja.IsUndefined(pv) {
			continue
		}
		if po, ok := pv.(*goja.Object); ok && po.ClassName() == "Function" {
			continue
		}
		doc = append(doc, bson.E{Key: k, Value: s.toBSON(pv)})
	}
	return doc
}

// toDoc converts a JS value that must be a document.
func (s *State) toDoc(v goja.Value, what string) bson.D {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return bson.D{}
	}
	switch d := s.toBSON(v).(type) {
	case bson.D:
		return d
	default:
		panic(s.rt.NewTypeError(what + " must be an object"))
	}
}

// unescapeSlashes turns the "\/" that JS needs inside a regex literal back into "/",
// so a pattern survives a round trip through the shell unchanged.
func unescapeSlashes(src string) string {
	if !strings.Contains(src, `\/`) {
		return src
	}
	var b strings.Builder
	escaped := false
	for _, r := range src {
		if escaped {
			if r != '/' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		b.WriteRune(r)
	}
	if escaped {
		b.WriteByte('\\')
	}
	return b.String()
}

func regexOptions(flags string) string {
	var b strings.Builder
	for _, f := range "imsx" {
		if strings.ContainsRune(flags, f) {
			b.WriteRune(f)
		}
	}
	return b.String()
}

// jsNumber marks a count that must reach scripts as a plain JS number (like mongosh)
// rather than as a NumberLong.
type jsNumber int64

// toJS converts a BSON value into a JS value.
func (s *State) toJS(v any) goja.Value {
	rt := s.rt
	switch x := v.(type) {
	case nil, bson.Null, bson.Undefined:
		return goja.Null()
	case bson.D:
		o := rt.NewObject()
		for _, e := range x {
			_ = o.Set(e.Key, s.toJS(e.Value))
		}
		return o
	case bson.M:
		o := rt.NewObject()
		for k, val := range x {
			_ = o.Set(k, s.toJS(val))
		}
		return o
	case bson.Raw:
		var d bson.D
		if err := bson.Unmarshal(x, &d); err != nil {
			s.throw(err)
		}
		return s.toJS(d)
	case bson.A:
		items := make([]any, len(x))
		for i, e := range x {
			items[i] = s.toJS(e)
		}
		return rt.NewArray(items...)
	case []any:
		return s.toJS(bson.A(x))
	case bson.ObjectID:
		return s.newObjectID(x)
	case bson.DateTime:
		return s.newDate(x.Time())
	case time.Time:
		return s.newDate(x)
	case int32:
		return rt.ToValue(int64(x))
	case int:
		return rt.ToValue(int64(x))
	case int64:
		return s.newLong(x)
	case jsNumber:
		return rt.ToValue(int64(x))
	case float64:
		return rt.ToValue(x)
	case bool, string:
		return rt.ToValue(x)
	case bson.Regex:
		flags := strings.ReplaceAll(x.Options, "x", "")
		if re, err := rt.New(rt.Get("RegExp"), rt.ToValue(x.Pattern), rt.ToValue(flags)); err == nil && flags == x.Options {
			return re
		}
		return s.wrap(x, "BSONRegExp", fmt.Sprintf("/%s/%s", x.Pattern, x.Options))
	default:
		return s.wrapAny(x)
	}
}

// docsToJS converts a slice of documents to a JS array.
func (s *State) docsToJS(docs []bson.D) goja.Value {
	items := make([]any, len(docs))
	for i, d := range docs {
		items[i] = s.toJS(d)
	}
	return s.rt.NewArray(items...)
}

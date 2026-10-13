package shell

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// wrap creates a JS object standing in for a BSON value that has no native JS type.
func (s *State) wrap(val any, typeName, display string) *goja.Object {
	o := s.rt.NewObject()
	_ = o.Set("toString", func(goja.FunctionCall) goja.Value { return s.rt.ToValue(display) })
	_ = o.Set("toJSON", func(goja.FunctionCall) goja.Value {
		raw, err := marshalValue(val)
		if err != nil {
			return goja.Null()
		}
		parsed, err := s.rt.RunString("(" + raw + ")")
		if err != nil {
			return goja.Null()
		}
		return parsed
	})
	_ = o.DefineDataProperty("_bsontype", s.rt.ToValue(typeName), goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_FALSE)
	s.wrapped[o] = val
	return o
}

func (s *State) wrapAny(v any) goja.Value {
	switch x := v.(type) {
	case bson.Decimal128:
		return s.newDecimal(x)
	case bson.Binary:
		return s.newBinary(x)
	case bson.Timestamp:
		return s.wrap(x, "Timestamp", fmt.Sprintf("Timestamp(%d, %d)", x.T, x.I))
	case bson.MinKey:
		return s.wrap(x, "MinKey", "MinKey()")
	case bson.MaxKey:
		return s.wrap(x, "MaxKey", "MaxKey()")
	case bson.JavaScript:
		return s.wrap(x, "Code", fmt.Sprintf("Code(%q)", string(x)))
	case bson.Symbol:
		return s.rt.ToValue(string(x))
	case bson.DBPointer:
		return s.wrap(x, "DBPointer", fmt.Sprintf("DBPointer(%q, %s)", x.DB, x.Pointer.Hex()))
	case bson.CodeWithScope:
		return s.wrap(x, "CodeWithScope", fmt.Sprintf("Code(%q)", string(x.Code)))
	default:
		return s.rt.ToValue(v)
	}
}

func (s *State) newObjectID(id bson.ObjectID) *goja.Object {
	o := s.wrap(id, "ObjectId", id.Hex())
	_ = o.Set("str", id.Hex())
	_ = o.Set("toHexString", func(goja.FunctionCall) goja.Value { return s.rt.ToValue(id.Hex()) })
	_ = o.Set("getTimestamp", func(goja.FunctionCall) goja.Value { return s.newDate(id.Timestamp()) })
	_ = o.Set("equals", func(c goja.FunctionCall) goja.Value {
		other, ok := s.toBSON(c.Argument(0)).(bson.ObjectID)
		return s.rt.ToValue(ok && other == id)
	})
	_ = o.Set("inspect", func(goja.FunctionCall) goja.Value { return s.rt.ToValue(`ObjectId("` + id.Hex() + `")`) })
	return o
}

func (s *State) newLong(n int64) *goja.Object {
	o := s.wrap(n, "Long", strconv.FormatInt(n, 10))
	_ = o.Set("valueOf", func(goja.FunctionCall) goja.Value { return s.rt.ToValue(n) })
	_ = o.Set("toNumber", func(goja.FunctionCall) goja.Value { return s.rt.ToValue(n) })
	return o
}

func (s *State) newInt(n int32) *goja.Object {
	o := s.wrap(n, "Int32", strconv.FormatInt(int64(n), 10))
	_ = o.Set("valueOf", func(goja.FunctionCall) goja.Value { return s.rt.ToValue(int64(n)) })
	return o
}

func (s *State) newDouble(f float64) *goja.Object {
	o := s.wrap(f, "Double", strconv.FormatFloat(f, 'g', -1, 64))
	_ = o.Set("valueOf", func(goja.FunctionCall) goja.Value { return s.rt.ToValue(f) })
	return o
}

func (s *State) newDecimal(d bson.Decimal128) *goja.Object {
	o := s.wrap(d, "Decimal128", d.String())
	_ = o.Set("valueOf", func(goja.FunctionCall) goja.Value {
		f, _ := strconv.ParseFloat(d.String(), 64)
		return s.rt.ToValue(f)
	})
	return o
}

func (s *State) newBinary(b bson.Binary) *goja.Object {
	display := base64.StdEncoding.EncodeToString(b.Data)
	if (b.Subtype == bson.TypeBinaryUUID || b.Subtype == bson.TypeBinaryUUIDOld) && len(b.Data) == 16 {
		display = formatUUID(b.Data)
	}
	o := s.wrap(b, "Binary", display)
	_ = o.Set("base64", func(goja.FunctionCall) goja.Value {
		return s.rt.ToValue(base64.StdEncoding.EncodeToString(b.Data))
	})
	_ = o.Set("hex", func(goja.FunctionCall) goja.Value { return s.rt.ToValue(hex.EncodeToString(b.Data)) })
	_ = o.Set("sub_type", int64(b.Subtype))
	return o
}

func (s *State) newDate(t time.Time) goja.Value {
	d, err := s.rt.New(s.rt.Get("Date"), s.rt.ToValue(t.UnixMilli()))
	if err != nil {
		s.throw(err)
	}
	return d
}

func formatUUID(b []byte) string {
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// installTypes registers the BSON constructors on the global object.
func (s *State) installTypes() {
	rt := s.rt
	set := func(name string, fn func(goja.FunctionCall) goja.Value) {
		_ = rt.Set(name, fn)
	}
	set("ObjectId", func(c goja.FunctionCall) goja.Value {
		arg := c.Argument(0)
		if goja.IsUndefined(arg) || goja.IsNull(arg) {
			return s.newObjectID(bson.NewObjectID())
		}
		if n, ok := arg.Export().(int64); ok {
			var id bson.ObjectID
			binary.BigEndian.PutUint32(id[:4], s.uint32Arg(n, "ObjectId timestamp"))
			return s.newObjectID(id)
		}
		id, err := bson.ObjectIDFromHex(arg.String())
		if err != nil {
			panic(rt.NewTypeError("invalid ObjectId: " + arg.String()))
		}
		return s.newObjectID(id)
	})
	set("ISODate", func(c goja.FunctionCall) goja.Value {
		arg := c.Argument(0)
		if goja.IsUndefined(arg) {
			return s.newDate(time.Now())
		}
		t, err := parseISODate(arg.String())
		if err != nil {
			panic(rt.NewTypeError(err.Error()))
		}
		return s.newDate(t)
	})
	long := func(c goja.FunctionCall) goja.Value {
		arg := c.Argument(0)
		if str, ok := arg.Export().(string); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(str), 10, 64)
			if err != nil {
				panic(rt.NewTypeError("invalid NumberLong: " + str))
			}
			return s.newLong(n)
		}
		return s.newLong(arg.ToInteger())
	}
	set("NumberLong", long)
	set("Long", long)
	intFn := func(c goja.FunctionCall) goja.Value {
		n := c.Argument(0).ToInteger()
		if n < math.MinInt32 || n > math.MaxInt32 {
			panic(rt.NewTypeError("NumberInt out of range: " + c.Argument(0).String()))
		}
		return s.newInt(int32(n))
	}
	set("NumberInt", intFn)
	set("Int32", intFn)
	dbl := func(c goja.FunctionCall) goja.Value { return s.newDouble(c.Argument(0).ToFloat()) }
	set("Double", dbl)
	set("NumberDouble", dbl)
	dec := func(c goja.FunctionCall) goja.Value {
		d, err := bson.ParseDecimal128(c.Argument(0).String())
		if err != nil {
			panic(rt.NewTypeError("invalid NumberDecimal: " + c.Argument(0).String()))
		}
		return s.newDecimal(d)
	}
	set("NumberDecimal", dec)
	set("Decimal128", dec)
	set("UUID", func(c goja.FunctionCall) goja.Value {
		var raw []byte
		if arg := c.Argument(0); goja.IsUndefined(arg) {
			raw = make([]byte, 16)
			_, _ = rand.Read(raw)
			raw[6] = (raw[6] & 0x0f) | 0x40
			raw[8] = (raw[8] & 0x3f) | 0x80
		} else {
			h := strings.ReplaceAll(arg.String(), "-", "")
			b, err := hex.DecodeString(h)
			if err != nil || len(b) != 16 {
				panic(rt.NewTypeError("invalid UUID: " + arg.String()))
			}
			raw = b
		}
		return s.newBinary(bson.Binary{Subtype: bson.TypeBinaryUUID, Data: raw})
	})
	set("BinData", func(c goja.FunctionCall) goja.Value {
		data, err := base64.StdEncoding.DecodeString(c.Argument(1).String())
		if err != nil {
			panic(rt.NewTypeError("invalid base64 in BinData"))
		}
		sub := c.Argument(0).ToInteger()
		if sub < 0 || sub > math.MaxUint8 {
			panic(rt.NewTypeError("invalid BinData subtype: " + c.Argument(0).String()))
		}
		return s.newBinary(bson.Binary{Subtype: byte(sub), Data: data})
	})
	set("Timestamp", func(c goja.FunctionCall) goja.Value {
		arg := c.Argument(0)
		if o, ok := arg.(*goja.Object); ok && o.ClassName() == "Object" {
			return s.wrapAny(bson.Timestamp{T: s.uint32Arg(o.Get("t").ToInteger(), "Timestamp t"), I: s.uint32Arg(o.Get("i").ToInteger(), "Timestamp i")})
		}
		return s.wrapAny(bson.Timestamp{T: s.uint32Arg(arg.ToInteger(), "Timestamp t"), I: s.uint32Arg(c.Argument(1).ToInteger(), "Timestamp i")})
	})
	set("MinKey", func(goja.FunctionCall) goja.Value { return s.wrapAny(bson.MinKey{}) })
	set("MaxKey", func(goja.FunctionCall) goja.Value { return s.wrapAny(bson.MaxKey{}) })
}

func parseISODate(str string) (time.Time, error) {
	str = strings.TrimSpace(str)
	layouts := []string{
		time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05Z0700",
		"2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, str); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid ISODate: %q", str)
}

// uint32Arg range-checks a JS number used where BSON stores an unsigned 32-bit value.
func (s *State) uint32Arg(n int64, what string) uint32 {
	if n < 0 || n > math.MaxUint32 {
		panic(s.rt.NewTypeError(fmt.Sprintf("%s out of range: %d", what, n)))
	}
	return uint32(n)
}

package shell

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// marshalValue renders a single BSON value as relaxed Extended JSON.
func marshalValue(v any) (string, error) {
	raw, err := bson.MarshalExtJSON(bson.D{{Key: "v", Value: v}}, false, false)
	if err != nil {
		return "", err
	}
	var wrapper struct {
		V json.RawMessage `json:"v"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return "", err
	}
	return string(wrapper.V), nil
}

// CanonicalValue renders a BSON value as canonical Extended JSON, preserving types.
func CanonicalValue(v any) (string, error) {
	raw, err := bson.MarshalExtJSON(bson.D{{Key: "v", Value: v}}, true, false)
	if err != nil {
		return "", err
	}
	var wrapper struct {
		V json.RawMessage `json:"v"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return "", err
	}
	return string(wrapper.V), nil
}

var identRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// Format renders a BSON value in mongo shell syntax. indent "" produces a single line.
func Format(v any, indent string) string {
	var b strings.Builder
	formatValue(&b, v, indent, "")
	return b.String()
}

func formatKey(k string) string {
	if identRe.MatchString(k) {
		return k
	}
	q, _ := json.Marshal(k)
	return string(q)
}

func formatValue(b *strings.Builder, v any, indent, cur string) {
	nl, next, sep := "", cur+indent, " "
	if indent != "" {
		nl = "\n"
		sep = ""
	}
	switch x := v.(type) {
	case nil, bson.Null:
		b.WriteString("null")
	case bson.Undefined:
		b.WriteString("undefined")
	case bson.D:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{" + nl)
		for i, e := range x {
			if indent != "" {
				b.WriteString(next)
			} else {
				b.WriteString(" ")
			}
			b.WriteString(formatKey(e.Key))
			b.WriteString(": ")
			formatValue(b, e.Value, indent, next)
			if i < len(x)-1 {
				b.WriteString(",")
			}
			b.WriteString(nl)
		}
		if indent != "" {
			b.WriteString(cur + "}")
		} else {
			b.WriteString(" }")
		}
	case bson.M:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		d := make(bson.D, 0, len(x))
		for _, k := range keys {
			d = append(d, bson.E{Key: k, Value: x[k]})
		}
		formatValue(b, d, indent, cur)
	case bson.Raw:
		var d bson.D
		if err := bson.Unmarshal(x, &d); err != nil {
			b.WriteString("<invalid document>")
			return
		}
		formatValue(b, d, indent, cur)
	case bson.A:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[" + nl)
		for i, e := range x {
			if indent != "" {
				b.WriteString(next)
			}
			formatValue(b, e, indent, next)
			if i < len(x)-1 {
				b.WriteString("," + sep)
			}
			b.WriteString(nl)
		}
		b.WriteString(cur + "]")
	case []any:
		formatValue(b, bson.A(x), indent, cur)
	case string:
		q, _ := json.Marshal(x)
		b.Write(q)
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString("NumberLong(" + strconv.FormatInt(x, 10) + ")")
	case float64:
		b.WriteString(formatDouble(x))
	case bson.ObjectID:
		b.WriteString(`ObjectId("` + x.Hex() + `")`)
	case bson.DateTime:
		b.WriteString(`ISODate("` + x.Time().UTC().Format("2006-01-02T15:04:05.000Z") + `")`)
	case time.Time:
		b.WriteString(`ISODate("` + x.UTC().Format("2006-01-02T15:04:05.000Z") + `")`)
	case bson.Regex:
		b.WriteString("/" + escapeSlashes(x.Pattern) + "/" + x.Options)
	case bson.Decimal128:
		b.WriteString(`NumberDecimal("` + x.String() + `")`)
	case bson.Binary:
		if (x.Subtype == bson.TypeBinaryUUID) && len(x.Data) == 16 {
			b.WriteString(`UUID("` + formatUUID(x.Data) + `")`)
		} else {
			fmt.Fprintf(b, `BinData(%d, "%s")`, x.Subtype, base64.StdEncoding.EncodeToString(x.Data))
		}
	case bson.Timestamp:
		fmt.Fprintf(b, "Timestamp(%d, %d)", x.T, x.I)
	case bson.MinKey:
		b.WriteString("MinKey()")
	case bson.MaxKey:
		b.WriteString("MaxKey()")
	case bson.JavaScript:
		q, _ := json.Marshal(string(x))
		b.WriteString("Code(" + string(q) + ")")
	case bson.Symbol:
		q, _ := json.Marshal(string(x))
		b.Write(q)
	default:
		if s, err := marshalValue(x); err == nil {
			b.WriteString(s)
		} else {
			fmt.Fprintf(b, "%v", x)
		}
	}
}

func formatDouble(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == math.Trunc(f) && math.Abs(f) < 1e15:
		return "Double(" + strconv.FormatFloat(f, 'f', -1, 64) + ")"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// escapeSlashes escapes forward slashes that are not already escaped.
func escapeSlashes(p string) string {
	if p == "" {
		return "(?:)"
	}
	var b strings.Builder
	escaped := false
	for _, r := range p {
		if r == '/' && !escaped {
			b.WriteByte('\\')
		}
		escaped = r == '\\' && !escaped
		b.WriteRune(r)
	}
	return b.String()
}

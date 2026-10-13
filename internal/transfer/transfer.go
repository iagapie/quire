// Package transfer reads and writes table dumps: CSV, JSON Lines, JSON arrays and SQL
// INSERT scripts, optionally gzip-compressed.
//
// Values travel as text, as the editor shows them; NULL stays distinct from an empty
// string in every format (an unquoted empty CSV field is NULL, a quoted one is empty).
// Each column has a kind (number, bool, json, binary, text...) that decides how JSON
// writes it.
package transfer

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Format is a dump file format.
type Format string

// Supported formats.
const (
	CSV   Format = "csv"
	JSONL Format = "jsonl"
	JSON  Format = "json"
	SQL   Format = "sql"
)

// Ext is the file extension of the format, without the dot.
func (f Format) Ext() string { return string(f) }

// Valid reports whether f is a supported format.
func (f Format) Valid() bool { return f == CSV || f == JSONL || f == JSON || f == SQL }

// Column kinds, as the query engine names them.
const (
	KindNumber = "number"
	KindBool   = "bool"
	KindJSON   = "json"
	KindBinary = "binary"
)

// Column is a column of a dump.
type Column struct {
	Name string
	Kind string
}

// SchemaSuffix ends the name of the file holding a table definition.
const SchemaSuffix = ".schema.sql"

// FileName is the data file name of a table: the name made safe for file systems.
func FileName(table string, f Format, gz bool) string {
	name := SafeName(table) + "." + f.Ext()
	if gz {
		name += ".gz"
	}
	return name
}

// SchemaName is the file name of a table definition.
func SchemaName(table string) string { return SafeName(table) + SchemaSuffix }

// SafeName replaces characters that are not allowed in file names on common systems.
func SafeName(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_", "\x00", "_")
	s = r.Replace(s)
	if s == "" || s == "." || s == ".." {
		s = "_" + s
	}
	return s
}

// Detect guesses the format of a dump file from its name, and whether the name says it
// is gzip-compressed. ok is false for files that are not dumps (table definitions
// included).
func Detect(path string) (f Format, gz bool, stem string, ok bool) {
	name := filepath.Base(path)
	if strings.HasSuffix(strings.ToLower(name), ".gz") {
		gz = true
		name = name[:len(name)-3]
	}
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, SchemaSuffix) {
		return "", false, "", false
	}
	ext := filepath.Ext(lower)
	stem = name[:len(name)-len(ext)]
	switch ext {
	case ".csv":
		return CSV, gz, stem, true
	case ".jsonl", ".ndjson":
		return JSONL, gz, stem, true
	case ".json":
		return JSON, gz, stem, true
	case ".sql":
		return SQL, gz, stem, true
	}
	return "", false, "", false
}

// Encoder writes rows in one format.
type Encoder struct {
	w      *bufio.Writer
	format Format
	cols   []Column
	n      int
	// Insert writes the INSERT statement of a row (SQL format).
	insert func(values []*string) string
}

// NewEncoder writes rows of cols to w. insert renders one row as an INSERT statement
// for the SQL format; header is written first (a table definition, for SQL).
func NewEncoder(w io.Writer, f Format, cols []Column, header string, insert func([]*string) string) (*Encoder, error) {
	e := &Encoder{w: bufio.NewWriterSize(w, 256<<10), format: f, cols: cols, insert: insert}
	switch f {
	case CSV:
		names := make([]*string, len(cols))
		for i := range cols {
			names[i] = &cols[i].Name
		}
		if err := e.csvRow(names); err != nil {
			return nil, err
		}
	case SQL:
		if insert == nil {
			return nil, errors.New("the SQL format needs a statement writer")
		}
		if header != "" {
			if _, err := e.w.WriteString(strings.TrimRight(header, "\n") + "\n\n"); err != nil {
				return nil, err
			}
		}
	}
	return e, nil
}

// Encode writes one row.
func (e *Encoder) Encode(values []*string) error {
	defer func() { e.n++ }()
	switch e.format {
	case CSV:
		return e.csvRow(values)
	case SQL:
		_, err := e.w.WriteString(e.insert(values) + ";\n")
		return err
	case JSON:
		sep := ",\n"
		if e.n == 0 {
			sep = "[\n"
		}
		if _, err := e.w.WriteString(sep); err != nil {
			return err
		}
	}
	if err := e.object(values); err != nil {
		return err
	}
	if e.format == JSONL {
		return e.w.WriteByte('\n')
	}
	return nil
}

// Close ends the output (the closing bracket of a JSON array) and flushes it.
func (e *Encoder) Close() error {
	if e.format == JSON {
		end := "\n]\n"
		if e.n == 0 {
			end = "[]\n"
		}
		if _, err := e.w.WriteString(end); err != nil {
			return err
		}
	}
	return e.w.Flush()
}

func (e *Encoder) object(values []*string) error {
	if err := e.w.WriteByte('{'); err != nil {
		return err
	}
	for i, c := range e.cols {
		if i > 0 {
			_, _ = e.w.WriteString(", ")
		}
		name, _ := json.Marshal(c.Name)
		_, _ = e.w.Write(name)
		_, _ = e.w.WriteString(": ")
		if _, err := e.w.WriteString(JSONValue(c.Kind, values[i])); err != nil {
			return err
		}
	}
	return e.w.WriteByte('}')
}

// JSONValue writes a value as JSON: numbers, booleans and JSON values keep their type
// when their text allows it; everything else is a string.
func JSONValue(kind string, v *string) string {
	if v == nil {
		return "null"
	}
	s := *v
	switch kind {
	case KindNumber:
		if isJSONNumber(s) {
			return s
		}
	case KindBool:
		switch strings.ToLower(s) {
		case "true", "t", "1", "yes", "y":
			return "true"
		case "false", "f", "0", "no", "n":
			return "false"
		}
	case KindJSON:
		if json.Valid([]byte(s)) {
			return strings.TrimSpace(s)
		}
	}
	out, _ := json.Marshal(s)
	return string(out)
}

func isJSONNumber(s string) bool {
	if s == "" {
		return false
	}
	var n json.Number
	return json.Unmarshal([]byte(s), &n) == nil && !strings.ContainsAny(s, " \t\n")
}

// csvRow writes RFC 4180 fields: NULL is an unquoted empty field, an empty string is "".
func (e *Encoder) csvRow(values []*string) error {
	for i, v := range values {
		if i > 0 {
			_ = e.w.WriteByte(',')
		}
		if v == nil {
			continue
		}
		s := *v
		if s == "" || strings.ContainsAny(s, ",\"\r\n") || s[0] == ' ' || s[len(s)-1] == ' ' {
			_ = e.w.WriteByte('"')
			_, _ = e.w.WriteString(strings.ReplaceAll(s, `"`, `""`))
			_ = e.w.WriteByte('"')
			continue
		}
		_, _ = e.w.WriteString(s)
	}
	_, err := e.w.WriteString("\r\n")
	return err
}

// Record is a row read from a dump: column names and values (nil is NULL).
type Record struct {
	Columns []string
	Values  []*string
}

// Decoder reads rows of a CSV, JSON Lines or JSON dump.
type Decoder struct {
	format Format
	csv    *csvReader
	header []string
	dec    *json.Decoder
	br     *bufio.Reader
	line   int
	n      int
}

// NewDecoder reads a dump in format f from r (decompressed already). SQL dumps are
// scripts and are run instead (see the import).
func NewDecoder(r io.Reader, f Format) (*Decoder, error) {
	d := &Decoder{format: f}
	switch f {
	case CSV:
		d.csv = &csvReader{r: bufio.NewReaderSize(r, 256<<10)}
		head, err := d.csv.read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("the file is empty")
			}
			return nil, err
		}
		for _, h := range head {
			if h == nil {
				d.header = append(d.header, "")
			} else {
				d.header = append(d.header, strings.TrimPrefix(*h, "\uFEFF"))
			}
		}
	case JSONL:
		d.br = bufio.NewReaderSize(r, 256<<10)
	case JSON:
		d.dec = json.NewDecoder(bufio.NewReaderSize(r, 256<<10))
		d.dec.UseNumber()
		tok, err := d.dec.Token()
		if err != nil {
			return nil, fmt.Errorf("not a JSON array: %w", err)
		}
		if delim, ok := tok.(json.Delim); !ok || delim != '[' {
			return nil, errors.New("not a JSON array of objects")
		}
	default:
		return nil, fmt.Errorf("cannot read %s rows", f)
	}
	return d, nil
}

// Next returns the next row, or io.EOF.
func (d *Decoder) Next() (Record, error) {
	d.n++
	switch d.format {
	case CSV:
		vals, err := d.csv.read()
		if err != nil {
			return Record{}, err
		}
		if len(vals) != len(d.header) {
			return Record{}, fmt.Errorf("row %d has %d fields, the header %d", d.n, len(vals), len(d.header))
		}
		return Record{Columns: d.header, Values: vals}, nil
	case JSONL:
		for {
			line, err := d.br.ReadBytes('\n')
			d.line++
			if len(strings.TrimSpace(string(line))) == 0 {
				if err != nil {
					return Record{}, err
				}
				continue
			}
			rec, perr := objectRecord(json.NewDecoder(strings.NewReader(string(line))))
			if perr != nil {
				return Record{}, fmt.Errorf("line %d: %w", d.line, perr)
			}
			return rec, nil
		}
	}
	if !d.dec.More() {
		return Record{}, io.EOF
	}
	rec, err := objectRecord(d.dec)
	if err != nil {
		return Record{}, fmt.Errorf("element %d: %w", d.n, err)
	}
	return rec, nil
}

// objectRecord reads one JSON object, keeping its keys in order. Nested objects and
// arrays become their JSON text; numbers and booleans their text.
func objectRecord(dec *json.Decoder) (Record, error) {
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return Record{}, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return Record{}, errors.New("expected an object")
	}
	var rec Record
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return Record{}, err
		}
		key, _ := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return Record{}, err
		}
		rec.Columns = append(rec.Columns, key)
		rec.Values = append(rec.Values, rawText(raw))
	}
	if _, err := dec.Token(); err != nil {
		return Record{}, err
	}
	return rec, nil
}

func rawText(raw json.RawMessage) *string {
	s := strings.TrimSpace(string(raw))
	switch {
	case s == "null":
		return nil
	case strings.HasPrefix(s, `"`):
		var out string
		if json.Unmarshal(raw, &out) == nil {
			return &out
		}
	}
	return &s
}

// csvReader reads RFC 4180 records and tells NULL (unquoted empty) from "" (quoted).
type csvReader struct {
	r *bufio.Reader
}

func (c *csvReader) read() ([]*string, error) {
	var (
		out     []*string
		field   []byte
		quoted  bool
		inQ     bool
		started bool
	)
	end := func() {
		if !quoted && len(field) == 0 {
			out = append(out, nil)
		} else {
			s := string(field)
			out = append(out, &s)
		}
		field = field[:0]
		quoted = false
	}
	// Bytes, not runes: the delimiters are ASCII, and values are kept as they are.
	for {
		ch, err := c.r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && started {
				if inQ {
					return nil, errors.New("unterminated quoted field")
				}
				end()
				return out, nil
			}
			return nil, err
		}
		started = true
		if inQ {
			if ch == '"' {
				next, err := c.r.ReadByte()
				if err == nil && next == '"' {
					field = append(field, '"')
					continue
				}
				if err == nil {
					_ = c.r.UnreadByte()
				}
				inQ = false
				continue
			}
			field = append(field, ch)
			continue
		}
		switch ch {
		case '"':
			if len(field) == 0 {
				inQ, quoted = true, true
				continue
			}
			field = append(field, ch)
		case ',':
			end()
		case '\r':
		case '\n':
			end()
			return out, nil
		default:
			field = append(field, ch)
		}
	}
}

// Reader opens a dump, decompressing it when gz is set.
func Reader(r io.Reader, gz bool) (io.Reader, func() error, error) {
	if !gz {
		return r, func() error { return nil }, nil
	}
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, nil, err
	}
	return zr, zr.Close, nil
}

// CountingWriter counts the bytes written through it.
type CountingWriter struct {
	W io.Writer
	N int64
}

func (c *CountingWriter) Write(p []byte) (int, error) {
	n, err := c.W.Write(p)
	c.N += int64(n)
	return n, err
}

// CountingReader counts the bytes read through it.
type CountingReader struct {
	R io.Reader
	N int64
}

func (c *CountingReader) Read(p []byte) (int, error) {
	n, err := c.R.Read(p)
	c.N += int64(n)
	return n, err
}

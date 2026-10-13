// Package docdump reads and writes collection dumps: JSON Lines, JSON arrays and
// mongodump-compatible BSON, optionally gzip-compressed, plus mongodump-style metadata
// files with the collection's indexes.
package docdump

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Format is a dump file format.
type Format string

// Supported formats.
const (
	JSONL Format = "jsonl"
	JSON  Format = "json"
	BSON  Format = "bson"
)

// Ext is the file extension of the format, without the dot.
func (f Format) Ext() string { return string(f) }

// Valid reports whether f is a supported format.
func (f Format) Valid() bool { return f == JSONL || f == JSON || f == BSON }

// MaxDocumentSize bounds a single BSON document read from a dump (16 MiB plus headroom).
const MaxDocumentSize = 48 << 20

// FileName is the data file name of a collection: the name made safe for file systems.
func FileName(coll string, f Format, gz bool) string {
	name := SafeName(coll) + "." + f.Ext()
	if gz {
		name += ".gz"
	}
	return name
}

// MetadataName is the metadata file name of a collection, as mongodump names it.
func MetadataName(coll string) string { return SafeName(coll) + ".metadata.json" }

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
// is gzip-compressed. ok is false for files that are not dumps (including metadata).
func Detect(path string) (f Format, gz bool, stem string, ok bool) {
	name := filepath.Base(path)
	if strings.HasSuffix(strings.ToLower(name), ".gz") {
		gz = true
		name = name[:len(name)-3]
	}
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".metadata.json") {
		return "", false, "", false
	}
	ext := filepath.Ext(lower)
	stem = name[:len(name)-len(ext)]
	switch ext {
	case ".bson":
		return BSON, gz, stem, true
	case ".jsonl", ".ndjson":
		return JSONL, gz, stem, true
	case ".json":
		return JSON, gz, stem, true
	}
	return "", false, "", false
}

// Encoder writes documents in one format.
type Encoder struct {
	w         *bufio.Writer
	format    Format
	canonical bool
	n         int
}

// NewEncoder writes documents to w. canonical selects canonical Extended JSON (lossless)
// over relaxed (readable); it has no effect on BSON.
func NewEncoder(w io.Writer, f Format, canonical bool) *Encoder {
	return &Encoder{w: bufio.NewWriterSize(w, 256<<10), format: f, canonical: canonical}
}

// Encode writes one document.
func (e *Encoder) Encode(doc bson.Raw) error {
	if e.format == BSON {
		_, err := e.w.Write(doc)
		e.n++
		return err
	}
	js, err := bson.MarshalExtJSON(doc, e.canonical, false)
	if err != nil {
		return err
	}
	if e.format == JSON {
		sep := ",\n"
		if e.n == 0 {
			sep = "[\n"
		}
		if _, err := e.w.WriteString(sep); err != nil {
			return err
		}
	}
	if _, err := e.w.Write(js); err != nil {
		return err
	}
	e.n++
	if e.format == JSONL {
		return e.w.WriteByte('\n')
	}
	return nil
}

// Close finishes the output (the closing bracket of a JSON array) and flushes it.
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

// Decoder reads documents from a dump.
type Decoder struct {
	next func() (bson.Raw, error)
}

// Next returns the next document, or io.EOF at the end.
func (d *Decoder) Next() (bson.Raw, error) { return d.next() }

// NewDecoder reads documents in format f. gzip input is detected from its content. JSON
// input may be an array or a sequence of documents (JSON Lines or concatenated).
func NewDecoder(r io.Reader, f Format) (*Decoder, error) {
	br := bufio.NewReaderSize(r, 256<<10)
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		br = bufio.NewReaderSize(zr, 256<<10)
	}
	if f == BSON {
		return &Decoder{next: func() (bson.Raw, error) { return readBSON(br) }}, nil
	}
	return jsonDecoder(br)
}

func readBSON(r *bufio.Reader) (bson.Raw, error) {
	head, err := r.Peek(4)
	if err != nil {
		if errors.Is(err, io.EOF) && len(head) == 0 {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("truncated BSON document: %w", io.ErrUnexpectedEOF)
	}
	size := int(int32(binary.LittleEndian.Uint32(head))) //nolint:gosec // validated below
	if size < 5 || size > MaxDocumentSize {
		return nil, fmt.Errorf("invalid BSON document size %d", size)
	}
	doc := make([]byte, size)
	if _, err := io.ReadFull(r, doc); err != nil {
		return nil, fmt.Errorf("truncated BSON document: %w", io.ErrUnexpectedEOF)
	}
	raw := bson.Raw(doc)
	if err := raw.Validate(); err != nil {
		return nil, fmt.Errorf("invalid BSON document: %w", err)
	}
	return raw, nil
}

func jsonDecoder(br *bufio.Reader) (*Decoder, error) {
	first, err := firstByte(br)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return &Decoder{next: func() (bson.Raw, error) { return nil, io.EOF }}, nil
		}
		return nil, err
	}
	dec := json.NewDecoder(br)
	dec.UseNumber()
	array := first == '['
	if array {
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
	}
	n := 0
	return &Decoder{next: func() (bson.Raw, error) {
		if array && !dec.More() {
			return nil, io.EOF
		}
		var msg json.RawMessage
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) && !array {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("document %d: %w", n+1, err)
		}
		n++
		msg = bytes.TrimSpace(msg)
		if len(msg) == 0 || msg[0] != '{' {
			return nil, fmt.Errorf("document %d is not an object", n)
		}
		var doc bson.Raw
		if err := bson.UnmarshalExtJSON(msg, false, &doc); err != nil {
			return nil, fmt.Errorf("document %d: %w", n, err)
		}
		return doc, nil
	}}, nil
}

func firstByte(br *bufio.Reader) (byte, error) {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return 0, err
		}
		// Skip whitespace and a UTF-8 byte order mark.
		if b == ' ' || b == '\n' || b == '\r' || b == '\t' || b == 0xEF || b == 0xBB || b == 0xBF {
			continue
		}
		return b, br.UnreadByte()
	}
}

// Metadata is the mongodump metadata of a collection.
type Metadata struct {
	CollectionName string   `bson:"collectionName"`
	Type           string   `bson:"type,omitempty"`
	Options        bson.D   `bson:"options"`
	Indexes        []bson.D `bson:"indexes"`
}

// EncodeMetadata renders metadata as canonical Extended JSON.
func EncodeMetadata(m Metadata) ([]byte, error) {
	if m.Options == nil {
		m.Options = bson.D{}
	}
	if m.Indexes == nil {
		m.Indexes = []bson.D{}
	}
	out, err := bson.MarshalExtJSON(m, true, false)
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// DecodeMetadata parses a metadata file (plain or gzip-compressed).
func DecodeMetadata(r io.Reader) (Metadata, error) {
	br := bufio.NewReader(r)
	var src io.Reader = br
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return Metadata{}, err
		}
		src = zr
	}
	data, err := io.ReadAll(io.LimitReader(src, 16<<20))
	if err != nil {
		return Metadata{}, err
	}
	var m Metadata
	if err := bson.UnmarshalExtJSON(data, false, &m); err != nil {
		return Metadata{}, fmt.Errorf("metadata: %w", err)
	}
	return m, nil
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

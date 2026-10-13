package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/iagapie/quire/internal/docsession"
)

// Mode selects which part of a script runs.
type Mode string

const (
	ModeAll       Mode = "all"
	ModeCurrent   Mode = "current"
	ModeSelection Mode = "selection"
)

// RunRequest describes a script execution. Offsets are UTF-16 code units, as used by
// the editor.
type RunRequest struct {
	Script   string `json:"script"`
	Mode     Mode   `json:"mode"`
	Cursor   int    `json:"cursor"`
	SelStart int    `json:"selStart"`
	SelEnd   int    `json:"selEnd"`
	PageSize int64  `json:"pageSize"`
}

// Query is the editable text form of a read query shown above a result grid.
type Query struct {
	Kind       string `json:"kind"`
	Database   string `json:"database"`
	Collection string `json:"collection"`
	Filter     string `json:"filter"`
	Projection string `json:"projection"`
	Sort       string `json:"sort"`
	Pipeline   string `json:"pipeline"`
	Skip       int64  `json:"skip"`
	Limit      int64  `json:"limit"`
}

// Result is the outcome of one statement.
type Result struct {
	Kind       string `json:"kind"` // find | aggregate | value | error
	Title      string `json:"title"`
	Line       int    `json:"line"`
	Statement  string `json:"statement"`
	Query      *Query `json:"query,omitempty"`
	Page       *Page  `json:"page,omitempty"`
	Value      string `json:"value,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs"`
}

// RunResult is the outcome of a script execution.
type RunResult struct {
	Results    []Result `json:"results"`
	Output     []string `json:"output"`
	Database   string   `json:"database"`
	DurationMs int64    `json:"durationMs"`
}

type statement struct {
	src   string
	start int // byte offset in script
	line  int
}

// Run executes the requested part of the script.
func Run(ctx context.Context, env Env, req RunRequest) RunResult {
	start := time.Now()
	if req.PageSize <= 0 {
		req.PageSize = 50
	}
	s := NewState(ctx, env)
	defer s.Close()

	stop := context.AfterFunc(ctx, func() { s.rt.Interrupt(ctx.Err()) })
	defer stop()

	res := RunResult{Results: []Result{}}
	stmts, err := selectStatements(req)
	if err != nil {
		res.Results = append(res.Results, Result{Kind: "error", Title: "Syntax error", Line: errorLine(err), Error: err.Error()})
	}
	for _, st := range stmts {
		r, ok := s.exec(st, req.PageSize)
		if ok {
			res.Results = append(res.Results, r)
		}
		if r.Kind == "error" || ctx.Err() != nil {
			break
		}
	}
	res.Output = s.output
	if res.Output == nil {
		res.Output = []string{}
	}
	res.Database = s.Database()
	res.DurationMs = time.Since(start).Milliseconds()
	return res
}

// exec runs one statement. ok is false when the statement produced nothing to show.
func (s *State) exec(st statement, pageSize int64) (r Result, ok bool) {
	start := time.Now()
	r = Result{Line: st.line, Statement: strings.TrimSpace(st.src)}
	defer func() {
		r.DurationMs = time.Since(start).Milliseconds()
	}()

	v, err := s.rt.RunString(st.src)
	if err != nil {
		r.Kind, r.Title, r.Error = "error", fmt.Sprintf("Error (line %d)", st.line), jsError(err, s.ctx)
		return r, true
	}
	if v == nil || goja.IsUndefined(v) {
		return r, false
	}
	if obj, isObj := v.(*goja.Object); isObj {
		if spec, isCursor := s.cursors[obj]; isCursor {
			return s.cursorResult(r, spec, pageSize), true
		}
	}
	defer func() {
		if p := recover(); p != nil {
			r.Kind, r.Error = "error", fmt.Sprint(p)
			ok = true
		}
	}()
	val := s.toBSON(v)
	if o, isObj := v.(*goja.Object); isObj {
		if name, isDB := s.dbs[o]; isDB {
			val = name
		} else if c, isColl := s.collections[o]; isColl {
			val = c[0] + "." + c[1]
		}
	}
	raw, err := CanonicalValue(val)
	if err != nil {
		r.Kind, r.Error = "error", err.Error()
		return r, true
	}
	r.Kind, r.Title, r.Value = "value", fmt.Sprintf("Result (line %d)", st.line), raw
	return r, true
}

func (s *State) cursorResult(r Result, spec *cursorSpec, pageSize int64) Result {
	q := spec.q
	r.Query = TextQuery(q)
	if q.Kind == "aggregate" {
		r.Kind, r.Title = "aggregate", fmt.Sprintf("Aggregate %s (line %d)", q.Collection, r.Line)
	} else {
		r.Kind, r.Title = "find", fmt.Sprintf("Find %s (line %d)", q.Collection, r.Line)
	}
	if s.client == nil {
		r.Kind, r.Error = "error", "not connected"
		return r
	}
	page, err := retryPage(s.ctx, s, q, 0, pageSize)
	if err != nil {
		r.Kind, r.Error = "error", jsError(err, s.ctx)
		return r
	}
	r.Page = &page
	return r
}

func retryPage(ctx context.Context, s *State, q QuerySpec, offset, pageSize int64) (Page, error) {
	var out Page
	_, err := docsession.RetryRead(ctx, s.env.OnRetry, func(ctx context.Context) (struct{}, error) {
		p, err := FetchPage(ctx, s.client, q, offset, pageSize)
		out = p
		return struct{}{}, err
	})
	return out, err
}

// TextQuery renders a spec in editable shell syntax.
func TextQuery(q QuerySpec) *Query {
	t := &Query{Kind: q.Kind, Database: q.Database, Collection: q.Collection, Skip: q.Skip, Limit: q.Limit}
	if q.Kind == "aggregate" {
		t.Pipeline = Format(q.Pipeline, "  ")
		return t
	}
	t.Filter = formatDoc(q.Filter)
	t.Projection = formatDoc(q.Projection)
	t.Sort = formatDoc(q.Sort)
	return t
}

func formatDoc(d bson.D) string {
	if len(d) == 0 {
		return ""
	}
	return Format(d, "")
}

func jsError(err error, ctx context.Context) string {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "operation timed out"
		}
		return "operation cancelled"
	}
	var ex *goja.Exception
	if errors.As(err, &ex) {
		if o, ok := ex.Value().(*goja.Object); ok {
			if msg := o.Get("message"); msg != nil && !goja.IsUndefined(msg) {
				name := o.Get("name")
				if name != nil && name.String() != "Error" {
					return name.String() + ": " + msg.String()
				}
				return msg.String()
			}
		}
		return ex.Value().String()
	}
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		return "operation cancelled"
	}
	return err.Error()
}

// selectStatements parses the script and picks the statements to run.
func selectStatements(req RunRequest) ([]statement, error) {
	src := req.Script
	base := 0
	if req.Mode == ModeSelection && req.SelEnd > req.SelStart {
		a, b := byteOffset(src, req.SelStart), byteOffset(src, req.SelEnd)
		base, src = a, src[a:b]
	}
	prog, err := parser.ParseFile(nil, "", src, 0)
	if err != nil {
		if req.Mode == ModeCurrent {
			// The rest of the script may be mid-edit; fall back to the paragraph at the cursor.
			return paragraphAt(req.Script, byteOffset(req.Script, req.Cursor))
		}
		return nil, err
	}
	all := make([]statement, 0, len(prog.Body))
	for _, st := range prog.Body {
		a, b := int(st.Idx0())-1, int(st.Idx1())-1
		if _, empty := st.(*ast.EmptyStatement); empty || a < 0 || b > len(src) || a >= b {
			continue
		}
		all = append(all, statement{src: src[a:b], start: base + a, line: lineOf(req.Script, base+a)})
	}
	if req.Mode != ModeCurrent {
		return all, nil
	}
	cur := byteOffset(req.Script, req.Cursor)
	return pickCurrent(req.Script, all, cur), nil
}

// pickCurrent returns the statement under the cursor, or the closest one before it on
// the same line, or the last statement that ends before the cursor.
func pickCurrent(script string, all []statement, cur int) []statement {
	var before *statement
	curLine := lineOf(script, cur)
	for i := range all {
		st := &all[i]
		end := st.start + len(st.src)
		if cur >= st.start && cur <= end {
			return []statement{*st}
		}
		if end <= cur {
			before = st
		}
	}
	if before != nil && lineOf(script, before.start+len(before.src)) == curLine {
		return []statement{*before}
	}
	for i := range all {
		if lineOf(script, all[i].start) == curLine {
			return []statement{all[i]}
		}
	}
	if before != nil {
		return []statement{*before}
	}
	return nil
}

// paragraphAt returns the block of non-blank lines around offset as one statement.
func paragraphAt(script string, offset int) ([]statement, error) {
	lines := strings.SplitAfter(script, "\n")
	pos, idx := 0, 0
	for i, l := range lines {
		if offset < pos+len(l) || i == len(lines)-1 {
			idx = i
			break
		}
		pos += len(l)
	}
	startLine, endLine := idx, idx
	for startLine > 0 && strings.TrimSpace(lines[startLine-1]) != "" {
		startLine--
	}
	for endLine < len(lines)-1 && strings.TrimSpace(lines[endLine+1]) != "" {
		endLine++
	}
	start := 0
	for i := range startLine {
		start += len(lines[i])
	}
	src := strings.Join(lines[startLine:endLine+1], "")
	if strings.TrimSpace(src) == "" {
		return nil, errors.New("nothing to run at the cursor")
	}
	if _, err := parser.ParseFile(nil, "", src, 0); err != nil {
		return nil, err
	}
	return []statement{{src: src, start: start, line: startLine + 1}}, nil
}

// byteOffset converts a UTF-16 offset into a byte offset in s.
func byteOffset(s string, u16 int) int {
	if u16 <= 0 {
		return 0
	}
	n := 0
	for i, r := range s {
		if n >= u16 {
			return i
		}
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return len(s)
}

func lineOf(s string, offset int) int {
	if offset > len(s) {
		offset = len(s)
	}
	return strings.Count(s[:offset], "\n") + 1
}

func errorLine(err error) int {
	var list parser.ErrorList
	if errors.As(err, &list) && len(list) > 0 {
		return list[0].Position.Line
	}
	return 0
}

// Eval parses a shell-syntax expression (e.g. a filter or a document) into BSON.
func Eval(text string) (v any, err error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	s := NewState(context.Background(), Env{})
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%v", p)
		}
	}()
	val, err := s.rt.RunString("(\n" + text + "\n)")
	if err != nil {
		return nil, errors.New(jsError(err, context.Background()))
	}
	if obj, ok := val.(*goja.Object); ok {
		if _, isCursor := s.cursors[obj]; isCursor {
			return nil, errors.New("expected a value, got a cursor")
		}
	}
	return s.toBSON(val), nil
}

// EvalDoc parses a shell-syntax document. Empty text yields an empty document.
func EvalDoc(text, what string) (bson.D, error) {
	v, err := Eval(text)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	switch d := v.(type) {
	case nil:
		return bson.D{}, nil
	case bson.D:
		return d, nil
	}
	return nil, fmt.Errorf("%s must be a document", what)
}

// EvalArray parses a shell-syntax array (e.g. a pipeline).
func EvalArray(text, what string) (bson.A, error) {
	v, err := Eval(text)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	switch a := v.(type) {
	case nil:
		return bson.A{}, nil
	case bson.A:
		return a, nil
	}
	return nil, fmt.Errorf("%s must be an array", what)
}

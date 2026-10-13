package dialect

import (
	"strconv"
	"strings"

	"github.com/iagapie/quire/internal/sqlsplit"
)

// Kind is what a statement does, as far as running it is concerned.
type Kind int

const (
	// KindQuery returns rows (SELECT, SHOW, EXPLAIN...).
	KindQuery Kind = iota
	// KindExec changes data or schema and reports affected rows.
	KindExec
	// KindOther may or may not return rows (CALL, EXEC, batches...).
	KindOther
)

// Analysis is what the application learns about a statement before running it.
type Analysis struct {
	Kind Kind
	// Verb is the leading keyword (SELECT, INSERT, ...), or the main verb after WITH.
	Verb string
	// ReadOnly tells the statement only reads data.
	ReadOnly bool
	// Wrappable tells the statement can be used as a subquery (for paging and counting).
	Wrappable bool
	// Transaction is set for BEGIN/COMMIT/ROLLBACK/START TRANSACTION.
	Transaction string
}

// PRAGMAs that change the database even without "=".
var writingPragmas = map[string]bool{
	"OPTIMIZE": true, "WAL_CHECKPOINT": true, "INCREMENTAL_VACUUM": true, "SHRINK_MEMORY": true,
	"INTEGRITY_CHECK": false, "QUICK_CHECK": false,
}

// Analyze classifies a statement.
func Analyze(stmt string, o sqlsplit.Options) Analysis {
	toks := Lex(stmt, o)
	return analyze(toks)
}

func analyze(toks []Token) Analysis {
	// Skip leading parentheses: (SELECT ...) UNION (...).
	i := 0
	for i < len(toks) && toks[i].Value == "(" {
		i++
	}
	if i >= len(toks) || toks[i].Kind != TokWord {
		return Analysis{Kind: KindOther}
	}
	verb := toks[i].Value
	a := Analysis{Verb: verb}
	switch verb {
	case "WITH":
		main := mainVerbAfterWith(toks[i:])
		a.Verb = main
		switch main {
		case "SELECT", "VALUES", "TABLE":
			a.Kind, a.ReadOnly, a.Wrappable = KindQuery, !hasTopLevel(toks, "INTO"), true
		case "INSERT", "UPDATE", "DELETE", "MERGE":
			a.Kind = KindExec
			if hasWord(toks, "RETURNING") {
				a.Kind = KindQuery
			}
		default:
			a.Kind = KindOther
		}
	case "SELECT":
		// SELECT ... INTO creates a table (PostgreSQL, SQL Server) or sets variables.
		a.Kind, a.ReadOnly, a.Wrappable = KindQuery, !hasTopLevel(toks, "INTO"), true
		if hasTopLevel(toks, "INTO") {
			a.Kind, a.Wrappable = KindOther, false
		}
	case "VALUES", "TABLE":
		a.Kind, a.ReadOnly, a.Wrappable = KindQuery, true, true
	case "SHOW", "DESCRIBE", "DESC":
		a.Kind, a.ReadOnly = KindQuery, true
	case "EXPLAIN":
		a.Kind = KindQuery
		// EXPLAIN ANALYZE runs the statement.
		rest := toks[i+1:]
		a.ReadOnly = true
		if hasWord(rest, "ANALYZE") || hasWord(rest, "ANALYSE") {
			inner := analyze(skipExplainOptions(rest))
			a.ReadOnly = inner.ReadOnly
		}
	case "PRAGMA":
		a.Kind = KindQuery
		a.ReadOnly = !hasPunct(toks, "=") && (i+1 >= len(toks) || !writingPragmas[lastWordOfPragma(toks[i+1:])])
	case "INSERT", "UPDATE", "DELETE", "MERGE", "UPSERT", "REPLACE":
		a.Kind = KindExec
		if hasWord(toks, "RETURNING") || hasTopLevel(toks, "OUTPUT") {
			a.Kind = KindQuery
		}
	case "CREATE", "ALTER", "DROP", "TRUNCATE", "GRANT", "REVOKE", "COMMENT", "RENAME", "ANALYZE", "VACUUM",
		"REINDEX", "CLUSTER", "REFRESH", "LOCK", "SET", "RESET", "USE", "ATTACH", "DETACH", "DISCARD", "LISTEN",
		"NOTIFY", "UNLISTEN", "DEALLOCATE", "PREPARE", "SAVEPOINT", "RELEASE", "DECLARE", "FETCH", "CLOSE", "MOVE",
		"KILL", "FLUSH", "OPTIMIZE", "LOAD", "COPY":
		a.Kind = KindExec
		if verb == "FETCH" || verb == "MOVE" {
			a.Kind = KindOther
		}
		if verb == "USE" {
			a.ReadOnly = true
		}
	case "BEGIN", "START":
		a.Kind = KindExec
		if verb == "START" || len(toks) == i+1 || toks[i+1].Is("TRANSACTION") || toks[i+1].Is("WORK") ||
			toks[i+1].Is("TRAN") || toks[i+1].Is("DEFERRED") || toks[i+1].Is("IMMEDIATE") || toks[i+1].Is("EXCLUSIVE") ||
			toks[i+1].Is("ISOLATION") || toks[i+1].Is("READ") {
			a.Transaction = "BEGIN"
		} else {
			a.Kind = KindOther // a PL/SQL or T-SQL block
		}
	case "COMMIT", "END":
		a.Kind, a.Transaction, a.ReadOnly = KindExec, "COMMIT", true
	case "ROLLBACK", "ABORT":
		a.Kind, a.ReadOnly = KindExec, true
		if !hasWord(toks, "TO") {
			a.Transaction = "ROLLBACK"
		}
	default:
		a.Kind = KindOther
	}
	return a
}

func skipExplainOptions(toks []Token) []Token {
	i := 0
	if i < len(toks) && toks[i].Value == "(" {
		depth := 0
		for i < len(toks) {
			if toks[i].Value == "(" {
				depth++
			} else if toks[i].Value == ")" {
				depth--
				if depth == 0 {
					i++
					break
				}
			}
			i++
		}
	}
	for i < len(toks) && toks[i].Kind == TokWord {
		switch toks[i].Value {
		case "ANALYZE", "ANALYSE", "VERBOSE", "QUERY", "PLAN", "FORMAT", "EXTENDED", "PARTITIONS", "JSON", "TREE", "TRADITIONAL":
			i++
			continue
		}
		break
	}
	return toks[i:]
}

func lastWordOfPragma(toks []Token) string {
	// PRAGMA schema.name or PRAGMA name
	name := ""
	for _, t := range toks {
		if t.Kind == TokWord || t.Kind == TokIdent {
			name = strings.ToUpper(t.Value)
			continue
		}
		if t.Value != "." {
			break
		}
	}
	return name
}

// mainVerbAfterWith finds the statement keyword following the CTE list.
func mainVerbAfterWith(toks []Token) string {
	depth := 0
	for k, t := range toks {
		switch t.Value {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth == 0 && k > 0 && t.Kind == TokWord {
			switch t.Value {
			case "SELECT", "INSERT", "UPDATE", "DELETE", "MERGE", "VALUES", "TABLE":
				return t.Value
			}
		}
	}
	return ""
}

func hasWord(toks []Token, w string) bool {
	for _, t := range toks {
		if t.Is(w) {
			return true
		}
	}
	return false
}

func hasPunct(toks []Token, p string) bool {
	for _, t := range toks {
		if t.Kind == TokPunct && t.Value == p {
			return true
		}
	}
	return false
}

// hasTopLevel reports whether keyword w appears outside parentheses.
func hasTopLevel(toks []Token, w string) bool {
	depth := 0
	for _, t := range toks {
		switch t.Value {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth == 0 && t.Is(w) {
			return true
		}
	}
	return false
}

// ReadOnlyError is returned by CheckReadOnly.
type ReadOnlyError struct{ Verb string }

func (e *ReadOnlyError) Error() string {
	if e.Verb == "" {
		return "this connection is read-only: only SELECT, WITH, EXPLAIN, SHOW, DESCRIBE and PRAGMA statements can run"
	}
	return "this connection is read-only: " + e.Verb + " statements cannot run (only SELECT, WITH, EXPLAIN, SHOW, DESCRIBE and PRAGMA)"
}

// CheckReadOnly is the client-side guard of read-only connections.
func CheckReadOnly(a Analysis) error {
	if a.ReadOnly {
		return nil
	}
	return &ReadOnlyError{Verb: a.Verb}
}

// TableRef is the table a single-table query reads. Unquoted names are as written;
// the Quoted flags tell which were quoted (and must not be case-folded).
type TableRef struct {
	Schema       string `json:"schema"`
	Table        string `json:"table"`
	SchemaQuoted bool   `json:"schemaQuoted"`
	TableQuoted  bool   `json:"tableQuoted"`
	// Columns are the selected columns, or nil for *.
	Columns []string `json:"columns"`
	Where   string   `json:"where"`
	OrderBy string   `json:"orderBy"`
	// Limit is the row limit written in the query (LIMIT, TOP, FETCH FIRST), or 0.
	Limit int64 `json:"limit"`
}

// SingleTable reports whether stmt is a simple SELECT from one table, which the result
// grid can filter, sort and edit. Database-qualified names are not editable.
func SingleTable(stmt string, o sqlsplit.Options) (TableRef, bool) {
	toks := Lex(stmt, o)
	// A batch (SQL Server) keeps its terminator.
	for len(toks) > 0 && toks[len(toks)-1].Kind == TokPunct && toks[len(toks)-1].Value == ";" {
		toks = toks[:len(toks)-1]
	}
	if len(toks) < 4 || !toks[0].Is("SELECT") {
		return TableRef{}, false
	}
	i := 1
	var ref TableRef
	// TOP n (SQL Server)
	if toks[i].Is("TOP") {
		i++
		if i < len(toks) && toks[i].Value == "(" {
			i++
		}
		if i < len(toks) && toks[i].Kind == TokNumber {
			ref.Limit, _ = strconv.ParseInt(toks[i].Value, 10, 64)
		}
		for i < len(toks) && (toks[i].Kind == TokNumber || toks[i].Value == ")") {
			i++
		}
	}
	if i < len(toks) && (toks[i].Is("DISTINCT") || toks[i].Is("ALL")) {
		return TableRef{}, false
	}
	star := false
	// Select list: *, alias.*, or plain (qualified) column names.
	for {
		if i >= len(toks) {
			return TableRef{}, false
		}
		start := i
		var last string
		for i < len(toks) {
			t := toks[i]
			switch {
			case t.Kind == TokPunct && t.Value == "*":
				star = true
				last = "*"
			case t.Kind == TokWord && !reserved[t.Value]:
				last = t.Text
			case t.Kind == TokIdent:
				last = t.Value
			default:
				return TableRef{}, false
			}
			i++
			if i < len(toks) && toks[i].Value == "." {
				i++
				continue
			}
			break
		}
		if i == start {
			return TableRef{}, false
		}
		if last != "*" {
			ref.Columns = append(ref.Columns, last)
		}
		if i < len(toks) && toks[i].Value == "," {
			i++
			continue
		}
		break
	}
	if star {
		ref.Columns = nil
	}
	if i >= len(toks) || !toks[i].Is("FROM") {
		return TableRef{}, false
	}
	i++
	var parts []string
	var quoted []bool
	for i < len(toks) && (toks[i].Kind == TokIdent || (toks[i].Kind == TokWord && !reserved[toks[i].Value])) {
		if toks[i].Kind == TokIdent {
			parts = append(parts, toks[i].Value)
		} else {
			parts = append(parts, toks[i].Text)
		}
		quoted = append(quoted, toks[i].Kind == TokIdent)
		i++
		if i < len(toks) && toks[i].Value == "." {
			i++
			continue
		}
		break
	}
	switch len(parts) {
	case 1:
		ref.Table, ref.TableQuoted = parts[0], quoted[0]
	case 2:
		ref.Schema, ref.Table = parts[0], parts[1]
		ref.SchemaQuoted, ref.TableQuoted = quoted[0], quoted[1]
	default:
		return TableRef{}, false
	}
	// Optional alias.
	if i < len(toks) && toks[i].Is("AS") {
		i++
	}
	if i < len(toks) && (toks[i].Kind == TokIdent || (toks[i].Kind == TokWord && !reserved[toks[i].Value])) {
		i++
	}
	// The rest may only be WHERE, ORDER BY and row limits.
	whereAt, orderAt, endAt := -1, -1, len(stmt)
	depth := 0
	for k := i; k < len(toks); k++ {
		t := toks[k]
		switch t.Value {
		case "(":
			depth++
			continue
		case ")":
			depth--
			continue
		case ",", ";":
			if depth == 0 && whereAt < 0 && orderAt < 0 {
				return TableRef{}, false
			}
		}
		if depth > 0 || t.Kind != TokWord {
			continue
		}
		switch t.Value {
		case "JOIN", "UNION", "INTERSECT", "EXCEPT", "MINUS", "GROUP", "HAVING", "WINDOW", "INTO", "CONNECT", "START", "PIVOT", "UNPIVOT", "QUALIFY", "SAMPLE", "FINAL", "NATURAL", "CROSS", "APPLY":
			return TableRef{}, false
		case "WHERE":
			if whereAt >= 0 || orderAt >= 0 {
				return TableRef{}, false
			}
			whereAt = k
		case "ORDER":
			if k+1 < len(toks) && toks[k+1].Is("BY") {
				orderAt = k
			}
		case "LIMIT", "OFFSET", "FETCH", "FOR":
			if endAt == len(stmt) {
				endAt = t.Pos
			}
			if t.Value == "LIMIT" && k+1 < len(toks) && toks[k+1].Kind == TokNumber {
				ref.Limit, _ = strconv.ParseInt(toks[k+1].Value, 10, 64)
			}
			if t.Value == "FETCH" && k+2 < len(toks) && toks[k+2].Kind == TokNumber {
				ref.Limit, _ = strconv.ParseInt(toks[k+2].Value, 10, 64)
			}
		}
	}
	if whereAt < 0 && orderAt < 0 && i < len(toks) && endAt == len(stmt) {
		// Anything else after the table name (e.g. a table hint) is not understood.
		return TableRef{}, false
	}
	if whereAt >= 0 {
		end := endAt
		if orderAt >= 0 {
			end = toks[orderAt].Pos
		}
		ref.Where = strings.TrimSpace(stmt[toks[whereAt].End:end])
	}
	if orderAt >= 0 {
		ref.OrderBy = strings.TrimSpace(stmt[toks[orderAt+1].End:endAt])
	}
	return ref, true
}

// reserved words that cannot be bare identifiers in the patterns above.
var reserved = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "ORDER": true, "GROUP": true, "BY": true, "HAVING": true,
	"LIMIT": true, "OFFSET": true, "FETCH": true, "JOIN": true, "INNER": true, "LEFT": true, "RIGHT": true,
	"FULL": true, "OUTER": true, "CROSS": true, "ON": true, "AS": true, "UNION": true, "INTERSECT": true,
	"EXCEPT": true, "MINUS": true, "INTO": true, "DISTINCT": true, "ALL": true, "CASE": true, "WHEN": true,
	"THEN": true, "ELSE": true, "END": true, "AND": true, "OR": true, "NOT": true, "NULL": true, "FOR": true,
	"WINDOW": true, "TOP": true, "WITH": true, "NATURAL": true, "USING": true, "TABLESAMPLE": true,
}

// TopLevel reports whether words appear in a row outside parentheses in stmt (e.g.
// "ORDER", "BY").
func TopLevel(stmt string, o sqlsplit.Options, words ...string) bool {
	toks := Lex(stmt, o)
	depth := 0
	for i, t := range toks {
		switch t.Value {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth != 0 || i+len(words) > len(toks) {
			continue
		}
		match := true
		for k, w := range words {
			if !toks[i+k].Is(w) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

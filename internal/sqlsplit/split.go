// Package sqlsplit splits SQL scripts into statements. It understands the lexical rules
// that decide where a statement ends in each supported dialect: quoted strings and
// identifiers, comments, PostgreSQL dollar quoting, SQL Server GO batches, Oracle
// PL/SQL blocks ended by "/", the MySQL DELIMITER command and BEGIN…END bodies of
// triggers and routines.
package sqlsplit

import (
	"strings"
	"unicode/utf8"
)

// Options select the dialect rules.
type Options struct {
	// HashComments treats "#" as a line comment (MySQL).
	HashComments bool `json:"hashComments"`
	// Backticks quotes identifiers with `x` (MySQL, SQLite, ClickHouse).
	Backticks bool `json:"backticks"`
	// Brackets quotes identifiers with [x] (SQL Server, SQLite).
	Brackets bool `json:"brackets"`
	// DollarQuotes enables $$…$$ and $tag$…$tag$ strings (PostgreSQL).
	DollarQuotes bool `json:"dollarQuotes"`
	// NestedComments allows /* /* */ */ (PostgreSQL).
	NestedComments bool `json:"nestedComments"`
	// BackslashEscapes makes \ escape the next character in strings (MySQL).
	BackslashEscapes bool `json:"backslashEscapes"`
	// EStrings enables E'…' strings with backslash escapes (PostgreSQL).
	EStrings bool `json:"eStrings"`
	// QQuotes enables q'[…]' strings (Oracle).
	QQuotes bool `json:"qQuotes"`
	// GoBatches splits on a line holding only GO (SQL Server). Semicolons then do not
	// split, because variables live as long as the batch.
	GoBatches bool `json:"goBatches"`
	// SlashTerminator ends a statement at a line holding only "/" and runs PL/SQL
	// blocks (DECLARE, BEGIN, CREATE PROCEDURE…) until such a line (Oracle).
	SlashTerminator bool `json:"slashTerminator"`
	// Delimiter honours the DELIMITER command (MySQL).
	Delimiter bool `json:"delimiter"`
	// BlockStatements keeps the semicolons inside the BEGIN…END body of CREATE
	// TRIGGER/PROCEDURE/FUNCTION/EVENT (SQLite, MySQL without DELIMITER).
	BlockStatements bool `json:"blockStatements"`
	// BlankLines ends a statement at an empty line, besides the usual terminators.
	BlankLines bool `json:"blankLines"`
}

// Statement is one statement of a script.
type Statement struct {
	// Text is the statement without leading comments and without the terminator.
	Text string
	// Start and End are the byte offsets of Text in the script.
	Start int
	End   int
	// Line is the 1-based line where Text starts.
	Line int
}

// Split returns the statements of script. Empty statements and statements made only
// of comments are dropped.
func Split(script string, o Options) []Statement {
	s := &splitter{src: script, o: o, delim: ";"}
	s.run()
	return s.out
}

type splitter struct {
	src   string
	o     Options
	out   []Statement
	delim string

	start int // start of the current statement
	// head holds the first words of the current statement, upper case.
	head  []string
	depth int // BEGIN/CASE nesting in block statements
	// code reports whether the current statement has anything but whitespace/comments.
	code bool
}

func (s *splitter) run() {
	i := 0
	for i < len(s.src) {
		if s.lineStart(i) {
			if n, ok := s.lineCommand(i); ok {
				i = n
				continue
			}
		}
		c := s.src[i]
		switch {
		case c == '\n' || c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && s.at(i+1) == '-':
			i = s.lineEnd(i)
		case c == '#' && s.o.HashComments:
			i = s.lineEnd(i)
		case c == '/' && s.at(i+1) == '*':
			i = s.blockComment(i)
		case c == '\'':
			s.code = true
			i = s.quoted(i, '\'', s.o.BackslashEscapes || (s.o.EStrings && s.ePrefix(i)))
		case (c == 'q' || c == 'Q') && s.o.QQuotes && s.at(i+1) == '\'' && !identByte(s.prev(i)):
			s.code = true
			i = s.qQuote(i)
		case c == '"':
			s.code = true
			i = s.quoted(i, '"', false)
		case c == '`' && s.o.Backticks:
			s.code = true
			i = s.quoted(i, '`', false)
		case c == '[' && s.o.Brackets:
			s.code = true
			i = s.quoted(i, ']', false)
		case c == '$' && s.o.DollarQuotes && !identByte(s.prev(i)):
			s.code = true
			i = s.dollar(i)
		case s.delim != ";" && strings.HasPrefix(s.src[i:], s.delim):
			s.emit(i)
			i += len(s.delim)
			s.reset(i)
		case c == ';' && s.delim == ";":
			if s.o.GoBatches || s.depth > 0 || s.plsql() {
				i++
				continue
			}
			s.emit(i)
			i++
			s.reset(i)
		case identStart(c):
			s.code = true
			i = s.word(i)
		default:
			s.code = true
			_, n := utf8.DecodeRuneInString(s.src[i:])
			i += n
		}
	}
	s.emit(len(s.src))
}

func (s *splitter) at(i int) byte {
	if i < len(s.src) {
		return s.src[i]
	}
	return 0
}

func (s *splitter) prev(i int) byte {
	if i > 0 {
		return s.src[i-1]
	}
	return 0
}

func (s *splitter) lineStart(i int) bool {
	return i == 0 || s.src[i-1] == '\n'
}

// lineEnd returns the index of the newline ending the line at i (or len(src)).
func (s *splitter) lineEnd(i int) int {
	if n := strings.IndexByte(s.src[i:], '\n'); n >= 0 {
		return i + n
	}
	return len(s.src)
}

// lineCommand handles the line-level commands: GO, "/", DELIMITER and blank lines.
// It returns the index after the line when the line was consumed.
func (s *splitter) lineCommand(i int) (int, bool) {
	end := s.lineEnd(i)
	line := strings.TrimSpace(s.src[i:end])
	next := min(end+1, len(s.src))
	switch {
	case s.o.GoBatches && isGo(line):
		s.emit(i)
		s.reset(next)
		return next, true
	case s.o.SlashTerminator && line == "/":
		s.emit(i)
		s.reset(next)
		return next, true
	case s.o.Delimiter && len(line) > 10 && strings.EqualFold(line[:10], "DELIMITER ") && s.depth == 0:
		if d := strings.TrimSpace(line[10:]); d != "" {
			s.emit(i)
			s.delim = d
			s.reset(next)
			return next, true
		}
	case s.o.BlankLines && line == "" && s.code && s.depth == 0 && !s.plsql():
		s.emit(i)
		s.reset(next)
		return next, true
	}
	return 0, false
}

func isGo(line string) bool {
	if len(line) < 2 || !strings.EqualFold(line[:2], "GO") {
		return false
	}
	rest := strings.TrimSpace(line[2:])
	if rest == "" {
		return true
	}
	if strings.HasPrefix(rest, "--") {
		return true
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (s *splitter) blockComment(i int) int {
	depth := 0
	j := i
	for j < len(s.src) {
		switch {
		case s.src[j] == '/' && s.at(j+1) == '*':
			if depth == 0 || s.o.NestedComments {
				depth++
			}
			j += 2
		case s.src[j] == '*' && s.at(j+1) == '/':
			depth--
			j += 2
			if depth == 0 {
				return j
			}
		default:
			j++
		}
	}
	return len(s.src)
}

// quoted skips a string or quoted identifier starting at i; a doubled closing quote
// is an escaped quote.
func (s *splitter) quoted(i int, closer byte, backslash bool) int {
	j := i + 1
	for j < len(s.src) {
		c := s.src[j]
		switch {
		case backslash && c == '\\':
			j += 2
		case c == closer:
			if s.at(j+1) == closer {
				j += 2
				continue
			}
			return j + 1
		default:
			j++
		}
	}
	return len(s.src)
}

func (s *splitter) ePrefix(i int) bool {
	if i == 0 || (s.src[i-1] != 'e' && s.src[i-1] != 'E') {
		return false
	}
	return i < 2 || !identByte(s.src[i-2])
}

// qQuote skips an Oracle q'X…X' string.
func (s *splitter) qQuote(i int) int {
	if i+2 >= len(s.src) {
		return len(s.src)
	}
	open := s.src[i+2]
	closer := open
	switch open {
	case '[':
		closer = ']'
	case '{':
		closer = '}'
	case '(':
		closer = ')'
	case '<':
		closer = '>'
	}
	j := i + 3
	for j < len(s.src) {
		if s.src[j] == closer && s.at(j+1) == '\'' {
			return j + 2
		}
		j++
	}
	return len(s.src)
}

// dollar skips a PostgreSQL dollar-quoted string ($$…$$ or $tag$…$tag$). A "$" that
// does not start one (e.g. the parameter $1) is a single character.
func (s *splitter) dollar(i int) int {
	j := i + 1
	for j < len(s.src) && (identStart(s.src[j]) || (j > i+1 && isDigit(s.src[j]))) {
		j++
	}
	if j >= len(s.src) || s.src[j] != '$' {
		return i + 1
	}
	tag := s.src[i : j+1]
	if n := strings.Index(s.src[j+1:], tag); n >= 0 {
		return j + 1 + n + len(tag)
	}
	return len(s.src)
}

// word reads an identifier or keyword and tracks the statement head and BEGIN…END
// nesting.
func (s *splitter) word(i int) int {
	j := i
	for j < len(s.src) && identByte(s.src[j]) {
		if s.delim != ";" && j > i && strings.HasPrefix(s.src[j:], s.delim) {
			break
		}
		j++
	}
	w := strings.ToUpper(s.src[i:j])
	if len(s.head) < 8 {
		s.head = append(s.head, w)
	}
	if s.o.BlockStatements && s.blockHead() {
		switch w {
		case "BEGIN", "CASE":
			s.depth++
		case "END":
			if s.depth > 0 {
				switch s.nextWord(j) {
				case "IF", "LOOP", "WHILE", "REPEAT", "FOR":
				default:
					s.depth--
				}
			}
		}
	}
	return j
}

// nextWord returns the next word after i on the same statement, upper case.
func (s *splitter) nextWord(i int) string {
	for i < len(s.src) && (s.src[i] == ' ' || s.src[i] == '\t' || s.src[i] == '\n' || s.src[i] == '\r') {
		i++
	}
	j := i
	for j < len(s.src) && identByte(s.src[j]) {
		j++
	}
	return strings.ToUpper(s.src[i:j])
}

// blockHead reports whether the statement creates a trigger or routine with a body.
func (s *splitter) blockHead() bool {
	if len(s.head) < 2 || s.head[0] != "CREATE" {
		return false
	}
	for _, w := range s.head[1:] {
		switch w {
		case "TRIGGER", "PROCEDURE", "FUNCTION", "EVENT":
			return true
		}
	}
	return false
}

// plsql reports whether the current statement is an Oracle PL/SQL unit, which only
// "/" ends.
func (s *splitter) plsql() bool {
	if !s.o.SlashTerminator || len(s.head) == 0 {
		return false
	}
	switch s.head[0] {
	case "DECLARE", "BEGIN":
		return true
	case "CREATE":
		for _, w := range s.head[1:] {
			switch w {
			case "PROCEDURE", "FUNCTION", "PACKAGE", "TRIGGER", "TYPE":
				return true
			case "OR", "REPLACE", "EDITIONABLE", "NONEDITIONABLE", "EDITIONING", "BODY":
				continue
			}
			return false
		}
	}
	return false
}

func (s *splitter) reset(i int) {
	s.start = i
	s.head = s.head[:0]
	s.depth = 0
	s.code = false
}

// emit adds the statement between s.start and end, trimmed of whitespace and leading
// comments.
func (s *splitter) emit(end int) {
	a, b := s.start, end
	for {
		for a < b && isSpace(s.src[a]) {
			a++
		}
		switch {
		case a+1 < b && s.src[a] == '-' && s.src[a+1] == '-':
			a = min(s.lineEnd(a), b)
			continue
		case a < b && s.src[a] == '#' && s.o.HashComments:
			a = min(s.lineEnd(a), b)
			continue
		case a+1 < b && s.src[a] == '/' && s.src[a+1] == '*':
			a = min(s.blockComment(a), b)
			continue
		}
		break
	}
	for b > a && isSpace(s.src[b-1]) {
		b--
	}
	if a >= b {
		return
	}
	s.out = append(s.out, Statement{Text: s.src[a:b], Start: a, End: b, Line: strings.Count(s.src[:a], "\n") + 1})
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\t' || c == '\r' || c == '\f' || c == '\v'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func identStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func identByte(c byte) bool {
	return identStart(c) || isDigit(c) || c == '$'
}

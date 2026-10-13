package dialect

import (
	"strings"

	"github.com/iagapie/quire/internal/sqlsplit"
)

// Token kinds.
const (
	TokWord   = iota // keyword or bare identifier
	TokIdent         // quoted identifier
	TokString        // string literal
	TokNumber
	TokPunct
)

// Token is a lexical token of a statement.
type Token struct {
	Kind int
	// Text is the source text; Value is the unquoted identifier or upper-cased word.
	Text  string
	Value string
	Pos   int
	End   int
}

// Is reports whether t is the keyword w (upper case).
func (t Token) Is(w string) bool { return t.Kind == TokWord && t.Value == w }

// Lex splits a statement into tokens, skipping comments and whitespace.
func Lex(stmt string, o sqlsplit.Options) []Token {
	var out []Token
	i := 0
	for i < len(stmt) {
		c := stmt[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && at(stmt, i+1) == '-', c == '#' && o.HashComments:
			for i < len(stmt) && stmt[i] != '\n' {
				i++
			}
		case c == '/' && at(stmt, i+1) == '*':
			depth := 0
			for i < len(stmt) {
				if stmt[i] == '/' && at(stmt, i+1) == '*' {
					if depth == 0 || o.NestedComments {
						depth++
					}
					i += 2
				} else if stmt[i] == '*' && at(stmt, i+1) == '/' {
					depth--
					i += 2
					if depth == 0 {
						break
					}
				} else {
					i++
				}
			}
		case c == '\'':
			j := skipQuoted(stmt, i, '\'', o.BackslashEscapes)
			out = append(out, Token{Kind: TokString, Text: stmt[i:j], Value: unquote(stmt[i:j], '\''), Pos: i, End: j})
			i = j
		case c == '"':
			j := skipQuoted(stmt, i, '"', false)
			out = append(out, Token{Kind: TokIdent, Text: stmt[i:j], Value: unquote(stmt[i:j], '"'), Pos: i, End: j})
			i = j
		case c == '`' && o.Backticks:
			j := skipQuoted(stmt, i, '`', false)
			out = append(out, Token{Kind: TokIdent, Text: stmt[i:j], Value: unquote(stmt[i:j], '`'), Pos: i, End: j})
			i = j
		case c == '[' && o.Brackets:
			j := skipQuoted(stmt, i, ']', false)
			out = append(out, Token{Kind: TokIdent, Text: stmt[i:j], Value: unquote(stmt[i:j], ']'), Pos: i, End: j})
			i = j
		case c == '$' && o.DollarQuotes && at(stmt, i+1) != 0 && !isDigitByte(at(stmt, i+1)):
			j := i + 1
			for j < len(stmt) && wordByte(stmt[j]) && stmt[j] != '$' {
				j++
			}
			if j < len(stmt) && stmt[j] == '$' {
				tag := stmt[i : j+1]
				end := len(stmt)
				if n := strings.Index(stmt[j+1:], tag); n >= 0 {
					end = j + 1 + n + len(tag)
				}
				out = append(out, Token{Kind: TokString, Text: stmt[i:end], Pos: i, End: end})
				i = end
			} else {
				out = append(out, Token{Kind: TokPunct, Text: "$", Value: "$", Pos: i, End: i + 1})
				i++
			}
		case isDigitByte(c):
			j := i
			for j < len(stmt) && (isDigitByte(stmt[j]) || stmt[j] == '.' || stmt[j] == 'e' || stmt[j] == 'E') {
				j++
			}
			out = append(out, Token{Kind: TokNumber, Text: stmt[i:j], Value: stmt[i:j], Pos: i, End: j})
			i = j
		case wordStart(c):
			j := i
			for j < len(stmt) && wordByte(stmt[j]) {
				j++
			}
			out = append(out, Token{Kind: TokWord, Text: stmt[i:j], Value: strings.ToUpper(stmt[i:j]), Pos: i, End: j})
			i = j
		default:
			n := 1
			if i+1 < len(stmt) {
				switch stmt[i : i+2] {
				case "::", "<=", ">=", "<>", "!=", "||", "->", "=>":
					n = 2
				}
			}
			out = append(out, Token{Kind: TokPunct, Text: stmt[i : i+n], Value: stmt[i : i+n], Pos: i, End: i + n})
			i += n
		}
	}
	return out
}

func at(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func skipQuoted(s string, i int, closer byte, backslash bool) int {
	j := i + 1
	for j < len(s) {
		switch {
		case backslash && s[j] == '\\':
			j += 2
		case s[j] == closer:
			if at(s, j+1) == closer {
				j += 2
				continue
			}
			return j + 1
		default:
			j++
		}
	}
	return len(s)
}

func unquote(s string, closer byte) string {
	if len(s) < 2 {
		return ""
	}
	inner := s[1:]
	if inner[len(inner)-1] == closer {
		inner = inner[:len(inner)-1]
	}
	q := string(closer)
	return strings.ReplaceAll(inner, q+q, q)
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

func wordStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80 || c == '@'
}

func wordByte(c byte) bool {
	return wordStart(c) || isDigitByte(c) || c == '$' || c == '#'
}

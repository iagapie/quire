package sqlsplit

// At returns the statement under the cursor (a byte offset): the one containing it,
// else the closest one ending before it on the same line, else one starting on the
// cursor line, else the last one before it. ok is false when the script has none.
func At(script string, stmts []Statement, cursor int) (Statement, bool) {
	var before *Statement
	line := lineOf(script, cursor)
	for i := range stmts {
		st := &stmts[i]
		if cursor >= st.Start && cursor <= st.End {
			return *st, true
		}
		if st.End <= cursor {
			before = st
		}
	}
	if before != nil && lineOf(script, before.End) == line {
		return *before, true
	}
	for _, st := range stmts {
		if st.Line == line {
			return st, true
		}
	}
	if before != nil {
		return *before, true
	}
	if len(stmts) > 0 && lineOf(script, stmts[0].Start) == line {
		return stmts[0], true
	}
	return Statement{}, false
}

// Within returns the statements of script[from:to], with offsets and lines relative to
// the whole script.
func Within(script string, from, to int, o Options) []Statement {
	from = max(0, min(from, len(script)))
	to = max(from, min(to, len(script)))
	stmts := Split(script[from:to], o)
	base := lineOf(script, from) - 1
	for i := range stmts {
		stmts[i].Start += from
		stmts[i].End += from
		stmts[i].Line += base
	}
	return stmts
}

func lineOf(s string, offset int) int {
	offset = max(0, min(offset, len(s)))
	n := 1
	for i := range offset {
		if s[i] == '\n' {
			n++
		}
	}
	return n
}

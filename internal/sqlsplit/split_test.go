package sqlsplit

import (
	"strings"
	"testing"
)

var (
	postgres = Options{DollarQuotes: true, NestedComments: true, EStrings: true}
	mysql    = Options{HashComments: true, Backticks: true, BackslashEscapes: true, Delimiter: true, BlockStatements: true}
	sqlite   = Options{Backticks: true, Brackets: true, BlockStatements: true}
	mssql    = Options{Brackets: true, GoBatches: true}
	oracle   = Options{QQuotes: true, SlashTerminator: true}
)

func texts(stmts []Statement) []string {
	out := make([]string, len(stmts))
	for i, s := range stmts {
		out[i] = s.Text
	}
	return out
}

func TestSplit(t *testing.T) {
	cases := []struct {
		name   string
		o      Options
		script string
		want   []string
	}{
		{"simple", Options{}, "select 1; select 2;", []string{"select 1", "select 2"}},
		{"no terminator", Options{}, "select 1;\nselect 2", []string{"select 1", "select 2"}},
		{"empty statements", Options{}, ";;\n ; select 1;;", []string{"select 1"}},
		{"semicolon in string", Options{}, "select ';' as a; select 'it''s; ok'", []string{"select ';' as a", "select 'it''s; ok'"}},
		{"semicolon in identifier", Options{}, `select 1 as "a;b"; select 2`, []string{`select 1 as "a;b"`, "select 2"}},
		{"line comment", Options{}, "select 1 -- not; here\n; select 2", []string{"select 1 -- not; here", "select 2"}},
		{"block comment", Options{}, "select /* ; */ 1; select 2", []string{"select /* ; */ 1", "select 2"}},
		{"leading comments dropped", Options{}, "-- intro\n/* doc */\nselect 1;", []string{"select 1"}},
		{"only comments", Options{}, "-- nothing\n/* here */", nil},
		{"unterminated string", Options{}, "select 'abc; def", []string{"select 'abc; def"}},
		{"pg dollar", postgres, "create function f() returns int as $$ select 1; $$ language sql; select 2",
			[]string{"create function f() returns int as $$ select 1; $$ language sql", "select 2"}},
		{"pg tagged dollar", postgres, "do $body$ begin perform 1; end $body$; select 3",
			[]string{"do $body$ begin perform 1; end $body$", "select 3"}},
		{"pg dollar param", postgres, "select $1; select 2", []string{"select $1", "select 2"}},
		{"pg nested comment", postgres, "select /* a /* ; */ b; */ 1; select 2", []string{"select /* a /* ; */ b; */ 1", "select 2"}},
		{"pg e-string", postgres, `select E'a\'; b'; select 2`, []string{`select E'a\'; b'`, "select 2"}},
		{"plain string backslash", postgres, `select 'a\'; select 2`, []string{`select 'a\'`, "select 2"}},
		{"mysql hash", mysql, "select 1 # c;\n; select 2", []string{"select 1 # c;", "select 2"}},
		{"mysql backtick", mysql, "select `a;b` from t; select 2", []string{"select `a;b` from t", "select 2"}},
		{"mysql backslash", mysql, `select 'a\'; b'; select 2`, []string{`select 'a\'; b'`, "select 2"}},
		{"mysql delimiter", mysql, "DELIMITER $$\nCREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END$$\nDELIMITER ;\nSELECT 3;",
			[]string{"CREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END", "SELECT 3"}},
		{"mysql block without delimiter", mysql, "CREATE PROCEDURE p() BEGIN IF 1 THEN SELECT 1; END IF; SELECT 2; END; SELECT 3",
			[]string{"CREATE PROCEDURE p() BEGIN IF 1 THEN SELECT 1; END IF; SELECT 2; END", "SELECT 3"}},
		{"begin transaction", mysql, "BEGIN; SELECT 1; COMMIT;", []string{"BEGIN", "SELECT 1", "COMMIT"}},
		{"sqlite trigger", sqlite,
			"CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE b SET x = CASE WHEN 1 THEN 2 END; DELETE FROM c; END; SELECT 1",
			[]string{"CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE b SET x = CASE WHEN 1 THEN 2 END; DELETE FROM c; END", "SELECT 1"}},
		{"sqlite brackets", sqlite, "select [a;b]; select 2", []string{"select [a;b]", "select 2"}},
		{"mssql go", mssql, "DECLARE @x int; SET @x = 1;\nSELECT @x\nGO\nSELECT 2\ngo 3\nSELECT [a]]; b]",
			[]string{"DECLARE @x int; SET @x = 1;\nSELECT @x", "SELECT 2", "SELECT [a]]; b]"}},
		{"mssql goto is not go", mssql, "SELECT 1\nGOTO x\nSELECT 2", []string{"SELECT 1\nGOTO x\nSELECT 2"}},
		{"oracle sql", oracle, "select 1 from dual;\nselect 2 from dual", []string{"select 1 from dual", "select 2 from dual"}},
		{"oracle plsql", oracle, "BEGIN\n  dbms_output.put_line('x');\nEND;\n/\nselect 1 from dual;",
			[]string{"BEGIN\n  dbms_output.put_line('x');\nEND;", "select 1 from dual"}},
		{"oracle create procedure", oracle, "CREATE OR REPLACE PROCEDURE p IS\nBEGIN\n  NULL;\nEND;\n/\nCREATE TABLE t (a int);",
			[]string{"CREATE OR REPLACE PROCEDURE p IS\nBEGIN\n  NULL;\nEND;", "CREATE TABLE t (a int)"}},
		{"oracle q-quote", oracle, "select q'[a;']' from dual; select 2 from dual", []string{"select q'[a;']' from dual", "select 2 from dual"}},
		{"oracle slash after sql", oracle, "select 1 from dual\n/\nselect 2 from dual", []string{"select 1 from dual", "select 2 from dual"}},
		{"blank lines", Options{BlankLines: true}, "select 1\n\nselect 2\n  \nselect 3", []string{"select 1", "select 2", "select 3"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := texts(Split(tc.script, tc.o))
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") || len(got) != len(tc.want) {
				t.Fatalf("Split(%q)\n got %q\nwant %q", tc.script, got, tc.want)
			}
		})
	}
}

func TestOffsetsAndLines(t *testing.T) {
	script := "select 1;\n\n  -- c\n  select\n 2;"
	stmts := Split(script, Options{})
	if len(stmts) != 2 {
		t.Fatalf("got %d statements", len(stmts))
	}
	for _, st := range stmts {
		if script[st.Start:st.End] != st.Text {
			t.Errorf("offsets of %q do not match", st.Text)
		}
	}
	if stmts[0].Line != 1 || stmts[1].Line != 4 {
		t.Errorf("lines = %d, %d", stmts[0].Line, stmts[1].Line)
	}
}

func TestAt(t *testing.T) {
	script := "select 1; select 2;\n\nselect 3"
	stmts := Split(script, Options{})
	cases := map[int]string{
		0:               "select 1",
		8:               "select 1",
		9:               "select 1", // right after the semicolon, same line
		12:              "select 2",
		len(script):     "select 3",
		len(script) - 8: "select 3",
		20:              "select 2", // empty line after the statement
	}
	for cur, want := range cases {
		got, ok := At(script, stmts, cur)
		if !ok || got.Text != want {
			t.Errorf("At(%d) = %q, %v; want %q", cur, got.Text, ok, want)
		}
	}
	if _, ok := At("", nil, 0); ok {
		t.Error("At on an empty script found a statement")
	}
}

func TestWithin(t *testing.T) {
	script := "select 0;\nselect 1; select 2;"
	stmts := Within(script, 10, len(script), Options{})
	if len(stmts) != 2 || stmts[0].Text != "select 1" || stmts[0].Line != 2 || script[stmts[1].Start:stmts[1].End] != "select 2" {
		t.Fatalf("Within = %+v", stmts)
	}
}

func FuzzSplit(f *testing.F) {
	seeds := []string{
		"select 1; select 2",
		"create function f() as $$ begin; end $$; select $1",
		"DELIMITER //\nselect 1//\nDELIMITER ;\nselect 2;",
		"BEGIN\n null;\nEND;\n/\nselect q'{x}' from dual;",
		"select [a]; GO\nselect `b`; # c\n/* d */ -- e",
		"CREATE TRIGGER t BEGIN SELECT CASE WHEN 1 THEN 2 END; END; x",
		"select E'\\''; select 'a''b'",
	}
	for _, s := range seeds {
		f.Add(s, uint8(0))
		f.Add(s, uint8(255))
	}
	f.Fuzz(func(t *testing.T, script string, flags uint8) {
		o := Options{
			HashComments:     flags&1 != 0,
			Backticks:        flags&2 != 0,
			Brackets:         flags&2 != 0,
			DollarQuotes:     flags&4 != 0,
			NestedComments:   flags&4 != 0,
			EStrings:         flags&4 != 0,
			BackslashEscapes: flags&8 != 0,
			QQuotes:          flags&16 != 0,
			SlashTerminator:  flags&16 != 0,
			GoBatches:        flags&32 != 0,
			Delimiter:        flags&64 != 0,
			BlockStatements:  flags&128 != 0,
			BlankLines:       flags&129 == 129,
		}
		stmts := Split(script, o)
		prev := 0
		for _, st := range stmts {
			if st.Start < prev || st.End <= st.Start || st.End > len(script) {
				t.Fatalf("bad offsets %d..%d after %d in %q", st.Start, st.End, prev, script)
			}
			if script[st.Start:st.End] != st.Text || strings.Trim(st.Text, " \t\r\n\f\v") != st.Text {
				t.Fatalf("text %q does not match its offsets", st.Text)
			}
			if st.Line != strings.Count(script[:st.Start], "\n")+1 {
				t.Fatalf("wrong line for %q", st.Text)
			}
			prev = st.End
		}
		// Splitting a statement again never yields more text than it had.
		for _, st := range stmts {
			for _, sub := range Split(st.Text, o) {
				if len(sub.Text) > len(st.Text) {
					t.Fatalf("re-split grew %q", st.Text)
				}
			}
		}
	})
}

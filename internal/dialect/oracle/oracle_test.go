package oracle

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/profile"
	"github.com/iagapie/quire/internal/sqlsplit"
)

func TestMissingClient(t *testing.T) {
	p := profile.Profile{Driver: "oracle", Host: "127.0.0.1", Port: 1, User: "x", Password: "y", Database: "FREEPDB1",
		Options: map[string]string{"libDir": t.TempDir()}}
	db, err := (&Driver{}).Open(t.Context(), p, "", dialect.Network{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	err = db.PingContext(t.Context())
	if !errors.Is(err, ErrNoClient) || !strings.Contains(err.Error(), "Instant Client folder") {
		t.Fatalf("ping without a client = %v", err)
	}
	if (&Driver{}).Transient(err) {
		t.Error("a missing client is not transient")
	}
}

func TestSyntax(t *testing.T) {
	d := &Driver{}
	if got := connectString("db", 1521, "ORCL", true); !strings.Contains(got, "(SID=ORCL)") {
		t.Errorf("sid = %s", got)
	}
	if got := connectString("db", 1522, "FREEPDB1", false); got != "db:1522/FREEPDB1" {
		t.Errorf("service = %s", got)
	}
	if got := d.LimitRows("SELECT * FROM t;", 10); got != "SELECT * FROM t\nFETCH FIRST 10 ROWS ONLY" {
		t.Errorf("limit rows = %q", got)
	}
	plan := d.Explain("SELECT 1 FROM dual;")
	if plan.Before[0] != "EXPLAIN PLAN FOR SELECT 1 FROM dual" || !strings.Contains(plan.Query, "DBMS_XPLAN") {
		t.Errorf("plan = %+v", plan)
	}
	up := d.Upsert(`"T"`, []string{"ID", "NAME"}, []string{"ID"})
	if !strings.HasPrefix(up, `MERGE INTO "T" quire_t USING (SELECT :1 AS "ID", :2 AS "NAME" FROM dual) quire_s ON (quire_t."ID" = quire_s."ID")`) {
		t.Errorf("upsert = %s", up)
	}
	if d.Fold("abc") != "ABC" || !d.Plain("ABC_1") || d.Plain("abc") {
		t.Error("folding")
	}
	stmts := sqlsplit.Split("BEGIN\n  NULL;\nEND;\n/\nSELECT q'[a;b]' FROM dual;\n", d.Split())
	if len(stmts) != 2 || !strings.HasSuffix(stmts[0].Text, "END;") || stmts[1].Text != "SELECT q'[a;b]' FROM dual" {
		t.Errorf("split = %+v", stmts)
	}
	ix, err := d.CreateIndex("", "APP", dialect.Index{Name: "I1", Table: "T", Method: "bitmap", Columns: []dialect.IndexColumn{{Expression: "UPPER(NAME)"}, {Name: "ID", Desc: true}}}, false, false)
	if err != nil || ix != `CREATE BITMAP INDEX "APP"."I1" ON "APP"."T" (UPPER(NAME), "ID" DESC)` {
		t.Errorf("index = %s, %v", ix, err)
	}
}

func TestBuiltinURL(t *testing.T) {
	base := profile.Profile{User: "app", Password: "p@ss/w", Host: "db", Port: 1522, Database: "FREEPDB1", Options: map[string]string{"client": ClientBuiltin}}
	u, err := builtinURL(base)
	if err != nil || !strings.HasPrefix(u, "oracle://app:p@ss%2Fw@db:1522/FREEPDB1?") || !strings.Contains(u, "PROGRAM=Quire") {
		t.Errorf("url = %s, %v", u, err)
	}
	sid := base
	sid.Options = map[string]string{"client": ClientBuiltin, "sid": "true", "role": "sysdba"}
	if u, _ := builtinURL(sid); !strings.Contains(u, "SID=FREEPDB1") || !strings.Contains(u, "DBA PRIVILEGE=SYSDBA") || !strings.Contains(u, "@db:1522/?") {
		t.Errorf("sid url = %s", u)
	}
	for uri, want := range map[string]string{ //nolint:gosec // G101: test connection strings
		"db.example:1530/ORCLPDB": "@db.example:1530/ORCLPDB?",
		"//db/ORCL":               "@db:1521/ORCL?",
		"oracle://u:p@h:1/s":      "oracle://u:p@h:1/s",
		"(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=h)(PORT=1521))(CONNECT_DATA=(SERVICE_NAME=s)))": "connStr=%28DESCRIPTION",
	} {
		p := base
		p.URI = uri
		if u, err := builtinURL(p); err != nil || !strings.Contains(u, want) {
			t.Errorf("%s: url = %s, %v", uri, u, err)
		}
	}
	alias := base
	alias.URI = "PRODDB"
	if _, err := builtinURL(alias); err == nil || !strings.Contains(err.Error(), "TNS alias") {
		t.Errorf("a TNS alias = %v", err)
	}
	wallet := base
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cwallet.sso"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	wallet.Options = map[string]string{"client": ClientBuiltin, "configDir": dir}
	if u, _ := builtinURL(wallet); !strings.Contains(u, "SSL=true") || !strings.Contains(u, "WALLET=") {
		t.Errorf("wallet url = %s", u)
	}
}

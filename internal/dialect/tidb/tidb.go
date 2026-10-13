// Package tidb is the TiDB driver: the MySQL driver with TiDB's catalog (no routines
// or triggers, sequences), version and errors.
package tidb

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/go-sql-driver/mysql"

	"github.com/iagapie/quire/internal/dialect"
	mysqldialect "github.com/iagapie/quire/internal/dialect/mysql"
)

func init() {
	dialect.Register(&Driver{mysqldialect.New(info(), mysqldialect.ClientReadOnly())})
}

// Driver is the TiDB driver.
type Driver struct {
	*mysqldialect.Driver
}

func info() dialect.Info {
	i := mysqldialect.Info()
	i.ID, i.Label = "tidb", "TiDB"
	i.Description = "TiDB 6.5 and later, self-hosted or TiDB Cloud"
	i.DefaultPort = 4000
	i.Folders = []string{dialect.KindTable, dialect.KindView, dialect.KindSequence}
	// TiDB has no hash, full-text (outside TiDB Cloud) or spatial indexes, and keeps
	// descending keys only in the syntax.
	i.Index = dialect.IndexFeatures{Methods: []string{"btree"}, Expressions: true, Length: true, Invisible: true, IfNotExists: true, Editable: true}
	i.Note = "TiDB has no read-only sessions: a read-only connection is enforced by Quire, which runs only queries."
	i.Order = 32
	return i
}

var versionRe = regexp.MustCompile(`-TiDB-(v\S+)`)

// Version returns the TiDB release (VERSION() starts with the MySQL version it mimics).
func (d *Driver) Version(ctx context.Context, q dialect.Querier) (string, error) {
	v, err := d.Driver.Version(ctx, q)
	if m := versionRe.FindStringSubmatch(v); m != nil {
		return m[1], err
	}
	return v, err
}

// Transient adds TiDB's retryable errors: write conflicts, region and TiKV
// unavailability, and lock timeouts of optimistic transactions.
func (d *Driver) Transient(err error) bool {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 8002, // write conflict, retry the transaction
			8022, // transaction commit failed (retryable)
			8027, // schema version changed
			9001, // PD server timeout
			9002, // TiKV server timeout
			9003, // TiKV server busy
			9004, // resolve lock timeout
			9005, // region unavailable
			9006, // GC life time shorter than transaction duration
			9007: // write conflict
			return true
		}
	}
	return d.Driver.Transient(err)
}

// Objects lists tables, views and sequences: TiDB has no stored routines or triggers.
func (d *Driver) Objects(ctx context.Context, q dialect.Querier, database, _ string) ([]dialect.Object, error) {
	rows, err := q.QueryContext(ctx, `SELECT TABLE_NAME,
       CASE WHEN TABLE_TYPE LIKE '%VIEW' THEN 'view' WHEN TABLE_TYPE = 'SEQUENCE' THEN 'sequence' ELSE 'table' END,
       CASE WHEN TABLE_TYPE LIKE '%VIEW' THEN '' ELSE TABLE_COMMENT END
FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`, database)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []dialect.Object
	for rows.Next() {
		var o dialect.Object
		if err := rows.Scan(&o.Name, &o.Kind, &o.Comment); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DDL returns SHOW CREATE for tables, views and sequences.
func (d *Driver) DDL(ctx context.Context, q dialect.Querier, database, schema string, obj dialect.Object) (string, error) {
	if obj.Kind != dialect.KindSequence {
		return d.Driver.DDL(ctx, q, database, schema, obj)
	}
	var name, def string
	if err := q.QueryRowContext(ctx, "SHOW CREATE SEQUENCE "+d.Qualify(database, "", obj.Name)).Scan(&name, &def); err != nil {
		return "", fmt.Errorf("sequence %s: %w", obj.Name, err)
	}
	return def + ";\n", nil
}

// Package yugabyte is the YugabyteDB (YSQL) driver: the PostgreSQL driver (pgx v5)
// with YugabyteDB's defaults, index methods and version.
package yugabyte

import (
	"context"
	"regexp"

	"github.com/iagapie/quire/internal/dialect"
	"github.com/iagapie/quire/internal/dialect/postgres"
)

func init() {
	dialect.Register(&Driver{postgres.New(info())})
}

// Driver is the YugabyteDB driver.
type Driver struct {
	*postgres.Driver
}

func info() dialect.Info {
	i := postgres.Info()
	i.ID, i.Label = "yugabytedb", "YugabyteDB"
	i.Description = "YugabyteDB YSQL 2.20 and later, self-hosted or YugabyteDB Aeon"
	i.DefaultPort, i.DefaultUser, i.DefaultDatabase = 5433, "yugabyte", "yugabyte"
	// DDL inside a transaction block is only atomic with transactional DDL enabled on
	// the cluster, so the index editor does not count on it.
	i.TransactionalDDL = false
	i.Index = dialect.IndexFeatures{
		Methods: []string{"lsm", "gin"},
		Desc:    true, Nulls: true, Partial: true, Include: true, Concurrently: true, Tablespace: true,
		Collation: true, Expressions: true, IfNotExists: true, Editable: true,
	}
	i.Order = 12
	return i
}

var versionRe = regexp.MustCompile(`^(\S+?)-YB-(\S+)`)

// Version returns the YugabyteDB release and the PostgreSQL version it is built on.
func (d *Driver) Version(ctx context.Context, q dialect.Querier) (string, error) {
	v, err := d.Driver.Version(ctx, q)
	if m := versionRe.FindStringSubmatch(v); m != nil {
		return m[2] + " (PostgreSQL " + m[1] + ")", err
	}
	return v, err
}

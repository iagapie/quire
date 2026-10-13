// Package all registers every database driver. A new driver is added here with one
// blank import.
package all

import (
	_ "github.com/iagapie/quire/internal/dialect/clickhouse" // ClickHouse
	_ "github.com/iagapie/quire/internal/dialect/cockroach"  // CockroachDB
	_ "github.com/iagapie/quire/internal/dialect/cosmos"     // Cosmos DB
	_ "github.com/iagapie/quire/internal/dialect/mongodb"    // MongoDB, DocumentDB
	_ "github.com/iagapie/quire/internal/dialect/mssql"      // SQL Server
	_ "github.com/iagapie/quire/internal/dialect/mysql"      // MySQL, MariaDB
	_ "github.com/iagapie/quire/internal/dialect/oracle"     // Oracle
	_ "github.com/iagapie/quire/internal/dialect/postgres"   // PostgreSQL
	_ "github.com/iagapie/quire/internal/dialect/redshift"   // Amazon Redshift
	_ "github.com/iagapie/quire/internal/dialect/sqlite"     // SQLite
	_ "github.com/iagapie/quire/internal/dialect/tidb"       // TiDB
	_ "github.com/iagapie/quire/internal/dialect/yugabyte"   // YugabyteDB
)

//go:build duckdb

package all

// DuckDB links the DuckDB library into the app (about 50 MB), so it is built only with
// -tags duckdb (task build DUCKDB=1).
import _ "github.com/iagapie/quire/internal/dialect/duckdb" // DuckDB

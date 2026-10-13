//go:build !(windows && arm64)

// duckdb-go ships no library for windows/arm64; elsewhere the tests run on DuckDB too.

package query

import _ "github.com/iagapie/quire/internal/dialect/duckdb" // built into the app only with -tags duckdb

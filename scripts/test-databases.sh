#!/bin/sh
# Runs the Go tests against disposable database containers. The containers have their
# own names and ports, so existing containers are never touched, and they are removed
# at the end (also when the tests fail). A container that is already running (from a
# previous run that was interrupted) is reused.
set -eu

PG_NAME=quire-test-pg
PG_PORT=55432
MYSQL_NAME=quire-test-mysql
MYSQL_PORT=53306
MARIA_NAME=quire-test-mariadb
MARIA_PORT=53307
MSSQL_NAME=quire-test-mssql
MSSQL_PORT=51433
# SQL Server wants a strong password; it only protects a throwaway container.
MSSQL_PASSWORD=Quire_test_1
COSMOS_NAME=quire-test-cosmos
COSMOS_PORT=58081
CRDB_NAME=quire-test-crdb
CRDB_PORT=56257
YB_NAME=quire-test-yb
YB_PORT=55433
TIDB_NAME=quire-test-tidb
TIDB_PORT=54000
TIDB_STATUS_PORT=54080
ORA_NAME=quire-test-oracle
ORA_PORT=51521
CH_NAME=quire-test-ch
CH_PORT=59000
CH_HTTP_PORT=58123
MONGO_NAME=quire-test-mongo
MONGO_PORT=57017

started=""
cleanup() {
	for c in $started; do
		docker rm -f "$c" > /dev/null 2>&1 || true
	done
}
trap cleanup EXIT INT TERM

start() { # name, run arguments...
	name=$1
	shift
	if [ "$(docker inspect -f '{{.State.Running}}' "$name" 2> /dev/null || true)" = "true" ]; then
		return
	fi
	docker rm -f "$name" > /dev/null 2>&1 || true
	docker run -d --name "$name" "$@" > /dev/null
	started="$started $name"
}

wait_for() { # description, command...
	what=$1
	shift
	i=0
	until "$@" > /dev/null 2>&1; do
		i=$((i + 1))
		if [ "$i" -gt 120 ]; then
			echo "$what did not start" >&2
			exit 1
		fi
		sleep 1
	done
}

start "$PG_NAME" -p "127.0.0.1:$PG_PORT:5432" -e POSTGRES_USER=quire -e POSTGRES_PASSWORD=quirepw -e POSTGRES_DB=shop postgres:17-alpine
start "$MYSQL_NAME" -p "127.0.0.1:$MYSQL_PORT:3306" -e MYSQL_ROOT_PASSWORD=quirepw -e MYSQL_DATABASE=shop \
	-e MYSQL_USER=quire -e MYSQL_PASSWORD=quirepw mysql:8.4 --log-bin-trust-function-creators=1
start "$MARIA_NAME" -p "127.0.0.1:$MARIA_PORT:3306" -e MARIADB_ROOT_PASSWORD=quirepw -e MARIADB_DATABASE=shop \
	-e MARIADB_USER=quire -e MARIADB_PASSWORD=quirepw mariadb:11.4
# The SQL Server image is built for x86-64 only; Docker runs it emulated on Apple silicon.
start "$MSSQL_NAME" --platform linux/amd64 -p "127.0.0.1:$MSSQL_PORT:1433" -e ACCEPT_EULA=Y -e MSSQL_PID=Developer \
	-e MSSQL_SA_PASSWORD="$MSSQL_PASSWORD" mcr.microsoft.com/mssql/server:2022-latest
# The Cosmos DB emulator (vNext) runs natively on x86-64 and Apple silicon, over HTTP.
start "$COSMOS_NAME" -p "127.0.0.1:$COSMOS_PORT:8081" mcr.microsoft.com/cosmosdb/linux/azure-cosmos-emulator:vnext-preview --enable-explorer false
start "$CRDB_NAME" -p "127.0.0.1:$CRDB_PORT:26257" cockroachdb/cockroach:v26.3.2 start-single-node --insecure
start "$YB_NAME" -p "127.0.0.1:$YB_PORT:5433" yugabytedb/yugabyte:2026.1.2.0-b137 bin/yugabyted start --background=false
start "$TIDB_NAME" -p "127.0.0.1:$TIDB_PORT:4000" -p "127.0.0.1:$TIDB_STATUS_PORT:10080" pingcap/tidb:v8.5.4
# Oracle Free (23ai); the tests use the built-in client, so no Instant Client is needed.
start "$ORA_NAME" -p "127.0.0.1:$ORA_PORT:1521" -e ORACLE_PASSWORD="$MSSQL_PASSWORD" -e APP_USER=quire -e APP_USER_PASSWORD=quirepw \
	gvenzl/oracle-free:23-slim-faststart
start "$CH_NAME" -p "127.0.0.1:$CH_PORT:9000" -p "127.0.0.1:$CH_HTTP_PORT:8123" -e CLICKHOUSE_USER=quire -e CLICKHOUSE_PASSWORD=quirepw \
	-e CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1 clickhouse/clickhouse-server:26.9.12.8
start "$MONGO_NAME" -p "127.0.0.1:$MONGO_PORT:27017" -e MONGO_INITDB_ROOT_USERNAME=root -e MONGO_INITDB_ROOT_PASSWORD=rootpw mongo:8.3-noble

wait_for PostgreSQL docker exec "$PG_NAME" pg_isready -U quire -d shop
wait_for MySQL docker exec "$MYSQL_NAME" mysql -uquire -pquirepw -h127.0.0.1 -e "SELECT 1" shop
wait_for MariaDB docker exec "$MARIA_NAME" mariadb -uquire -pquirepw -h127.0.0.1 -e "SELECT 1" shop
wait_for "Cosmos DB emulator" curl -sf -o /dev/null "http://127.0.0.1:$COSMOS_PORT/"
wait_for CockroachDB docker exec "$CRDB_NAME" cockroach sql --insecure -e "SELECT 1"
wait_for YugabyteDB docker exec "$YB_NAME" sh -c 'bin/ysqlsh -h "$(hostname -i | cut -d" " -f1)" -c "SELECT 1"'
wait_for TiDB curl -sf -o /dev/null "http://127.0.0.1:$TIDB_STATUS_PORT/status"
wait_for ClickHouse docker exec "$CH_NAME" clickhouse-client --user quire --password quirepw -q "SELECT 1"
wait_for MongoDB docker exec "$MONGO_NAME" mongosh -u root -p rootpw --quiet --eval "db.runCommand({ping: 1})"
wait_for Oracle sh -c "docker logs $ORA_NAME 2>&1 | grep -q 'DATABASE IS READY'"
wait_for "SQL Server" docker exec "$MSSQL_NAME" /opt/mssql-tools18/bin/sqlcmd -C -S localhost -U sa -P "$MSSQL_PASSWORD" -Q "SELECT 1"

QUIRE_TEST_POSTGRES="127.0.0.1:$PG_PORT" \
QUIRE_TEST_POSTGRES_CONTAINER="$PG_NAME" \
QUIRE_TEST_MYSQL="127.0.0.1:$MYSQL_PORT" \
QUIRE_TEST_MYSQL_CONTAINER="$MYSQL_NAME" \
QUIRE_TEST_MARIADB="127.0.0.1:$MARIA_PORT" \
QUIRE_TEST_SQLSERVER="127.0.0.1:$MSSQL_PORT" \
QUIRE_TEST_SQLSERVER_PASSWORD="$MSSQL_PASSWORD" \
QUIRE_TEST_SQLSERVER_CONTAINER="$MSSQL_NAME" \
QUIRE_TEST_COSMOS="http://127.0.0.1:$COSMOS_PORT" \
QUIRE_TEST_COSMOS_CONTAINER="$COSMOS_NAME" \
QUIRE_TEST_COCKROACH="127.0.0.1:$CRDB_PORT" \
QUIRE_TEST_COCKROACH_CONTAINER="$CRDB_NAME" \
QUIRE_TEST_YUGABYTE="127.0.0.1:$YB_PORT" \
QUIRE_TEST_YUGABYTE_CONTAINER="$YB_NAME" \
QUIRE_TEST_TIDB="127.0.0.1:$TIDB_PORT" \
QUIRE_TEST_TIDB_CONTAINER="$TIDB_NAME" \
QUIRE_TEST_ORACLE="127.0.0.1:$ORA_PORT" \
QUIRE_TEST_ORACLE_CONTAINER="$ORA_NAME" \
QUIRE_TEST_CLICKHOUSE="127.0.0.1:$CH_PORT" \
QUIRE_TEST_CLICKHOUSE_HTTP="127.0.0.1:$CH_HTTP_PORT" \
QUIRE_TEST_CLICKHOUSE_CONTAINER="$CH_NAME" \
QUIRE_TEST_MONGODB="127.0.0.1:$MONGO_PORT" \
	go test -race -count=1 "$@" ./...

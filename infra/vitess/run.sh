#!/bin/bash
# Entrypoint: runs vttestserver with the schemas mounted at /vt/schema
# (one directory per keyspace: *.sql and vschema.json).
set -euo pipefail

KEYSPACES="${KEYSPACES:?comma-separated keyspace names}"
NUM_SHARDS="${NUM_SHARDS:?comma-separated shard counts, one per keyspace}"

echo "max_connections = ${MYSQL_MAX_CONNECTIONS:-1000}" >> /vt/config/mycnf/test-suite.cnf
# The test server's default pool is 32 MB; a day of load runs exceeds it and
# the stack slows as it grows. Keep the working set in memory.
echo "innodb_buffer_pool_size = ${INNODB_BUFFER_POOL_SIZE:-512M}" >> /vt/config/mycnf/test-suite.cnf

# Stale socket files from a previous run keep mysqld from starting.
rm -f "$VTDATAROOT"/*/mysql.sock "$VTDATAROOT"/*/mysql.sock.lock

exec /vt/bin/vttestserver \
  --port "${PORT:-15000}" \
  --keyspaces "$KEYSPACES" \
  --num_shards "$NUM_SHARDS" \
  --mysql_bind_host "${MYSQL_BIND_HOST:-0.0.0.0}" \
  --vtcombo-bind-host "${VTCOMBO_BIND_HOST:-0.0.0.0}" \
  --mysql-server-version "${MYSQL_SERVER_VERSION:-$1}" \
  --charset "${CHARSET:-utf8mb4}" \
  --foreign_key_mode "${FOREIGN_KEY_MODE:-disallow}" \
  --enable_online_ddl=false \
  --enable_direct_ddl=true \
  --planner-version=gen4 \
  --tablet_refresh_interval "${TABLET_REFRESH_INTERVAL:-10s}" \
  --schema_dir=/vt/schema

#!/bin/bash
# Run once by the mysql image's entrypoint on an empty data directory, while
# its temporary server listens on the socket only: one database per
# keyspace, with the schema Vitess loads into each shard.
set -euo pipefail
for keyspace in conversation user; do
  mysql --protocol=socket -u root -e "CREATE DATABASE \`$keyspace\` DEFAULT CHARACTER SET utf8mb4"
  mysql --protocol=socket -u root "$keyspace" </schema/"$keyspace"/schema.sql
done

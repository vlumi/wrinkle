#!/bin/bash
# Creates the HBase tables in schema/hbase. Idempotent.
set -euo pipefail
cd "$(dirname "$0")"
docker compose exec -T hbase hbase shell -n <../schema/hbase/schema.rb >/dev/null

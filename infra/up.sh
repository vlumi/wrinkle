#!/bin/bash
# Starts the stack with its database profile (see profile.sh), then creates
# the outbox topics, the blob bucket and the HBase tables, and registers the
# outbox connectors, resetting their positions if Vitess was recreated.
set -euo pipefail
cd "$(dirname "$0")"
profile=$(./profile.sh)
echo "database: $profile"
docker compose --profile "$profile" up -d --build --wait
./kafka-topics.sh
./garage-init.sh
./hbase-init.sh
debezium/register.sh "$profile"
if [ "$profile" = vitess ]; then debezium/vitess/stale-offsets.sh; fi

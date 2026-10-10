#!/bin/bash
# Moves the fan-out consumer group to the end of the outbox topic, so a
# backend started next does not inherit what an earlier run left behind.
# The group must be inactive: stop any backend first.
set -euo pipefail
cd "$(dirname "$0")"
docker compose exec -T kafka \
  /opt/kafka/bin/kafka-consumer-groups.sh --bootstrap-server localhost:9092 \
  --group wrinkle-fanout --topic wrinkle.conversation.outbox \
  --reset-offsets --to-latest --execute >/dev/null

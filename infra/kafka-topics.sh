#!/bin/bash
# Creates the outbox topics before anything streams to them, so consumers
# and offset resets find both from the start. Idempotent.
set -euo pipefail
cd "$(dirname "$0")"
for topic in wrinkle.conversation.outbox wrinkle.user.user_outbox; do
  docker compose exec -T kafka \
    /opt/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 \
    --create --if-not-exists --topic "$topic" >/dev/null
done

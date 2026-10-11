#!/bin/bash
# Smoke test of the change-data path: an outbox row written through vtgate
# shows up on the Kafka topic. Expects the stack to be up (make infra-up).
set -euo pipefail

cd "$(dirname "$0")"
compose() { docker compose "$@"; }
TOPIC=wrinkle.conversation.outbox
CONNECT_URL="${CONNECT_URL:-http://localhost:8083}"

echo "waiting for the outbox connector to run"
for _ in $(seq 1 60); do
  state=$(curl -sf "$CONNECT_URL/connectors/wrinkle-outbox/status" 2>/dev/null |
    sed -n 's/.*"tasks":\[{"id":0,"state":"\([A-Z]*\)".*/\1/p')
  if [ "$state" = "RUNNING" ]; then break; fi
  sleep 2
done
if [ "${state:-}" != "RUNNING" ]; then
  echo "connector task not running: $(curl -s "$CONNECT_URL/connectors/wrinkle-outbox/status")" >&2
  exit 1
fi

# Subscribe before inserting, so only new records matter however large the
# topic has grown. The connector may still be catching up, so a few tries.
for attempt in 1 2 3; do
  captured=$(mktemp -t wrinkle-smoke.XXXXXX)
  compose exec -T kafka /opt/kafka/bin/kafka-console-consumer.sh \
    --bootstrap-server localhost:9092 --topic "$TOPIC" --timeout-ms 30000 \
    >"$captured" 2>/dev/null &
  consumer=$!
  sleep 3
  id=$(date +%s%N)
  echo "inserting outbox row $id (attempt $attempt)"
  # As the services write it: the row is deleted in the transaction that
  # inserts it, and the connector passes on the insert only.
  compose exec -T vitess mysql -h 127.0.0.1 -P 15003 conversation -e "
    BEGIN;
    INSERT INTO outbox (conversation_id, outbox_id, recipients, conversation_kind, member_count, kind, payload, created_at)
    VALUES (X'0000000000000000000000000000002a', $id,
      X'0000000000000000000000000000000100000000000000000000000000000002', 'direct', 2, 'smoke', X'00', NOW(6));
    DELETE FROM outbox WHERE conversation_id = X'0000000000000000000000000000002a' AND outbox_id = $id;
    COMMIT"
  for _ in $(seq 1 20); do
    if grep -q "\"outbox_id\":\"$id\"" "$captured"; then
      kill "$consumer" 2>/dev/null || true
      echo "smoke test passed: outbox row $id reached $TOPIC"
      exit 0
    fi
    sleep 1
  done
  kill "$consumer" 2>/dev/null || true
done
echo "smoke test failed: no outbox row reached $TOPIC" >&2
exit 1

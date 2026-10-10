#!/bin/bash
# Smoke test of the change-data path: an outbox row inserted through vtgate
# shows up on the Kafka topic. Expects the stack to be up (make infra-up).
set -euo pipefail

cd "$(dirname "$0")"
compose() { docker compose "$@"; }
TOPIC=wrinkle.conversation.outbox
CONNECT_URL="${CONNECT_URL:-http://localhost:8083}"

echo "waiting for the outbox connector to run"
for _ in $(seq 1 60); do
  state=$(curl -sf "$CONNECT_URL/connectors/wrinkle-outbox/status" 2>/dev/null \
    | sed -n 's/.*"tasks":\[{"id":0,"state":"\([A-Z]*\)".*/\1/p')
  if [ "$state" = "RUNNING" ]; then break; fi
  sleep 2
done
if [ "${state:-}" != "RUNNING" ]; then
  echo "connector task not running: $(curl -s "$CONNECT_URL/connectors/wrinkle-outbox/status")" >&2
  exit 1
fi

# The connector streams from the position it reached when it started, so give
# it a few tries in case it is still catching up.
for attempt in 1 2 3; do
  id=$(date +%s%N)
  echo "inserting outbox row $id (attempt $attempt)"
  compose exec -T vitess mysql -h 127.0.0.1 -P 15003 conversation -e "
    INSERT INTO outbox (conversation_id, outbox_id, user_a, user_b, kind, payload, created_at)
    VALUES (42, $id, 1, 2, 'smoke', X'00', NOW(6))"
  if compose exec -T kafka /opt/kafka/bin/kafka-console-consumer.sh \
      --bootstrap-server localhost:9092 --topic "$TOPIC" \
      --from-beginning --timeout-ms 20000 2>/dev/null \
      | grep -q "\"outbox_id\":\"$id\""; then
    echo "smoke test passed: outbox row $id reached $TOPIC"
    exit 0
  fi
done
echo "smoke test failed: no outbox row reached $TOPIC" >&2
exit 1

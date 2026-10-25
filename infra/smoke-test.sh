#!/bin/bash
# Smoke test of the change-data path: an outbox row written to the database,
# through vtgate or to MySQL, shows up on the Kafka topic, for both outboxes;
# and a row round-trips through HBase's message table. Expects the stack to
# be up (make infra-up). The connectors skip the initial snapshot, so rows written
# before one streams are never delivered; passing here also means both
# stream.
set -euo pipefail

cd "$(dirname "$0")"
compose() { docker compose "$@"; }
CONNECT_URL="${CONNECT_URL:-http://localhost:8083}"
db=$(./profile.sh)

wait_running() {
  local connector=$1 state=
  echo "waiting for the $connector connector to run"
  for _ in $(seq 1 60); do
    state=$(curl -sf "$CONNECT_URL/connectors/$connector/status" 2>/dev/null |
      sed -n 's/.*"tasks":\[{"id":0,"state":"\([A-Z]*\)".*/\1/p')
    if [ "$state" = "RUNNING" ]; then return; fi
    sleep 2
  done
  echo "connector task not running: $(curl -s "$CONNECT_URL/connectors/$connector/status")" >&2
  exit 1
}

# As the services write them: the row is deleted in the transaction that
# inserts it, and the connector passes on the insert only.
conversation_row() {
  echo "BEGIN;
    INSERT INTO outbox (conversation_id, outbox_id, recipients, conversation_kind, member_count, kind, payload, created_at)
    VALUES (X'0000000000000000000000000000002a', $1,
      X'0000000000000000000000000000000100000000000000000000000000000002', 'direct', 2, 'smoke', X'00', NOW(6));
    DELETE FROM outbox WHERE conversation_id = X'0000000000000000000000000000002a' AND outbox_id = $1;
    COMMIT"
}

user_row() {
  echo "BEGIN;
    INSERT INTO user_outbox (user_id, outbox_id, kind, payload, created_at)
    VALUES (X'0000000000000000000000000000002a', $1, 'smoke', X'00', NOW(6));
    DELETE FROM user_outbox WHERE user_id = X'0000000000000000000000000000002a' AND outbox_id = $1;
    COMMIT"
}

# check <connector> <topic> <keyspace> <row function>
check() {
  local connector=$1 topic=$2 keyspace=$3 row=$4 captured consumer id
  wait_running "$connector"
  # Subscribe before inserting, so only new records matter however large the
  # topic has grown. The connector may still be catching up, so a few tries.
  for attempt in 1 2 3; do
    captured=$(mktemp -t wrinkle-smoke.XXXXXX)
    compose exec -T kafka /opt/kafka/bin/kafka-console-consumer.sh \
      --bootstrap-server localhost:9092 --topic "$topic" --timeout-ms 30000 \
      >"$captured" 2>/dev/null &
    consumer=$!
    sleep 3
    id=$(date +%s%N)
    echo "inserting $keyspace outbox row $id (attempt $attempt)"
    compose exec -T "$db" mysql -h 127.0.0.1 -P 15003 -u root "$keyspace" -e "$("$row" "$id")"
    for _ in $(seq 1 20); do
      # The Vitess connector sends unsigned bigints as strings, MySQL's as numbers.
      if grep -q -E "\"outbox_id\":\"?$id\"?[,}]" "$captured"; then
        kill "$consumer" 2>/dev/null || true
        wait "$consumer" 2>/dev/null || true
        echo "outbox row $id reached $topic"
        return
      fi
      sleep 1
    done
    kill "$consumer" 2>/dev/null || true
    wait "$consumer" 2>/dev/null || true
  done
  echo "smoke test failed: no outbox row reached $topic" >&2
  exit 1
}

check wrinkle-outbox wrinkle.conversation.outbox conversation conversation_row
check wrinkle-user-outbox wrinkle.user.user_outbox user user_row

echo "writing, reading and deleting an HBase row"
row=$(
  compose exec -T hbase hbase shell -n <<'EOF'
put 'message', "\x2asmoke", 'm:body', 'smoke'
get 'message', "\x2asmoke"
deleteall 'message', "\x2asmoke"
EOF
)
if ! grep -q 'm:body.*value=smoke' <<<"$row"; then
  echo "smoke test failed: the HBase row did not read back" >&2
  exit 1
fi
echo "smoke test passed"

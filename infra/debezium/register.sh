#!/bin/bash
# Creates or updates the outbox connectors: the conversation keyspace's
# outbox and the user keyspace's. Idempotent: PUT on the config.
set -euo pipefail

cd "$(dirname "$0")"
CONNECT_URL="${CONNECT_URL:-http://localhost:8083}"
response=$(mktemp)

for _ in $(seq 1 60); do
  if curl -sf "$CONNECT_URL/connectors" >/dev/null; then break; fi
  sleep 2
done

register() {
  local name=$1 config=$2
  if ! curl -sf -X PUT -H 'Content-Type: application/json' \
    --data @"$config" \
    "$CONNECT_URL/connectors/$name/config" -o "$response"; then
    echo "registering $name failed: $(cat "$response")" >&2
    exit 1
  fi
  echo "connector $name registered"
}

register wrinkle-outbox outbox-connector.json
register wrinkle-user-outbox user-outbox-connector.json

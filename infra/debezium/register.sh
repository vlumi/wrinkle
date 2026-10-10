#!/bin/bash
# Creates or updates the outbox connector. Idempotent: PUT on the config.
set -euo pipefail

cd "$(dirname "$0")"
CONNECT_URL="${CONNECT_URL:-http://localhost:8083}"
NAME=wrinkle-outbox
response=$(mktemp)

for _ in $(seq 1 60); do
  if curl -sf "$CONNECT_URL/connectors" >/dev/null; then break; fi
  sleep 2
done

if ! curl -sf -X PUT -H 'Content-Type: application/json' \
    --data @outbox-connector.json \
    "$CONNECT_URL/connectors/$NAME/config" -o "$response"; then
  echo "registering $NAME failed: $(cat "$response")" >&2
  exit 1
fi
echo "connector $NAME registered"

#!/bin/bash
# Resets the outbox connectors' stored positions when they name a server
# that no longer exists. The test server starts an empty database, with a
# new server id, whenever its container restarts; the connectors would then
# retry streaming from the old positions forever ("failed to find a PRIMARY
# tablet for VStream"). Without stored positions they start from the current
# one, as on their first registration.
set -euo pipefail
cd "$(dirname "$0")/../.."
CONNECT_URL="${CONNECT_URL:-http://localhost:8083}"

executed() {
  docker compose --profile vitess exec -T vitess \
    mysql -h 127.0.0.1 -P 15003 -N -e "USE \`$1:$2\`; SELECT @@global.gtid_executed"
}

# Every server id in a stored position, one line per shard and id.
servers() {
  jq -r '.[] | .keyspace as $k | .shard as $s
    | .gtid | sub("^MySQL56/"; "") | split(",")[] | "\($k) \($s) \(split(":")[0])"'
}

stale() {
  local keyspace shard server
  while read -r keyspace shard server; do
    if ! executed "$keyspace" "$shard" | grep -qi "$server"; then return 0; fi
  done
  return 1
}

until_state() {
  local name=$1 pattern=$2
  for _ in $(seq 1 30); do
    if curl -sf "$CONNECT_URL/connectors/$name/status" | grep -q "$pattern"; then return; fi
    sleep 1
  done
  echo "connector $name did not reach $pattern" >&2
  exit 1
}

for name in wrinkle-outbox wrinkle-user-outbox; do
  vgtid=$(curl -sf "$CONNECT_URL/connectors/$name/offsets" | jq -r '.offsets[0].offset.vgtid // empty')
  if [ -z "$vgtid" ] || ! servers <<<"$vgtid" | stale; then continue; fi
  curl -sf -X PUT "$CONNECT_URL/connectors/$name/stop" >/dev/null
  until_state "$name" '"STOPPED"'
  curl -sf -X DELETE "$CONNECT_URL/connectors/$name/offsets" >/dev/null
  curl -sf -X PUT "$CONNECT_URL/connectors/$name/resume" >/dev/null
  until_state "$name" '"tasks":\[{"id":0,"state":"RUNNING"'
  echo "connector $name restarted from the current position: the database was recreated"
done

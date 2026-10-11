#!/bin/bash
# Runs a gateway with a services process beside it, the shape conformance and
# measurement start a split backend in. The services serve their metrics on
# 127.0.0.1:8090 and the RPC on 127.0.0.1:9000; the gateway, started in the
# caller's directory, serves ws://localhost:8080/v1/ws. Stops both on exit.
# Usage: beside-services.sh go|java <gateway command...>
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
case ${1:-} in
  go) services=$repo/bin/wrinkle ;;
  java) services=$repo/backends/java/build/install/wrinkle/bin/wrinkle ;;
  *)
    echo "usage: beside-services.sh go|java <gateway command...>" >&2
    exit 2
    ;;
esac
shift

"$services" --roles services,fanout --listen 127.0.0.1:8090 --services-listen 127.0.0.1:9000 &
services_pid=$!
gateway_pid=""
stop() {
  kill $gateway_pid $services_pid 2>/dev/null || true
  wait $gateway_pid $services_pid 2>/dev/null || true
}
trap stop EXIT INT TERM
for _ in $(seq 1 60); do
  if curl -sf http://localhost:8090/healthz >/dev/null; then break; fi
  sleep 0.5
done
WRINKLE_SERVICES=127.0.0.1:9000 "$@" &
gateway_pid=$!
wait $gateway_pid

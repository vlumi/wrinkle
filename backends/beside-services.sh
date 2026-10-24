#!/bin/bash
# Runs a gateway with a services process beside it, the shape conformance and
# measurement start a split backend in. The services serve their metrics on
# 127.0.0.1:8090 and the RPC on 127.0.0.1:9000; the gateway, started in the
# caller's directory, serves ws://localhost:8080/v1/ws. Stops both on exit.
# Usage: beside-services.sh go|java|ts <gateway command...>
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
case ${1:-} in
  go) services=$repo/bin/wrinkle ;;
  java) services=$repo/backends/java/build/install/wrinkle/bin/wrinkle ;;
  ts) services=$repo/backends/ts/wrinkle ;;
  *)
    echo "usage: beside-services.sh go|java|ts <gateway command...>" >&2
    exit 2
    ;;
esac
shift

# macOS starts shells at 256 open files. Go and the JVM raise their own limit;
# the BEAM and the Rust runtime do not, and would refuse connections past
# about 200.
ulimit -Sn "$(sysctl -n kern.maxfilesperproc 2>/dev/null || ulimit -Hn)" 2>/dev/null || true

# A services process left over from an aborted run would answer instead, and
# its fan-out would take records from the shared consumer group.
"$repo/backends/ports-free.sh" 8090 9000
"$services" --roles services,fanout --listen 127.0.0.1:8090 --services-listen 127.0.0.1:9000 &
services_pid=$!
gateway_pid=""
# The gateway first, so it drains its connections while the services still
# answer.
stop() {
  kill $gateway_pid 2>/dev/null || true
  wait $gateway_pid 2>/dev/null || true
  kill $services_pid 2>/dev/null || true
  wait $services_pid 2>/dev/null || true
}
trap stop EXIT INT TERM
for _ in $(seq 1 60); do
  if curl -sf http://localhost:8090/healthz >/dev/null; then break; fi
  sleep 0.5
done
WRINKLE_SERVICES=127.0.0.1:9000 "$@" &
gateway_pid=$!
wait $gateway_pid

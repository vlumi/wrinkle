#!/bin/bash
# The Java backend as two processes: services and fan-out in one, the gateway
# in another, forwarding over the internal RPC. Usage: split.sh [gateway flags]
# The services process serves its metrics on :8090 and the RPC on :9000; the
# gateway serves ws://localhost:8080/v1/ws. Stops both on exit.
set -euo pipefail
cd "$(dirname "$0")"
bin=build/install/wrinkle/bin/wrinkle

"$bin" --roles services,fanout --listen 127.0.0.1:8090 --services-listen 127.0.0.1:9000 &
services=$!
gateway=""
stop() {
  kill $gateway $services 2>/dev/null || true
  wait $gateway $services 2>/dev/null || true
}
trap stop EXIT INT TERM
for _ in $(seq 1 60); do
  if curl -sf http://localhost:8090/healthz >/dev/null; then break; fi
  sleep 0.5
done
"$bin" --roles gateway --services 127.0.0.1:9000 "$@" &
gateway=$!
wait $gateway

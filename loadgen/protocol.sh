#!/bin/bash
# Runs the measurement protocol in docs/measuring.md against a backend.
# Usage: loadgen/protocol.sh <results.jsonl> <backend command...>
# The backend is restarted before every run. REPS repeats each series.
set -euo pipefail

cd "$(dirname "$0")/.."
if [ $# -lt 2 ]; then
  echo "usage: $0 <results.jsonl> <backend command...>" >&2
  exit 2
fi
out=$1
shift
REPS=${REPS:-1}
LABEL=${LABEL:-$(basename "$1")}
commit=$(git rev-parse --short HEAD)

go build -o bin/loadgen ./loadgen

backend=""
start_backend() {
  "$@" >/dev/null 2>&1 &
  backend=$!
  for _ in $(seq 1 60); do
    if curl -sf http://localhost:8080/healthz >/dev/null; then return; fi
    if ! kill -0 "$backend" 2>/dev/null; then
      echo "backend exited early" >&2
      exit 1
    fi
    sleep 1
  done
}
stop_backend() {
  if [ -n "$backend" ]; then
    kill "$backend" 2>/dev/null || true
    wait "$backend" 2>/dev/null || true
    backend=""
  fi
}
trap stop_backend EXIT

run() {
  # The consumer group's offsets outlive the backend; without a reset, a run
  # would start by draining what the previous one left in Kafka.
  infra/reset-fanout.sh
  start_backend "$@"
  # Let the fan-out worker join its consumer group before measuring.
  sleep 3
  bin/loadgen --out "$out" --label "$LABEL" --commit "$commit" "${LOADGEN_ARGS[@]}"
  stop_backend
  sleep 2
}

for rep in $(seq 1 "$REPS"); do
  echo "== idle connections (rep $rep)"
  for n in 500 1000 2000 4000; do
    LOADGEN_ARGS=(--connections "$n" --rate 0 --warmup 10s --duration 30s --ramp 500)
    run "$@"
  done
  echo "== latency at fixed rates (rep $rep)"
  for rate in 100 500 1000 2000; do
    LOADGEN_ARGS=(--connections 1000 --rate "$rate" --warmup 15s --duration 60s --ramp 500)
    run "$@"
  done
done

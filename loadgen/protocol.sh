#!/bin/bash
# Runs the measurement protocol in docs/measuring.md against a backend.
# Usage: loadgen/protocol.sh <results.jsonl> <backend command...>
# The backend is restarted before every run. REPS repeats each series;
# SERIES=idle or SERIES=rates runs one of them. FRESH=1 recreates the
# infrastructure first: latency degrades as the tables and the topic grow,
# so backends are compared from the same empty stack.
set -euo pipefail

cd "$(dirname "$0")/.."
if [ $# -lt 2 ]; then
  echo "usage: $0 <results.jsonl> <backend command...>" >&2
  exit 2
fi
out=$1
shift
REPS=${REPS:-1}
SERIES=${SERIES:-all}
LABEL=${LABEL:-$(basename "$1")}
commit=$(git rev-parse --short HEAD)
if [ -s "$out" ] && grep -qv "\"commit\":\"$commit\"" "$out"; then
  echo "$out holds runs from another commit; move it aside first" >&2
  exit 1
fi

go build -o bin/loadgen ./loadgen
if [ "${FRESH:-0}" = 1 ]; then
  echo "== fresh infrastructure"
  make --no-print-directory infra-down >/dev/null 2>&1
  make --no-print-directory infra-up >/dev/null
fi

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
  if [ "$SERIES" != rates ]; then
    echo "== idle connections (rep $rep)"
    for n in 500 1000 2000 4000; do
      LOADGEN_ARGS=(--connections "$n" --rate 0 --warmup 10s --duration 30s --ramp 500)
      run "$@"
    done
  fi
  if [ "$SERIES" != idle ]; then
    echo "== latency at fixed rates (rep $rep)"
    for rate in 100 500 1000 1500 2000; do
      LOADGEN_ARGS=(--connections 1000 --rate "$rate" --warmup 15s --duration 60s --ramp 500)
      run "$@"
    done
  fi
done

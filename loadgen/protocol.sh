#!/bin/bash
# Runs the measurement protocol in docs/measuring.md against a backend.
# Usage: loadgen/protocol.sh <results.jsonl> <backend command...>
# The backend is restarted before every run. REPS repeats each series;
# SERIES=idle, SERIES=rates or SERIES=history runs one of them. FRESH=1
# recreates the infrastructure first: latency degrades as the tables and
# the topic grow, so backends are compared from the same empty stack.
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
  # On the database profile the stack has.
  profile=$(infra/profile.sh)
  echo "== fresh infrastructure ($profile)"
  make --no-print-directory infra-down >/dev/null 2>&1
  make --no-print-directory infra-up PROFILE="$profile" >/dev/null
fi

backend=""
BACKEND_ENV=()
start_backend() {
  # Bash 3 calls an empty array unbound.
  env ${BACKEND_ENV[@]+"${BACKEND_ENV[@]}"} "$@" >/dev/null 2>&1 &
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

# run_history picks the mover's metrics once the backend is up: those of
# the services process, which runs it, when split.
run_history() {
  infra/reset-fanout.sh
  start_backend "$@"
  sleep 3
  if curl -sf http://localhost:8090/metrics >/dev/null; then
    mover=http://localhost:8090/metrics
  fi
  bin/loadgen --out "$out" --label "$LABEL" --commit "$commit" --mover-metrics "$mover" "${LOADGEN_ARGS[@]}"
  stop_backend
  sleep 2
}

for rep in $(seq 1 "$REPS"); do
  if [ "$SERIES" = all ] || [ "$SERIES" = idle ]; then
    echo "== idle connections (rep $rep)"
    for n in 500 1000 2000 4000; do
      LOADGEN_ARGS=(--connections "$n" --rate 0 --warmup 10s --duration 30s --ramp 500)
      run "$@"
    done
  fi
  if [ "$SERIES" = all ] || [ "$SERIES" = rates ]; then
    echo "== latency at fixed rates (rep $rep)"
    for rate in 100 500 1000 1500 2000; do
      LOADGEN_ARGS=(--connections 1000 --rate "$rate" --warmup 15s --duration 60s --ramp 500)
      run "$@"
    done
  fi
  if [ "$SERIES" = all ] || [ "$SERIES" = history ]; then
    echo "== history across the watermark (rep $rep)"
    # A hot window of a minute, which the hot messages and the reads stay
    # within, and a mover that looks every second. The services' metrics
    # are on port 8090 when split.
    BACKEND_ENV=(WRINKLE_HISTORY_HOT_S=60 WRINKLE_COLD_MOVE_INTERVAL_S=1)
    mover=http://localhost:8080/metrics
    LOADGEN_ARGS=(--connections 200 --history 200 --hot-window 60s --rate 200 --warmup 5s --duration 30s --ramp 500)
    run_history "$@"
    BACKEND_ENV=()
  fi
done

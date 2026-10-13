#!/bin/bash
# Runs the package tests against a backend started by the given command.
# Usage: test-backend.sh <backend command...>
set -euo pipefail
cd "$(dirname "$0")/.."
if [ $# -eq 0 ]; then
  echo "usage: $0 <backend command...>" >&2
  exit 2
fi

../../infra/reset-fanout.sh
log=$(mktemp -t wrinkle-backend.XXXXXX)
"$@" >"$log" 2>&1 &
backend=$!
disown "$backend"
cleanup() {
  kill "$backend" 2>/dev/null || true
  for _ in $(seq 1 10); do
    if ! kill -0 "$backend" 2>/dev/null; then break; fi
    sleep 1
  done
  kill -9 "$backend" 2>/dev/null || true
}
trap cleanup EXIT

for _ in $(seq 1 60); do
  if curl -sf http://localhost:8080/healthz >/dev/null; then break; fi
  if ! kill -0 "$backend" 2>/dev/null; then
    echo "backend exited early:" >&2
    cat "$log" >&2
    exit 1
  fi
  sleep 1
done

if ! WRINKLE_TEST_REQUIRES_BACKEND=1 Scripts/test.sh; then
  echo "backend log:" >&2
  tail -50 "$log" >&2
  exit 1
fi

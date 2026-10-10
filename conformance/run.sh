#!/bin/bash
# Runs the conformance tests against a backend started by the given command.
# The command must serve ws://localhost:8080/v1/ws against the shared
# infrastructure. Usage: conformance/run.sh <backend command...>
set -euo pipefail

cd "$(dirname "$0")"
if [ $# -eq 0 ]; then
  echo "usage: $0 <backend command...>" >&2
  exit 2
fi

log=$(mktemp -t wrinkle-backend.XXXXXX)
"$@" >"$log" 2>&1 &
backend=$!
cleanup() {
  kill "$backend" 2>/dev/null || true
  for _ in $(seq 1 10); do
    if ! kill -0 "$backend" 2>/dev/null; then break; fi
    sleep 1
  done
  kill -9 "$backend" 2>/dev/null || true
  wait "$backend" 2>/dev/null || true
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

if ! go test -count=1 ./...; then
  echo "backend log:" >&2
  tail -50 "$log" >&2
  exit 1
fi

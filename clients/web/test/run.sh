#!/bin/bash
# Runs the browser test against a backend started by the given command,
# serving the repository root for the page. Usage: run.sh <backend command...>
set -euo pipefail

cd "$(dirname "$0")"
repo=$(cd ../../.. && pwd)
if [ $# -eq 0 ]; then
  echo "usage: $0 <backend command...>" >&2
  exit 2
fi

log=$(mktemp -t wrinkle-backend.XXXXXX)
"$@" >"$log" 2>&1 &
backend=$!
python3 -m http.server 8081 --directory "$repo" --bind 127.0.0.1 >/dev/null 2>&1 &
web=$!
disown "$backend" "$web"
cleanup() {
  for pid in "$backend" "$web"; do
    kill "$pid" 2>/dev/null || true
  done
  for _ in $(seq 1 10); do
    if ! kill -0 "$backend" 2>/dev/null; then break; fi
    sleep 1
  done
  kill -9 "$backend" 2>/dev/null || true
}
trap cleanup EXIT

for _ in $(seq 1 60); do
  if curl -sf http://localhost:8080/healthz >/dev/null && curl -sf http://localhost:8081/clients/web/ >/dev/null; then break; fi
  if ! kill -0 "$backend" 2>/dev/null; then
    echo "backend exited early:" >&2
    cat "$log" >&2
    exit 1
  fi
  sleep 1
done

if ! node browser_test.mjs; then
  echo "backend log:" >&2
  tail -50 "$log" >&2
  exit 1
fi

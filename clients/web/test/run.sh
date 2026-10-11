#!/bin/bash
# Builds the web client and runs the browser test against a backend started
# by the given command, serving the build with server.mjs.
# Usage: run.sh <backend command...>
set -euo pipefail

cd "$(dirname "$0")"
web=$(cd .. && pwd)
(cd "$web" && npm run --silent build >/dev/null)
if [ $# -eq 0 ]; then
  echo "usage: $0 <backend command...>" >&2
  exit 2
fi

log=$(mktemp -t wrinkle-backend.XXXXXX)
"$@" >"$log" 2>&1 &
backend=$!
HOST=127.0.0.1 WEB_TLS=0 node "$web/server.mjs" 8081 >/dev/null 2>&1 &
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
  if curl -sf http://localhost:8080/healthz >/dev/null && curl -sf http://127.0.0.1:8081/healthz >/dev/null; then break; fi
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

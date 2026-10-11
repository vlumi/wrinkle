#!/bin/bash
# One gateway of the side-by-side setup, forwarding to the services instances
# through the services balancer (make lb-up). Each language has its own port,
# so they can run side by side against the same users.
# Usage: gateway.sh go|elixir|rust|java [port]
set -euo pipefail
cd "$(dirname "$0")"
lang=${1:-}
case $lang in
  go) port=${2:-8180} ;;
  elixir) port=${2:-8181} ;;
  rust) port=${2:-8182} ;;
  java) port=${2:-8183} ;;
  *) echo "usage: gateway.sh go|elixir|rust|java [port]" >&2; exit 2 ;;
esac
if ! curl -sf "http://localhost:8404/;csv" | grep -q "^services,BACKEND,.*,UP,"; then
  echo "No services to forward to: start make lb-up and a services instance (make run-go-services) first." >&2
  exit 1
fi
./ports-free.sh "$port"

export WRINKLE_SERVICES=127.0.0.1:9100 WRINKLE_LISTEN=":$port" WRINKLE_NODE="$(hostname -s)-$lang-$port"
echo "$lang gateway on ws://localhost:$port/v1/ws"
case $lang in
  go) exec ../bin/wrinkle --roles gateway ;;
  elixir) cd elixir && exec mix run --no-halt ;;
  rust) exec rust/target/release/wrinkle ;;
  java) exec java/build/install/wrinkle/bin/wrinkle --roles gateway ;;
esac

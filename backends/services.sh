#!/bin/bash
# One services instance of the side-by-side setup, in Go or Java, for the
# gateways to reach through the services balancer (make lb-up). Both languages
# serve one pool and join one fan-out consumer group; Java's sends no pushes
# yet. Instance n (INSTANCE, 1 to 4, default 1) serves the RPC on
# 127.0.0.1:9100+n and its metrics and health on 127.0.0.1:8190+n.
# Usage: services.sh go|java [flags]
set -euo pipefail
cd "$(dirname "$0")/.."
case ${1:-} in
  go) bin=bin/wrinkle ;;
  java) bin=backends/java/build/install/wrinkle/bin/wrinkle ;;
  *)
    echo "usage: services.sh go|java [flags]" >&2
    exit 2
    ;;
esac
shift
n=${INSTANCE:-1}
backends/ports-free.sh $((8190 + n)) $((9100 + n))
exec "$bin" --roles services,fanout --listen "127.0.0.1:$((8190 + n))" --services-listen "127.0.0.1:$((9100 + n))" "$@"

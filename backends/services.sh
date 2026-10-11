#!/bin/bash
# The Go services and fan-out alone, for gateways started separately with
# gateway.sh, so gateways in several languages can run side by side. Serves
# its metrics on :8090 and the RPC on 127.0.0.1:9000.
set -euo pipefail
cd "$(dirname "$0")/.."
exec bin/wrinkle --roles services,fanout --listen 127.0.0.1:8090 --services-listen 127.0.0.1:9000 "$@"

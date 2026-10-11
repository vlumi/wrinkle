#!/bin/bash
# The Go services and fan-out as one instance of the side-by-side setup, for
# the gateways to reach through the services balancer (make lb-up). Instance
# n (INSTANCE, 1 to 4, default 1) serves the RPC on 127.0.0.1:9100+n and its
# metrics and health on 127.0.0.1:8190+n. Every instance joins the same
# fan-out consumer group, so they share the work.
set -euo pipefail
cd "$(dirname "$0")/../.."
n=${INSTANCE:-1}
backends/ports-free.sh $((8190 + n)) $((9100 + n))
exec bin/wrinkle --roles services,fanout --listen "127.0.0.1:$((8190 + n))" --services-listen "127.0.0.1:$((9100 + n))" "$@"

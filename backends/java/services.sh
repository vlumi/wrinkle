#!/bin/bash
# The Java services and fan-out as one instance of the side-by-side setup,
# beside or instead of Go ones: the same contract, the same consumer group.
# Instance n (INSTANCE, 1 to 4, default 1) serves the RPC on 127.0.0.1:9100+n
# and its metrics and health on 127.0.0.1:8190+n. Its fan-out sends no pushes
# yet.
set -euo pipefail
cd "$(dirname "$0")/../.."
n=${INSTANCE:-1}
backends/ports-free.sh $((8190 + n)) $((9100 + n))
exec backends/java/build/install/wrinkle/bin/wrinkle --roles services,fanout --listen "127.0.0.1:$((8190 + n))" --services-listen "127.0.0.1:$((9100 + n))" "$@"

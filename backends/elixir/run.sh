#!/bin/bash
# The Elixir gateway beside a Go services process. The VM ignores SIGINT
# (its break handler would stop it at once); beside-services.sh turns SIGINT
# into SIGTERM, on which the gateway drains.
set -euo pipefail
cd "$(dirname "$0")"
export ELIXIR_ERL_OPTIONS="+Bi ${ELIXIR_ERL_OPTIONS:-}"
exec ../beside-services.sh go mix run --no-halt

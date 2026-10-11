#!/bin/bash
# The Elixir gateway beside a Go services process.
set -euo pipefail
cd "$(dirname "$0")"
exec ../beside-services.sh go mix run --no-halt

#!/bin/bash
# The Go backend split: a Go gateway beside a Go services process.
# Usage: split.sh [gateway flags]
set -euo pipefail
repo=$(cd "$(dirname "$0")/../.." && pwd)
exec "$repo/backends/beside-services.sh" go "$repo/bin/wrinkle" --roles gateway "$@"

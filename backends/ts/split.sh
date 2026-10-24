#!/bin/bash
# The TypeScript backend split: a TypeScript gateway beside a TypeScript
# services process. Usage: split.sh [gateway flags]
set -euo pipefail
repo=$(cd "$(dirname "$0")/../.." && pwd)
exec "$repo/backends/beside-services.sh" ts "$repo/backends/ts/wrinkle" --roles gateway "$@"

#!/bin/bash
# The Java backend split: a Java gateway beside a Java services process.
# Usage: split.sh [gateway flags]
set -euo pipefail
repo=$(cd "$(dirname "$0")/../.." && pwd)
exec "$repo/backends/beside-services.sh" java "$repo/backends/java/build/install/wrinkle/bin/wrinkle" --roles gateway "$@"

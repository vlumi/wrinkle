#!/bin/bash
# The Rust gateway beside a Go services process.
set -euo pipefail
cd "$(dirname "$0")"
exec ../beside-services.sh go target/release/wrinkle

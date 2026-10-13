#!/bin/bash
# Run the package tests headless on the Mac. The backend tests need a backend
# at ws://localhost:8080/v1/ws and skip themselves otherwise.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "Running WrinkleCore tests..."
swift test --package-path Packages/WrinkleCore --quiet -Xswiftc -warnings-as-errors

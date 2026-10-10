#!/usr/bin/env bash
# Regenerate Wrinkle.xcodeproj from project.yml.
set -euo pipefail
cd "$(dirname "$0")/.."

if ! command -v xcodegen >/dev/null; then
    echo "error: xcodegen not found (brew install xcodegen)." >&2
    exit 1
fi
xcodegen generate --quiet
echo "Generated Wrinkle.xcodeproj"

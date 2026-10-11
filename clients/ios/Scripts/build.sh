#!/usr/bin/env bash
# Build the iOS app for the simulator, unsigned. Assumes the project is generated.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "Building Wrinkle-iOS..."
xcodebuild -project Wrinkle.xcodeproj -scheme Wrinkle-iOS \
  -destination "generic/platform=iOS Simulator" \
  -derivedDataPath .build-xcode -quiet \
  CODE_SIGNING_ALLOWED=NO build

#!/bin/bash
# Build, install and launch the app on a connected iPhone, signed with the
# team in the repository's .env. Usage: run-device.sh [device-name-substring]
# The phone needs Developer Mode on (Settings, Privacy & Security) and must
# be paired with this Mac (connect it once by cable and trust the Mac).
set -euo pipefail
cd "$(dirname "$0")/.."

bundle_id="fi.misaki.wrinkle"
derived=".build-xcode"
want="${1:-}"

Scripts/generate.sh
if ! grep -q 'DEVELOPMENT_TEAM = [A-Z0-9]' Wrinkle.xcodeproj/project.pbxproj; then
  echo "error: no team; set WRINKLE_TEAM_ID in the repository's .env" >&2
  exit 1
fi

json=$(mktemp)
xcrun devicectl list devices --json-output "$json" >/dev/null
device=$(
  WANT="$want" python3 - "$json" <<'PY'
import json, os, sys
want = os.environ["WANT"].lower()
for d in json.load(open(sys.argv[1]))["result"]["devices"]:
    props = d.get("deviceProperties", {})
    name = props.get("name", "")
    if d.get("hardwareProperties", {}).get("platform") != "iOS":
        continue
    if want and want not in name.lower():
        continue
    print(d["identifier"])
    break
PY
)
rm -f "$json"
[ -n "$device" ] || {
  echo "error: no paired iPhone${want:+ matching \"$want\"}" >&2
  exit 1
}

echo "Building for the device..."
xcodebuild -project Wrinkle.xcodeproj -scheme Wrinkle-iOS \
  -destination "generic/platform=iOS" -configuration Debug \
  -derivedDataPath "$derived" -allowProvisioningUpdates -quiet build

app="$derived/Build/Products/Debug-iphoneos/Wrinkle.app"
xcrun devicectl device install app --device "$device" "$app"
xcrun devicectl device process launch --device "$device" "$bundle_id"

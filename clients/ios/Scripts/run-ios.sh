#!/bin/bash
# Build, install and launch the app in an iPhone simulator.
#   Usage: run-ios.sh [device-name-substring]
# Reuses a booted iPhone if there is one, else the newest available.
set -euo pipefail
cd "$(dirname "$0")/.."

bundle_id="fi.misaki.wrinkle"
derived=".build-xcode"
device="${1:-}"

udid="$(xcrun simctl list devices --json | DEVICE="$device" python3 -c '
import json, os, re, sys
d = json.load(sys.stdin)["devices"]
want = os.environ.get("DEVICE", "").strip().lower()
booted = None
matches = []
for runtime, devs in d.items():
    m = re.search(r"iOS-(\d+)-(\d+)", runtime)
    ver = (int(m.group(1)), int(m.group(2))) if m else None
    if not ver or ver < (26, 0):
        continue
    for dev in devs:
        name = dev["name"]
        if not dev.get("isAvailable") or "iPhone" not in name:
            continue
        if want and want not in name.lower():
            continue
        if dev["state"] == "Booted":
            booted = dev["udid"]
        matches.append((name.lower() != want, len(name), tuple(-x for x in ver), dev["udid"]))
if not matches:
    print("")
elif want:
    print(sorted(matches)[0][3])
else:
    print(booted or sorted(matches)[0][3])
')"
[ -n "$udid" ] || {
  echo "error: no available iOS 26 iPhone simulator${device:+ matching \"$device\"}" >&2
  exit 1
}

echo "Simulator: $udid"
open -a Simulator
xcrun simctl bootstatus "$udid" -b >/dev/null 2>&1 || true

Scripts/build.sh
app="$derived/Build/Products/Debug-iphonesimulator/Wrinkle.app"
[ -d "$app" ] || {
  echo "error: built app not found at $app" >&2
  exit 1
}
xcrun simctl install "$udid" "$app"
xcrun simctl launch "$udid" "$bundle_id"

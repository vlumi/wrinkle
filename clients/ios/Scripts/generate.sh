#!/usr/bin/env bash
# Regenerate Wrinkle.xcodeproj from project.yml, with the team from the
# repository's .env (WRINKLE_TEAM_ID); without one, only simulator builds work.
set -euo pipefail
cd "$(dirname "$0")/.."

if ! command -v xcodegen >/dev/null; then
  echo "error: xcodegen not found (brew install xcodegen)." >&2
  exit 1
fi
if [ -z "${WRINKLE_TEAM_ID:-}" ] && [ -f ../../.env ]; then
  WRINKLE_TEAM_ID=$(sed -n 's/^WRINKLE_TEAM_ID=//p' ../../.env | tr -d "\"' ")
fi
export WRINKLE_TEAM_ID="${WRINKLE_TEAM_ID:-}"
xcodegen generate --quiet
echo "Generated Wrinkle.xcodeproj${WRINKLE_TEAM_ID:+ for team $WRINKLE_TEAM_ID}"

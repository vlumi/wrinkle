#!/bin/bash
# Prints the stack's database profile: PROFILE if set, else the one the
# stack has, else vitess. The two hold different data, so a PROFILE other
# than the one the stack has fails: make infra-down first.
set -euo pipefail
cd "$(dirname "$0")"
current=$(docker compose ps -a --format '{{.Service}}' 2>/dev/null | grep -x -E 'vitess|mysql' || true)
profile=${PROFILE:-${current:-vitess}}
case $profile in
  vitess | mysql) ;;
  *)
    echo "PROFILE=$profile: expected vitess or mysql" >&2
    exit 2
    ;;
esac
if [ -n "$current" ] && [ "$current" != "$profile" ]; then
  echo "the stack runs the $current profile; make infra-down before PROFILE=$profile" >&2
  exit 1
fi
echo "$profile"

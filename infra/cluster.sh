#!/bin/bash
# The containerized cluster: services instances and gateways in any number and
# language behind one balancer, on the infrastructure's network.
# Usage: cluster.sh up|down|status|logs [service]
# Counts for up, from the environment: GO_SERVICES, JAVA_SERVICES, GO_GW,
# ELIXIR_GW, RUST_GW, JAVA_GW (up to 8 each). Running up again rescales.
set -euo pipefail
cd "$(dirname "$0")/.."
compose=(docker compose -f infra/docker-compose.yml -f infra/cluster.yml)
all=(cluster-lb go-services java-services go-gw elixir-gw rust-gw java-gw)

case ${1:-} in
  up)
    # Push settings from .env, with the key file resolved for mounting.
    if [ -f .env ]; then
      set -a
      . ./.env
      set +a
    fi
    key=${WRINKLE_APNS_KEY:-}
    key=${key/#\~/$HOME}
    if [ -n "$key" ]; then
      APNS_KEY_FILE="$(cd "$(dirname "$key")" && pwd)/$(basename "$key")"
      WRINKLE_APNS_KEY_ID=${WRINKLE_APNS_KEY_ID:-$(basename "$key" .p8 | sed 's/^AuthKey_//')}
      export APNS_KEY_FILE WRINKLE_APNS_KEY_ID
    fi
    if [ -z "$("${compose[@]}" ps -q cluster-lb)" ]; then
      backends/ports-free.sh 8080 8404
    fi
    "${compose[@]}" up -d --build --wait \
      --scale go-services="${GO_SERVICES:-2}" --scale java-services="${JAVA_SERVICES:-1}" \
      --scale go-gw="${GO_GW:-1}" --scale elixir-gw="${ELIXIR_GW:-1}" \
      --scale rust-gw="${RUST_GW:-1}" --scale java-gw="${JAVA_GW:-1}" \
      "${all[@]}"
    "${compose[@]}" ps --format '{{.Service}}' "${all[@]}" | sort | uniq -c
    echo "devices: ws://<this host>:8080/v1/ws; statistics: http://localhost:8404/"
    ;;
  down)
    "${compose[@]}" rm --stop --force "${all[@]}"
    ;;
  status)
    "${compose[@]}" ps "${all[@]}"
    ;;
  logs)
    shift
    "${compose[@]}" logs -f --tail 50 "${@:-${all[@]}}"
    ;;
  *)
    echo "usage: cluster.sh up|down|status|logs [service]" >&2
    exit 2
    ;;
esac

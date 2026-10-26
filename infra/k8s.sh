#!/bin/bash
# The stateless tier on Docker Desktop's Kubernetes (infra/k8s): services
# instances and gateways in any number and language, beside the Compose
# cluster rather than in it; the infrastructure stays in Compose.
# Usage: k8s.sh up|down|status|logs [kind]
# Counts for up, from the environment, as for cluster.sh: GO_SERVICES,
# JAVA_SERVICES, TS_SERVICES, GO_GW, ELIXIR_GW, RUST_GW, JAVA_GW, TS_GW (up to
# 8 each). A count not given keeps what is running, or takes its default on a
# fresh start. Running up again rebuilds the images and rolls the pods whose
# image or settings changed.
set -euo pipefail
cd "$(dirname "$0")/.."
# Always Docker Desktop's cluster, whatever kubectl's current context is.
kube=(kubectl --context docker-desktop)
k=("${kube[@]}" --namespace wrinkle)
entry=(docker compose -f infra/docker-compose.yml -f infra/k8s/entry.yml)
kinds=(go-services java-services ts-services go-gw elixir-gw rust-gw java-gw ts-gw)
repos=(wrinkle/go wrinkle/java wrinkle/ts wrinkle/elixir wrinkle/rust)

# The count for a kind: given, else nothing (keep what runs), else the default.
count() {
  local given=$1 default=$2
  if [ -n "$given" ]; then
    if ! [[ $given =~ ^[0-8]$ ]]; then
      echo "counts are 0 to 8, not $given" >&2
      exit 2
    fi
    echo "$given"
  elif [ -z "$running" ]; then
    echo "$default"
  fi
}

# The image tag for this build: a digest of its layers and settings, so a
# changed image is a new tag and Kubernetes rolls the pods using it. Not the
# image's id, which every build changes.
tag() {
  echo "k8s-$(docker image inspect -f '{{json .RootFS.Layers}}{{json .Config}}' "$1:dev" | shasum -a 256 | cut -c1-12)"
}

# Removes the tags of earlier builds, all of them with "all".
untag() {
  local repo current old
  for repo in "${repos[@]}"; do
    current=$([ "${1:-}" = all ] || tag "$repo")
    for old in $(docker images "$repo" --format '{{.Tag}}' | grep '^k8s-' || true); do
      if [ "$old" != "$current" ]; then docker rmi -f "$repo:$old" >/dev/null; fi
    done
  done
}

case ${1:-} in
  up)
    if ! "${kube[@]}" get nodes >/dev/null 2>&1; then
      echo "Kubernetes is not running: enable it in Docker Desktop, Settings, Kubernetes." >&2
      exit 1
    fi
    running=$("${k[@]}" get deployment balancer -o name 2>/dev/null || true)
    if [ -z "$running" ]; then
      backends/ports-free.sh 8080 8404
    fi
    docker compose -f infra/docker-compose.yml -f infra/cluster.yml build go-services java-services ts-services elixir-gw rust-gw

    # The services' settings from .env, as a Secret; the APNs key file as
    # another, when there is one.
    if [ -f .env ]; then
      set -a
      # shellcheck source=/dev/null
      . ./.env
      set +a
    fi
    if [ -z "${WRINKLE_BLOB_KEY_ID:-}" ]; then
      echo "no blob key in .env: make infra-up makes one" >&2
      exit 1
    fi
    key=${WRINKLE_APNS_KEY:-}
    key=${key/#\~/$HOME}
    settings=$(mktemp)
    trap 'rm -f "$settings"' EXIT
    {
      echo "WRINKLE_BLOB_KEY_ID=$WRINKLE_BLOB_KEY_ID"
      echo "WRINKLE_BLOB_SECRET=${WRINKLE_BLOB_SECRET:-}"
      echo "WRINKLE_TEAM_ID=${WRINKLE_TEAM_ID:-}"
      if [ -n "$key" ]; then
        echo "WRINKLE_APNS_KEY=/run/apns/key.p8"
        echo "WRINKLE_APNS_KEY_ID=${WRINKLE_APNS_KEY_ID:-$(basename "$key" .p8 | sed 's/^AuthKey_//')}"
      fi
    } >"$settings"
    "${kube[@]}" apply -f infra/k8s/namespace.yaml >/dev/null
    "${k[@]}" create secret generic wrinkle-env --from-env-file="$settings" --dry-run=client -o yaml |
      "${k[@]}" apply -f - >/dev/null
    if [ -n "$key" ]; then
      "${k[@]}" create secret generic wrinkle-apns --from-file=key.p8="$key" --dry-run=client -o yaml |
        "${k[@]}" apply -f - >/dev/null
    else
      "${k[@]}" delete secret wrinkle-apns --ignore-not-found >/dev/null
    fi
    # A digest of both, on the services' pods: a changed setting rolls them.
    digest=$({
      cat "$settings"
      if [ -n "$key" ]; then cat "$key"; fi
    } | shasum -a 256 | cut -c1-16)

    # The manifests with this build's tags, through an overlay made for
    # each run, in build/, which is not committed.
    overlay=build/k8s
    mkdir -p "$overlay"
    {
      echo "resources: [../../infra/k8s]"
      echo "images:"
      for repo in "${repos[@]}"; do
        docker tag "$repo:dev" "$repo:$(tag "$repo")"
        echo "  - {name: $repo, newTag: $(tag "$repo")}"
      done
      cat <<EOF
patches:
  - target: {kind: Deployment, name: ".*-services"}
    patch: |
      - op: add
        path: /spec/template/metadata/annotations
        value: {wrinkle/settings: "$digest"}
EOF
    } >"$overlay/kustomization.yaml"
    "${kube[@]}" apply -k "$overlay"

    scale() {
      local n
      n=$(count "$2" "$3")
      if [ -n "$n" ]; then "${k[@]}" scale deployment "$1" --replicas="$n" >/dev/null; fi
    }
    scale go-services "${GO_SERVICES:-}" 2
    scale java-services "${JAVA_SERVICES:-}" 1
    scale ts-services "${TS_SERVICES:-}" 1
    scale go-gw "${GO_GW:-}" 1
    scale elixir-gw "${ELIXIR_GW:-}" 1
    scale rust-gw "${RUST_GW:-}" 1
    scale java-gw "${JAVA_GW:-}" 1
    scale ts-gw "${TS_GW:-}" 1
    for kind in balancer metrics "${kinds[@]}"; do
      "${k[@]}" rollout status deployment "$kind" --timeout=180s >/dev/null
    done
    untag

    node=$("${kube[@]}" get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')
    K8S_NODE=$node "${entry[@]}" up -d --wait k8s-entry
    "${k[@]}" get deployments
    echo "devices: ws://<this host>:8080/v1/ws; statistics: http://localhost:8404/"
    ;;
  down)
    "${entry[@]}" rm --stop --force k8s-entry
    "${kube[@]}" delete namespace wrinkle --ignore-not-found
    untag all
    ;;
  status)
    "${k[@]}" get deployments,pods -o wide
    ;;
  logs)
    shift
    if [ $# -gt 0 ]; then
      "${k[@]}" logs -f --tail 50 --prefix --max-log-requests 20 -l app="$1"
    else
      "${k[@]}" logs -f --tail 50 --prefix --max-log-requests 50 -l 'tier in (services, gateway)'
    fi
    ;;
  *)
    echo "usage: k8s.sh up|down|status|logs [kind]" >&2
    exit 2
    ;;
esac

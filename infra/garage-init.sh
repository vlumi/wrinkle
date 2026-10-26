#!/bin/bash
# Sets up the Garage node: its layout, the services' access key, and the
# blobs bucket, writable with the key and readable by anyone through the web
# endpoint. Idempotent.
set -euo pipefail
cd "$(dirname "$0")"
garage() { docker compose exec -T garage /garage "$@" 2>/dev/null; }

# The services' access key, made once per machine and kept in the
# repository's .env (WRINKLE_BLOB_KEY_ID, WRINKLE_BLOB_SECRET), which every
# backend reads; the stack is recreated with the same key.
env=../.env
key_id="" secret=""
if [ -f "$env" ]; then
  key_id=$(sed -n 's/^WRINKLE_BLOB_KEY_ID=//p' "$env" | tail -1)
  secret=$(sed -n 's/^WRINKLE_BLOB_SECRET=//p' "$env" | tail -1)
fi
if [ -z "$key_id" ] || [ -z "$secret" ]; then
  key_id=GK$(openssl rand -hex 12)
  secret=$(openssl rand -hex 32)
  printf '\n# The local blob storage key, made by make infra-up.\nWRINKLE_BLOB_KEY_ID=%s\nWRINKLE_BLOB_SECRET=%s\n' "$key_id" "$secret" >>"$env"
  echo "blob storage key written to .env"
fi

node=$(garage node id -q | cut -d@ -f1)
if ! garage layout show | grep -q "${node:0:16}.*local"; then
  garage layout assign -z local -c 1G "$node" >/dev/null
  garage layout apply --version 1 >/dev/null
fi
garage key info "$key_id" >/dev/null || garage key import --yes -n wrinkle "$key_id" "$secret" >/dev/null
# The key committed before 2026-10-26, which anyone could sign with.
garage key delete --yes GK7772696e6b6c6500000000 >/dev/null || true
garage bucket info blobs >/dev/null || garage bucket create blobs >/dev/null
garage bucket allow --read --write --owner blobs --key "$key_id" >/dev/null
garage bucket website --allow blobs >/dev/null

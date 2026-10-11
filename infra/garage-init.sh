#!/bin/bash
# Sets up the Garage node: its layout, the services' access key, and the
# blobs bucket, writable with the key and readable by anyone through the web
# endpoint. Idempotent.
set -euo pipefail
cd "$(dirname "$0")"
garage() { docker compose exec -T garage /garage "$@" 2>/dev/null; }

# The local credentials the backends use by default (WRINKLE_BLOB_KEY_ID,
# WRINKLE_BLOB_SECRET); nothing outside this machine reaches the S3 API.
key_id=GK7772696e6b6c6500000000
secret=667c7e6aafc73808d30a89b14f18816b605d7d340823d9493b32b97aa7079fc4

node=$(garage node id -q | cut -d@ -f1)
if ! garage layout show | grep -q "${node:0:16}.*local"; then
  garage layout assign -z local -c 1G "$node" >/dev/null
  garage layout apply --version 1 >/dev/null
fi
garage key info "$key_id" >/dev/null || garage key import --yes -n wrinkle "$key_id" "$secret" >/dev/null
garage bucket info blobs >/dev/null || garage bucket create blobs >/dev/null
garage bucket allow --read --write --owner blobs --key "$key_id" >/dev/null
garage bucket website --allow blobs >/dev/null

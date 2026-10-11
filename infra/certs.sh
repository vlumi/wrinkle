#!/bin/bash
# Makes a TLS certificate for this Mac, signed by mkcert's local authority,
# into infra/certs/: for localhost, the Bonjour name and the LAN addresses.
# Browsers and devices trust it once mkcert's root is installed on them
# (docs/local-network.md). Run again when the LAN address changes.
set -euo pipefail
cd "$(dirname "$0")"
if ! command -v mkcert >/dev/null; then
  echo "mkcert is missing: brew install mkcert, then mkcert -install once" >&2
  exit 1
fi
names=(localhost 127.0.0.1 ::1 "$(scutil --get LocalHostName).local")
for interface in en0 en1; do
  if address=$(ipconfig getifaddr "$interface" 2>/dev/null); then names+=("$address"); fi
done
mkdir -p certs
mkcert -cert-file certs/cert.pem -key-file certs/key.pem "${names[@]}"
# HAProxy takes the certificate and its key in one file.
cat certs/cert.pem certs/key.pem >certs/haproxy.pem
echo "certificate for ${names[*]} in infra/certs; the root to install on devices: $(mkcert -CAROOT)/rootCA.pem"

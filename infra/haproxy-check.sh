#!/bin/bash
# Parses every HAProxy configuration with the image the stack runs. The TLS
# edge loads its certificate, so the check makes a throwaway one; server
# names need not resolve.
set -euo pipefail
cd "$(dirname "$0")"
docker run --rm -v "$PWD:/cfg:ro" --tmpfs /usr/local/etc/haproxy/certs haproxy:3.4.6 sh -euc '
  cd /usr/local/etc/haproxy/certs
  openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=localhost -keyout key.pem -out cert.pem 2>/dev/null
  cat cert.pem key.pem >haproxy.pem
  for cfg in haproxy/haproxy.cfg haproxy/cluster.cfg edge/haproxy.cfg blobs/haproxy.cfg; do
    echo "== $cfg"
    haproxy -q -c -dr -f "/cfg/$cfg"
  done'

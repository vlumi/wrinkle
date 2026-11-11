#!/bin/bash
# Installs the TypeScript backend's dependencies. The Kafka client has no
# prebuilt binary for this Node and builds librdkafka, minutes of compiling.
# On Linux, GNU ar's script mode cuts the paths of the libraries it bundles
# at the @ of the package's scope, so the package is built where its path
# has none and keeps its name as a link, as in the Dockerfile.
set -euo pipefail
cd "$(dirname "$0")"
if [ "$(uname)" != Linux ]; then
  exec npm ci --no-audit --no-fund --silent
fi
npm ci --no-audit --no-fund --silent --ignore-scripts
mv node_modules/@confluentinc/kafka-javascript node_modules/confluentinc-kafka-javascript
ln -s ../confluentinc-kafka-javascript node_modules/@confluentinc/kafka-javascript
cd node_modules/confluentinc-kafka-javascript
log=$(mktemp)
if ! ../.bin/node-pre-gyp install --fallback-to-build >"$log" 2>&1; then
  tail -100 "$log" >&2
  exit 1
fi

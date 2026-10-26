# The TypeScript backend, for the containerized cluster. Built from the repository root.
FROM node:26.8.1-trixie-slim AS build
# The Kafka client has no prebuilt binary for this Node; it builds librdkafka
# and the libraries it links in statically, which it downloads.
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 make g++ curl ca-certificates perl patch \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY backends/ts/package.json backends/ts/package-lock.json ./
# GNU ar's script mode cuts the paths of the libraries it bundles at the @ of
# the package's scope, so the package is built where its path has none and
# keeps its name as a link.
RUN --mount=type=cache,target=/root/.npm,sharing=locked npm ci --omit=dev --no-audit --no-fund --ignore-scripts \
  && mv node_modules/@confluentinc/kafka-javascript node_modules/confluentinc-kafka-javascript \
  && ln -s ../confluentinc-kafka-javascript node_modules/@confluentinc/kafka-javascript
WORKDIR /app/node_modules/confluentinc-kafka-javascript
RUN ../.bin/node-pre-gyp install --fallback-to-build

FROM node:26.8.1-trixie-slim
WORKDIR /app
COPY --from=build /app/node_modules node_modules
COPY backends/ts/package.json ./
COPY backends/ts/src src
COPY backends/ts/wrinkle ./
USER node
ENTRYPOINT ["/app/wrinkle"]

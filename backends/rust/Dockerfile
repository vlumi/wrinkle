# The Rust gateway, for the containerized cluster. Built from the repository root.
FROM rust:1.99.0-trixie AS build
WORKDIR /src
COPY proto proto
COPY backends/rust backends/rust
WORKDIR /src/backends/rust
RUN --mount=type=cache,target=/usr/local/cargo/registry,sharing=locked --mount=type=cache,target=/src/backends/rust/target,sharing=locked \
    cargo build --release && cp target/release/wrinkle /wrinkle

FROM debian:trixie-slim
COPY --from=build /wrinkle /wrinkle
ENTRYPOINT ["/wrinkle"]

# The Go backend, for the containerized cluster. Built from the repository root.
FROM golang:1.27.2-trixie AS build
WORKDIR /src
COPY go.work go.work.sum ./
COPY backends/go backends/go
COPY clients/go clients/go
COPY conformance conformance
COPY loadgen loadgen
COPY proto/go proto/go
RUN --mount=type=cache,target=/root/.cache/go-build,sharing=locked --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    CGO_ENABLED=0 go build -o /wrinkle ./backends/go/cmd/wrinkle

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /wrinkle /wrinkle
ENTRYPOINT ["/wrinkle"]

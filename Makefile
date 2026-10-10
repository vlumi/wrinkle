# Local CI. `make ci` is the gate before every push to main.

COMPOSE := docker compose -f infra/docker-compose.yml
export PATH := $(shell go env GOPATH)/bin:$(PATH)

GO_MODULES := backends/go clients/go conformance proto/go
# Conformance tests need a running backend; they run from conformance-*.
GO_UNIT_MODULES := backends/go clients/go proto/go

.PHONY: ci lint proto-lint go-lint go-test proto proto-check infra-up infra-down smoke conformance-go tools

ci: lint proto-check go-test smoke conformance-go

lint: proto-lint go-lint

go-lint:
	@for m in $(GO_MODULES); do echo "== $$m"; (cd $$m && golangci-lint run ./...) || exit 1; done

go-test:
	@for m in $(GO_UNIT_MODULES); do echo "== $$m"; (cd $$m && go test ./...) || exit 1; done

proto-lint:
	buf lint
	buf format --diff --exit-code

# Generated code is committed; proto-check fails when it is stale.
proto:
	buf generate

proto-check: proto
	@git status --porcelain -- proto/go | grep . && { echo "generated code is stale, commit the output of make proto"; exit 1; } || true

infra-up:
	$(COMPOSE) up -d --build --wait
	infra/debezium/register.sh

infra-down:
	$(COMPOSE) down -v --remove-orphans

smoke: infra-up
	infra/smoke-test.sh

# Conformance tests, one target per backend. The runner starts the backend,
# tests, and stops it.
conformance-go: infra-up
	go build -o bin/wrinkle ./backends/go/cmd/wrinkle
	conformance/run.sh bin/wrinkle

tools:
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

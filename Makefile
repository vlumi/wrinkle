# Local CI. `make ci` is the gate before every push to main.

COMPOSE := docker compose -f infra/docker-compose.yml
export PATH := $(shell go env GOPATH)/bin:$(PATH)

.PHONY: ci lint proto-lint proto proto-check infra-up infra-down smoke tools

ci: lint proto-check smoke

lint: proto-lint

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

tools:
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest

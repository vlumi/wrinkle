# Wrinkle: routine operations. `make` lists them; `make ci` is the gate before
# every push to main.

SHELL := /bin/bash
.DEFAULT_GOAL := help

COMPOSE := docker compose -f infra/docker-compose.yml
export PATH := $(shell go env GOPATH 2>/dev/null)/bin:$(HOME)/.mix/escripts:/opt/homebrew/opt/rustup/bin:$(PATH)
# The Java backend needs JDK 25; Gradle and its launcher find it here.
export JAVA_HOME := $(shell /usr/libexec/java_home -v 25 2>/dev/null)

GO_BACKEND := $(CURDIR)/bin/wrinkle
JAVA_BACKEND := $(CURDIR)/backends/java/build/install/wrinkle/bin/wrinkle
GRADLE := cd backends/java && ./gradlew --quiet --console=plain
CARGO := cd backends/rust && cargo
MIX := cd backends/elixir && mix
NPM := cd clients/web && npm

GO_MODULES := backends/go clients/go conformance loadgen proto/go
# Conformance tests need a running backend; they run from conformance-*.
GO_UNIT_MODULES := backends/go clients/go loadgen proto/go
IOS_SWIFT := Packages/WrinkleCore/Sources/WrinkleCore/Transport Packages/WrinkleCore/Sources/WrinkleCore/Storage Packages/WrinkleCore/Sources/WrinkleKit Packages/WrinkleCore/Tests Sources
# Generated code is committed; proto-check fails when it is stale.
GENERATED := proto/go clients/ios/Packages/WrinkleCore/Sources/WrinkleCore/Contract backends/elixir/lib/wrinkle/proto clients/web/src/gen

.PHONY: help
help:
	@echo "Wrinkle. Usage: make <target> [VAR=value]"
	@awk 'BEGIN {FS = ":.*## "} \
		/^##@ / {printf "\n\033[1m%s\033[0m\n", substr($$0, 5); next} \
		/^##~ / {printf "  \033[2m%s\033[0m\n", substr($$0, 5); next} \
		/^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo

##@ Infrastructure (Vitess, Kafka, Debezium, Valkey, Prometheus, Grafana)

.PHONY: infra-up infra-down infra-reset infra-status infra-logs reset-fanout
infra-up:  ## Start the stack and register the outbox connector
	$(COMPOSE) up -d --build --wait
	infra/debezium/register.sh

infra-down:  ## Stop the stack and discard all data
	$(COMPOSE) down -v --remove-orphans

infra-reset: infra-down infra-up  ## Recreate the stack empty

infra-status:  ## Show the stack's containers, their health and resource use
	@$(COMPOSE) ps --format 'table {{.Service}}\t{{.Status}}'
	@echo
	@docker stats --no-stream --format 'table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}' | grep -E 'NAME|wrinkle'

infra-logs:  ## Follow the stack's logs (SERVICE=vitess|kafka|connect|... for one)
	$(COMPOSE) logs -f --tail 100 $(SERVICE)

reset-fanout:  ## Skip what is waiting in Kafka for the fan-out (stop the backend first)
	infra/reset-fanout.sh

##@ Run a backend (gateway on :8080; one at a time; Ctrl-C stops it)
##~ Settings for push and the iOS team come from .env (see .env.example). ARGS=... passes flags.

.PHONY: run-go run-go-split run-java run-elixir run-rust
run-go: build-go  ## Go, all roles in one process
	$(GO_BACKEND) $(ARGS)

run-go-split: build-go  ## Go as a gateway and a services process over the internal RPC
	backends/go/split.sh $(ARGS)

run-java: build-java  ## Java, all roles in one process
	$(JAVA_BACKEND) $(ARGS)

run-elixir: build-go build-elixir  ## Elixir gateway, with the Go services beside it
	backends/elixir/run.sh

run-rust: build-go build-rust  ## Rust gateway, with the Go services beside it
	backends/rust/run.sh

##@ Clients

.PHONY: web web-dev ios-sim ios-device ios-project
web: build-web  ## Web client, built and served to the network on :8081
	$(NPM) run --silent serve

web-dev: web-deps  ## Web client with live reload on :8081, for working on it
	$(NPM) run dev

ios-sim:  ## iOS app in an iPhone simulator (DEVICE="17 Pro" to pick one)
	clients/ios/Scripts/generate.sh
	clients/ios/Scripts/run-ios.sh "$(DEVICE)"

ios-device:  ## iOS app on a paired iPhone, signed with WRINKLE_TEAM_ID (DEVICE=<name>)
	clients/ios/Scripts/run-device.sh "$(DEVICE)"

ios-project:  ## Regenerate the Xcode project, to open in Xcode
	clients/ios/Scripts/generate.sh

##@ Build

.PHONY: build build-go build-java build-elixir build-rust build-web build-ios build-loadgen
build: build-go build-java build-elixir build-rust build-web build-ios build-loadgen  ## Everything

build-go:  ## Go backend (bin/wrinkle)
	go build -o $(GO_BACKEND) ./backends/go/cmd/wrinkle

build-java:  ## Java backend
	$(GRADLE) installDist

build-elixir: elixir-deps  ## Elixir gateway
	$(MIX) compile --quiet 2>/dev/null

build-rust:  ## Rust gateway (release)
	$(CARGO) build --release --quiet

build-web: web-deps  ## Web client (clients/web/dist)
	$(NPM) run --silent build

build-ios: ios-project  ## iOS app for the simulator, unsigned
	clients/ios/Scripts/build.sh

build-loadgen:  ## Load generator (bin/loadgen)
	go build -o bin/loadgen ./loadgen

##@ Check (make ci runs all of it)

.PHONY: ci lint format test conformance smoke web-test ios-test
ci: lint proto-check test build-ios smoke conformance web-test ios-test  ## The local CI: lint, unit tests, builds, conformance, client tests

lint:  proto-lint go-lint java-lint elixir-lint rust-lint web-lint ios-lint  ## Formatting checks and linters for everything

format:  ## Rewrite sources with each language's formatter
	buf format -w
	@for m in $(GO_MODULES); do (cd $$m && gofmt -w .); done
	$(GRADLE) spotlessApply
	$(MIX) format
	$(CARGO) fmt
	cd clients/ios && swift format --in-place --recursive --configuration .swift-format $(IOS_SWIFT)

test: go-test java-test elixir-test rust-test web-unit  ## Unit tests for everything

conformance: conformance-go conformance-go-split conformance-java conformance-elixir conformance-rust  ## The conformance tests against every backend

smoke: infra-up  ## The change-data path: an outbox insert reaches Kafka
	infra/smoke-test.sh

web-test: infra-up web-deps build-go  ## The web client in headless Chrome against the Go backend
	clients/web/test/run.sh $(GO_BACKEND)

ios-test: infra-up build-go  ## The iOS client's package tests, headless, against the Go backend
	clients/ios/Scripts/test-backend.sh $(GO_BACKEND)

##~ conformance-go, -go-split, -java, -elixir, -rust test one backend.

.PHONY: conformance-go conformance-go-split conformance-java conformance-elixir conformance-rust
conformance-go: infra-up build-go
	conformance/run.sh $(GO_BACKEND)

conformance-go-split: infra-up build-go
	conformance/run.sh $(CURDIR)/backends/go/split.sh

conformance-java: infra-up build-java
	conformance/run.sh $(JAVA_BACKEND)

conformance-elixir: infra-up build-go elixir-deps
	conformance/run.sh $(CURDIR)/backends/elixir/run.sh

conformance-rust: infra-up build-go build-rust
	conformance/run.sh $(CURDIR)/backends/rust/run.sh

##@ Measure (docs/measuring.md)

CONNECTIONS ?= 1000
RATE ?= 500
DURATION ?= 30s

.PHONY: load measure-go measure-go-split measure-java measure-elixir measure-rust measure-all
load: build-loadgen  ## A quick load run against the running backend (CONNECTIONS=1000 RATE=500 DURATION=30s)
	bin/loadgen --connections $(CONNECTIONS) --rate $(RATE) --duration $(DURATION) --warmup 10s

measure-all: measure-go measure-go-split measure-java measure-elixir measure-rust  ## The protocol against every backend, each from a fresh stack (about 50 minutes)

##~ measure-go, -go-split, -java, -elixir, -rust run the protocol for one (about 10 minutes each).

measure-go: infra-up build-go
	FRESH=1 LABEL=go loadgen/protocol.sh docs/results/$$(date +%Y-%m-%d)-go.jsonl $(GO_BACKEND)

measure-go-split: infra-up build-go
	FRESH=1 LABEL=go-gateway loadgen/protocol.sh docs/results/$$(date +%Y-%m-%d)-go-gateway.jsonl $(CURDIR)/backends/go/split.sh

measure-java: infra-up build-java
	FRESH=1 LABEL=java loadgen/protocol.sh docs/results/$$(date +%Y-%m-%d)-java.jsonl $(JAVA_BACKEND)

measure-elixir: infra-up build-go elixir-deps
	FRESH=1 LABEL=elixir loadgen/protocol.sh docs/results/$$(date +%Y-%m-%d)-elixir.jsonl $(CURDIR)/backends/elixir/run.sh

measure-rust: infra-up build-go build-rust
	FRESH=1 LABEL=rust loadgen/protocol.sh docs/results/$$(date +%Y-%m-%d)-rust.jsonl $(CURDIR)/backends/rust/run.sh

##@ Contract

.PHONY: proto proto-check
proto: web-deps  ## Regenerate code from proto/ for every language
	buf generate
	# The clients speak the device contract only.
	rm -f clients/ios/Packages/WrinkleCore/Sources/WrinkleCore/Contract/services.pb.swift
	rm -rf clients/web/src/gen/wrinkle/backend

proto-check: proto  ## Fail when the committed generated code is stale
	@git status --porcelain -- $(GENERATED) | grep . && { echo "generated code is stale, commit the output of make proto"; exit 1; } || true

##@ Setup

.PHONY: tools clean
tools:  ## Install the code generators and linters (Homebrew, go install, mix)
	brew install xcodegen swift-protobuf swiftlint elixir rustup node
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	mix local.hex --force && mix local.rebar --force && mix escript.install hex protobuf --force

clean:  ## Remove build output (not dependencies, not the stack)
	rm -rf bin backends/java/build backends/rust/target backends/elixir/_build clients/web/dist clients/ios/.build-xcode clients/ios/Wrinkle.xcodeproj

# Parts the targets above are made of.

.PHONY: proto-lint go-lint go-test java-lint java-test elixir-deps elixir-lint elixir-test rust-lint rust-test web-deps web-lint web-unit ios-lint
proto-lint:
	buf lint
	buf format --diff --exit-code

go-lint:
	@for m in $(GO_MODULES); do echo "== $$m"; (cd $$m && golangci-lint run ./...) || exit 1; done

go-test:
	@for m in $(GO_UNIT_MODULES); do echo "== $$m"; (cd $$m && go test ./...) || exit 1; done

java-lint:
	$(GRADLE) spotlessCheck compileJava

java-test:
	$(GRADLE) test

elixir-deps:
	$(MIX) deps.get --quiet 2>/dev/null

elixir-lint: elixir-deps
	$(MIX) format --check-formatted
	$(MIX) compile --warnings-as-errors --quiet 2>/dev/null

# The unit tests need no services, so the application is not started.
elixir-test: elixir-deps
	$(MIX) test --no-start 2>&1 | grep -E "^(Result|\*\*|  [0-9]+\))"; test $${PIPESTATUS[0]} -eq 0

rust-lint:
	$(CARGO) fmt --check
	$(CARGO) clippy --release --quiet -- -D warnings

rust-test:
	$(CARGO) test --release --quiet 2>&1 | grep -E "^test result|FAILED|panicked"

web-deps:
	$(NPM) ci --no-audit --no-fund --silent

web-lint: web-deps
	$(NPM) run --silent typecheck
	$(NPM) run --silent lint
	node --check clients/web/test/browser_test.mjs

web-unit: web-deps
	$(NPM) test --silent

ios-lint:
	cd clients/ios && swiftlint lint --strict --quiet
	cd clients/ios && swift format lint --strict --recursive --configuration .swift-format $(IOS_SWIFT)

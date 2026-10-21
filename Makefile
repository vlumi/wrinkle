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
# Each backend shape as the one command conformance and measurement start: it
# serves ws://localhost:8080/v1/ws, with its services beside it when split.
SHAPE_go-mono := $(GO_BACKEND)
SHAPE_java-mono := $(JAVA_BACKEND)
SHAPE_go-gw := $(CURDIR)/backends/go/split.sh
SHAPE_java-gw := $(CURDIR)/backends/java/split.sh
SHAPE_elixir-gw := $(CURDIR)/backends/elixir/run.sh
SHAPE_rust-gw := $(CURDIR)/backends/rust/run.sh
SHAPES := go-mono go-gw java-mono java-gw elixir-gw rust-gw
GRADLE := cd backends/java && ./gradlew --quiet --console=plain
CARGO := cd backends/rust && cargo
MIX := cd backends/elixir && mix
NPM := cd clients/web && npm

GO_MODULES := backends/go clients/go conformance loadgen proto/go
# Conformance tests need a running backend; they run from conformance-*.
GO_UNIT_MODULES := backends/go clients/go loadgen proto/go
# Every Swift source but the generated contract, relative to clients/ios.
IOS_SWIFT = $(shell cd clients/ios && find Packages Sources -name '*.swift' -not -path '*/Contract/*' -not -path '*/.build*')
# Generated code is committed; proto-check fails when it is stale.
GENERATED := proto/go clients/ios/Packages/WrinkleCore/Sources/WrinkleCore/Contract backends/elixir/lib/wrinkle/proto clients/web/src/gen
SH_FILES = $(shell git ls-files --cached --others --exclude-standard '*.sh')
SHFMT := -i 2 -ci

.PHONY: help
help:
	@echo "Wrinkle. Usage: make <target> [VAR=value]"
	@awk 'BEGIN {FS = ":.*## "} \
		/^##@ / {printf "\n\033[1m%s\033[0m\n", substr($$0, 5); next} \
		/^##~ / {printf "  \033[2m%s\033[0m\n", substr($$0, 5); next} \
		/^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo

##@ Infrastructure (Vitess, Kafka, Debezium, Valkey, Garage, Prometheus, Grafana)

.PHONY: infra-up infra-down infra-reset infra-status infra-logs reset-fanout
infra-up:  ## Start the stack and register the outbox connectors
	$(COMPOSE) up -d --build --wait
	infra/kafka-topics.sh
	infra/garage-init.sh
	infra/debezium/register.sh

infra-down:  ## Stop the stack and discard all data
	$(COMPOSE) --profile lb --profile tls down -v --remove-orphans

infra-reset: infra-down infra-up  ## Recreate the stack empty

infra-status:  ## Show the stack's containers, their health and resource use
	@$(COMPOSE) ps --format 'table {{.Service}}\t{{.Status}}'
	@echo
	@docker stats --no-stream --format 'table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}' | grep -E 'NAME|wrinkle'

infra-logs:  ## Follow the stack's logs (SERVICE=vitess|kafka|connect|... for one)
	$(COMPOSE) logs -f --tail 100 $(SERVICE)

reset-fanout:  ## Skip what is waiting in Kafka for the fan-out (stop the backend first)
	infra/reset-fanout.sh

##@ Monoliths (all roles in one process, on :8080; one at a time; Ctrl-C stops it)
##~ Settings for push and the iOS team come from .env (see .env.example). ARGS=... passes flags.

.PHONY: run-go-mono run-java-mono
run-go-mono: build-go  ## Go, all roles in one process
	backends/ports-free.sh 8080
	$(GO_BACKEND) $(ARGS)

run-java-mono: build-java  ## Java, all roles in one process
	backends/ports-free.sh 8080
	$(JAVA_BACKEND) $(ARGS)

##@ Side by side (services instances and gateways behind the balancers)
##~ make lb-up, then services instances and gateways in other terminals, in any number and language.
##~ INSTANCE=2 (up to 4) runs another services instance; PORT=... moves a gateway.

INSTANCE ?= 1

.PHONY: run-go-services run-java-services run-go-gw run-java-gw run-elixir-gw run-rust-gw
run-go-services: build-go  ## Go services and fan-out, instance INSTANCE (RPC on :9100+n)
	INSTANCE=$(INSTANCE) backends/services.sh go $(ARGS)

run-java-services: build-java  ## Java services and fan-out, instance INSTANCE; no pushes yet
	INSTANCE=$(INSTANCE) backends/services.sh java $(ARGS)

run-go-gw: build-go  ## Go gateway on :8180
	backends/gateway.sh go $(PORT)

run-elixir-gw: build-elixir  ## Elixir gateway on :8181
	backends/gateway.sh elixir $(PORT)

run-rust-gw: build-rust  ## Rust gateway on :8182
	backends/gateway.sh rust $(PORT)

run-java-gw: build-java  ## Java gateway on :8183
	backends/gateway.sh java $(PORT)

.PHONY: certs tls-up tls-down lb-up lb-down
certs:  ## A TLS certificate for this Mac from mkcert, for the TLS edge and the web client
	infra/certs.sh

tls-up:  ## TLS for devices on the network: wss://<address>:8443/v1/ws to whatever serves :8080 on this host
	@test -f infra/certs/haproxy.pem || infra/certs.sh
	$(COMPOSE) --profile tls up -d --wait edge

tls-down:  ## Stop the TLS edge
	$(COMPOSE) --profile tls stop edge

lb-up:  ## The balancers: devices on :8080 to the gateways, gateways on :9100 to the services; statistics on localhost:8404
	backends/ports-free.sh 8080
	$(COMPOSE) --profile lb up -d --wait haproxy

lb-down:  ## Stop the balancers
	$(COMPOSE) --profile lb stop haproxy

##@ Cluster (the side-by-side setup in containers, any number of each)
##~ Counts: GO_SERVICES JAVA_SERVICES GO_GW ELIXIR_GW RUST_GW JAVA_GW, 0 to 8 each; a count not given keeps
##~ what is running (defaults 2 1 1 1 1 1 on a fresh start). Devices on :8080; statistics on localhost:8404.

.PHONY: cluster-up cluster-down cluster-status cluster-logs
cluster-up: infra-up  ## Rebuild the images and start, update or rescale the cluster
	GO_SERVICES=$(GO_SERVICES) JAVA_SERVICES=$(JAVA_SERVICES) GO_GW=$(GO_GW) ELIXIR_GW=$(ELIXIR_GW) RUST_GW=$(RUST_GW) JAVA_GW=$(JAVA_GW) infra/cluster.sh up

cluster-down:  ## Remove the cluster, leaving the infrastructure running; the next start uses the defaults
	infra/cluster.sh down

cluster-status:  ## Every container of the cluster
	infra/cluster.sh status

cluster-logs:  ## Follow the cluster's logs (SERVICE=go-gw for one kind)
	infra/cluster.sh logs $(SERVICE)

##@ Clients

.PHONY: web web-dev monitor ios-sim ios-device ios-project
web: build-web tls-up  ## Web client, built and served to the network over HTTPS on :8081, with the TLS edge
	$(NPM) run --silent serve

web-dev: web-deps tls-up  ## Web client with live reload over HTTPS on :8081, with the TLS edge, for working on it
	$(NPM) run dev

monitor:  ## Open the monitoring page, links to every dashboard (on this host, with make web running)
	open "$$(test -f infra/certs/cert.pem && echo https || echo http)://localhost:8081/monitor"

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

lint:  proto-lint go-lint java-lint elixir-lint rust-lint web-lint ios-lint shell-lint docker-lint infra-lint  ## Formatting checks and linters for everything

format:  ## Rewrite sources with each language's formatter
	buf format -w
	@for m in $(GO_MODULES); do (cd $$m && gofmt -w .); done
	$(GRADLE) spotlessApply
	$(MIX) format
	$(CARGO) fmt
	$(NPM) run --silent format
	shfmt -w $(SHFMT) $(SH_FILES)
	cd clients/ios && swift format --in-place --recursive --configuration .swift-format $(IOS_SWIFT)

test: go-test java-test elixir-test rust-test web-unit  ## Unit tests for everything

conformance: $(SHAPES:%=conformance-%)  ## The conformance tests against every backend shape

smoke: infra-up  ## The change-data path: an outbox insert reaches Kafka
	infra/smoke-test.sh

web-test: infra-up web-deps build-go  ## The web client in headless Chrome against the Go backend
	clients/web/test/run.sh $(GO_BACKEND)

ios-test: infra-up build-go  ## The iOS client's package tests, headless, against the Go backend
	clients/ios/Scripts/test-backend.sh $(GO_BACKEND)

##~ conformance-go-mono, -go-gw, -java-mono, -java-gw, -elixir-gw, -rust-gw test one shape.

# What each shape needs built: Go's services run beside every gateway but Java's.
NEEDS_go-mono := build-go
NEEDS_go-gw := build-go
NEEDS_java-mono := build-java
NEEDS_java-gw := build-java
NEEDS_elixir-gw := build-go build-elixir
NEEDS_rust-gw := build-go build-rust

.PHONY: $(SHAPES:%=conformance-%)
.SECONDEXPANSION:
$(SHAPES:%=conformance-%): conformance-%: infra-up $$(NEEDS_%)
	conformance/run.sh $(SHAPE_$*)

##@ Measure (docs/measuring.md)

CONNECTIONS ?= 1000
RATE ?= 500
DURATION ?= 30s
LOADGEN_ARGS ?=

.PHONY: load measure-all $(SHAPES:%=measure-%)
load: build-loadgen  ## A quick load run against the running backend (CONNECTIONS=1000 RATE=500 DURATION=30s, LOADGEN_ARGS for any other flag)
	bin/loadgen --connections $(CONNECTIONS) --rate $(RATE) --duration $(DURATION) --warmup 10s $(LOADGEN_ARGS)

measure-all: $(SHAPES:%=measure-%)  ## The protocol against every backend shape, each from a fresh stack (about 75 minutes per repetition)

##~ measure-go-mono, -java-mono, -go-gw, -java-gw, -elixir-gw, -rust-gw run the protocol for one (about 12 minutes each per repetition).

# The labels name the results files, as in the earlier write-ups.
LABEL_go-mono := go
LABEL_java-mono := java
LABEL_go-gw := go-gateway
LABEL_java-gw := java-gateway
LABEL_elixir-gw := elixir
LABEL_rust-gw := rust

# One make invocation is one session: every shape's file carries the date it
# started. RESULTS_DATE, from the environment too, continues an earlier session.
RESULTS_DATE ?= $(shell date +%Y-%m-%d)
RESULTS_DATE := $(RESULTS_DATE)

$(SHAPES:%=measure-%): measure-%: infra-up $$(NEEDS_%)
	FRESH=1 LABEL=$(LABEL_$*) loadgen/protocol.sh docs/results/$(RESULTS_DATE)-$(LABEL_$*).jsonl $(SHAPE_$*)

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
	brew install xcodegen swift-protobuf swiftlint elixir rustup node shellcheck shfmt hadolint mkcert
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	mix local.hex --force && mix local.rebar --force && mix escript.install hex protobuf --force

clean:  ## Remove build output (not dependencies, not the stack)
	rm -rf bin backends/java/build backends/rust/target backends/elixir/_build clients/web/dist clients/ios/.build-xcode clients/ios/Wrinkle.xcodeproj clients/ios/Packages/WrinkleCore/.build

# Parts the targets above are made of.

.PHONY: proto-lint go-lint go-test java-lint java-test elixir-deps elixir-lint elixir-test rust-lint rust-test web-deps web-lint web-unit ios-lint shell-lint docker-lint infra-lint
proto-lint:
	buf lint
	buf format --diff --exit-code

go-lint:
	@for m in $(GO_MODULES); do echo "== $$m"; (cd $$m && golangci-lint run ./...) || exit 1; done

go-test:
	@for m in $(GO_UNIT_MODULES); do echo "== $$m"; (cd $$m && go test -race ./...) || exit 1; done

java-lint:
	$(GRADLE) spotlessCheck compileJava

java-test:
	$(GRADLE) test

elixir-deps:
	$(MIX) deps.get --quiet 2>/dev/null

elixir-lint: elixir-deps
	$(MIX) format --check-formatted
	$(MIX) compile --warnings-as-errors --quiet 2>/dev/null
	$(MIX) credo --strict

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
	$(NPM) run --silent format-check
	node --check clients/web/test/browser_test.mjs

web-unit: web-deps
	$(NPM) test --silent

ios-lint:
	cd clients/ios && swiftlint lint --strict --quiet
	cd clients/ios && swift format lint --strict --recursive --configuration .swift-format $(IOS_SWIFT)

shell-lint:
	shellcheck $(SH_FILES)
	shfmt -d $(SHFMT) $(SH_FILES)

docker-lint:
	hadolint $(shell git ls-files --cached --others --exclude-standard '*Dockerfile')

# The Compose files and every HAProxy configuration parse.
infra-lint:
	$(COMPOSE) config --quiet
	$(COMPOSE) -f infra/cluster.yml config --quiet
	infra/haproxy-check.sh

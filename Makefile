# Local CI. `make ci` is the gate before every push to main.

COMPOSE := docker compose -f infra/docker-compose.yml
export PATH := $(shell go env GOPATH)/bin:$(PATH)
# The Java backend needs JDK 25; Gradle and its launcher find it here.
export JAVA_HOME := $(shell /usr/libexec/java_home -v 25 2>/dev/null)
GRADLE := cd backends/java && ./gradlew --quiet --console=plain
JAVA_BACKEND := $(CURDIR)/backends/java/build/install/wrinkle/bin/wrinkle

GO_MODULES := backends/go clients/go conformance loadgen proto/go
# Conformance tests need a running backend; they run from conformance-*.
GO_UNIT_MODULES := backends/go clients/go loadgen proto/go

.PHONY: web web-lint web-test ios-project ios-lint ios-format ios-build ios-test measure-go measure-java ci lint proto-lint go-lint go-test java-lint java-test java-build proto proto-check infra-up infra-down smoke conformance-go conformance-java tools

ci: lint proto-check go-test java-test ios-build smoke conformance-go conformance-java web-test ios-test

lint: proto-lint go-lint java-lint web-lint ios-lint

web-lint:
	node --check clients/web/app.js
	node --check clients/web/test/browser_test.mjs

java-lint:
	$(GRADLE) spotlessCheck compileJava

# The iOS client: the logic in a Swift package tested headless on the Mac, the
# app shell built unsigned for the simulator from a generated Xcode project.
IOS_SWIFT := Packages/WrinkleCore/Sources/WrinkleCore/Transport Packages/WrinkleCore/Sources/WrinkleCore/Storage Packages/WrinkleCore/Sources/WrinkleKit Packages/WrinkleCore/Tests Sources

ios-lint:
	cd clients/ios && swiftlint lint --strict --quiet
	cd clients/ios && swift format lint --strict --recursive --configuration .swift-format $(IOS_SWIFT)

ios-format:
	cd clients/ios && swift format --in-place --recursive --configuration .swift-format $(IOS_SWIFT)

ios-project:
	clients/ios/Scripts/generate.sh

ios-build: ios-project
	clients/ios/Scripts/build.sh

java-test:
	$(GRADLE) test

java-build:
	$(GRADLE) installDist

go-lint:
	@for m in $(GO_MODULES); do echo "== $$m"; (cd $$m && golangci-lint run ./...) || exit 1; done

go-test:
	@for m in $(GO_UNIT_MODULES); do echo "== $$m"; (cd $$m && go test ./...) || exit 1; done

proto-lint:
	buf lint
	buf format --diff --exit-code

# Generated code (Go module, Swift contract) is committed; proto-check fails
# when it is stale.
proto:
	buf generate

proto-check: proto
	@git status --porcelain -- proto/go clients/ios/Packages/WrinkleCore/Sources/WrinkleCore/Contract | grep . && { echo "generated code is stale, commit the output of make proto"; exit 1; } || true

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
	conformance/run.sh $(CURDIR)/bin/wrinkle

conformance-java: infra-up java-build
	conformance/run.sh $(JAVA_BACKEND)

# The web client in headless Chrome against the Go backend.
web-test: infra-up
	go build -o bin/wrinkle ./backends/go/cmd/wrinkle
	clients/web/test/run.sh $(CURDIR)/bin/wrinkle

# The iOS client's package tests, headless, against the Go backend.
ios-test: infra-up
	go build -o bin/wrinkle ./backends/go/cmd/wrinkle
	clients/ios/Scripts/test-backend.sh $(CURDIR)/bin/wrinkle

# The measurement protocol (docs/measuring.md) against the Go backend.
measure-go: infra-up
	go build -o bin/wrinkle ./backends/go/cmd/wrinkle
	LABEL=go loadgen/protocol.sh docs/results/$$(date +%Y-%m-%d)-go.jsonl $(CURDIR)/bin/wrinkle

measure-java: infra-up java-build
	LABEL=java loadgen/protocol.sh docs/results/$$(date +%Y-%m-%d)-java.jsonl $(JAVA_BACKEND)

# Serves the repository root so the web client can load /proto/.
web:
	@echo "web client at http://localhost:8081/clients/web/"
	python3 -m http.server 8081 --directory . --bind 127.0.0.1

tools:
	brew install xcodegen swift-protobuf swiftlint
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

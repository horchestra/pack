# pack: build, test, generate.  `make help` lists the targets.

GO       ?= go
PROTOC   ?= protoc
GOBIN    := $(shell $(GO) env GOPATH)/bin
BIN      ?= bin

# protoc-gen-go and protoc-gen-go-grpc live in GOBIN
export PATH := $(GOBIN):$(PATH)

PROTO_DIR := proto
PROTO_OUT := pkg/target/remote/remotepb
PROTOS    := $(wildcard $(PROTO_DIR)/*.proto)

.DEFAULT_GOAL := help
.PHONY: help proto build test test-core test-sdk test-e2e test-e2e-hosts test-e2e-all golden vet fmt clean

help: ## list the targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-14s %s\n", $$1, $$2}'

proto: ## regenerate the target protocol stubs (proto/*.proto -> pkg/target/remote/remotepb)
	@command -v $(PROTOC) >/dev/null || { echo "protoc is not installed"; exit 1; }
	@command -v protoc-gen-go >/dev/null || { echo "protoc-gen-go is not installed: go install google.golang.org/protobuf/cmd/protoc-gen-go@latest"; exit 1; }
	@command -v protoc-gen-go-grpc >/dev/null || { echo "protoc-gen-go-grpc is not installed: go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest"; exit 1; }
	mkdir -p $(PROTO_OUT)
	$(PROTOC) -I $(PROTO_DIR) \
		--go_out=$(PROTO_OUT) --go_opt=paths=source_relative \
		--go-grpc_out=$(PROTO_OUT) --go-grpc_opt=paths=source_relative \
		$(PROTOS)

build: ## build pack into bin/
	$(GO) build -o $(BIN)/pack ./cmd/pack

test: test-core test-sdk ## everything

test-core: ## the core, the Go SDK (a module of its own) and the Go examples
	$(GO) test -race ./pkg/... ./internal/... ./cmd/... ./examples/...
	cd sdk && $(GO) test -race ./...

test-sdk: ## the Python and Java SDKs and examples against the golden files
	cd sdk/python && python3 -I -m unittest discover -s tests
	cd examples/postgres/python && python3 -m unittest test_postgres
	cd examples/openbao/python && python3 -m unittest test_openbao
	cd examples/docker/python && python3 -m unittest test_docker
	@set -e; out=$$(mktemp -d); \
	javac --release 21 -d $$out/sdk $$(find sdk/java/src/main -name '*.java'); \
	javac --release 21 -cp $$out/sdk -d $$out/sdk-test $$(find sdk/java/src/test -name '*.java'); \
	(cd sdk/java && java -cp $$out/sdk:$$out/sdk-test io.horchestra.pack.sdk.GoldenTest); \
	javac --release 21 -d $$out/ex $$(find sdk/java/src/main -name '*.java') examples/postgres/java/*.java; \
	(cd examples/postgres/java && java -cp $$out/ex PostgresTest); \
	javac --release 21 -d $$out/bao $$(find sdk/java/src/main -name '*.java') examples/postgres/java/Postgres.java examples/openbao/java/*.java; \
	(cd examples/openbao/java && java -cp $$out/bao OpenBaoTest); \
	javac --release 21 -d $$out/dock $$(find sdk/java/src/main -name '*.java') examples/docker/java/*.java; \
	(cd examples/docker/java && java -cp $$out/dock DockerTest); \
	rm -rf $$out

test-e2e: ## openbao from a registry on a real Docker engine (dev, raft, postgres): needs Docker and targets/
	cd tests/e2e && $(GO) test -count=1 -timeout 20m -run 'TestOpenBao|TestTargetInAContainer|TestScenariosOnARelease' ./...

test-e2e-hosts: ## the packs from OS packages on real Debian and Fedora hosts and from a registry mirror (ansible): slow, needs the internet
	cd tests/e2e && $(GO) test -count=1 -timeout 20m -run 'TestFromOSPackages|TestFromARegistryMirror' ./...

test-e2e-all: ## all of the e2e
	cd tests/e2e && $(GO) test -count=1 -timeout 20m ./...

golden: ## rewrite the golden files the SDKs and examples are compared with (after a deliberate change of the format)
	cd sdk && UPDATE_GOLDEN=1 $(GO) test . -run 'Golden'
	UPDATE_GOLDEN=1 $(GO) test ./examples/postgres/go ./examples/openbao/go ./examples/docker/go -run 'Golden'

vet: ## go vet of the core, of the SDK and of tests/e2e, modules of their own
	$(GO) vet ./pkg/... ./internal/... ./cmd/... ./examples/...
	cd sdk && $(GO) vet ./...
	cd tests/e2e && $(GO) vet ./...

fmt: ## gofmt of the core
	gofmt -l -w cmd examples internal pkg sdk

clean: ## remove bin/
	rm -rf $(BIN)

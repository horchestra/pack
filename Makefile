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
.PHONY: help proto build test test-core test-sdk golden vet fmt clean

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

test-core: ## the core, its SDK and the Go example
	$(GO) test -race ./pkg/... ./internal/... ./cmd/... ./sdk/... ./examples/...

test-sdk: ## the Python and Java SDKs and examples against the golden files
	cd sdk/python && python3 -I -m unittest discover -s tests
	cd examples/postgres/python && python3 -m unittest test_postgres
	@set -e; out=$$(mktemp -d); \
	javac --release 21 -d $$out/sdk $$(find sdk/java/src/main -name '*.java'); \
	javac --release 21 -cp $$out/sdk -d $$out/sdk-test $$(find sdk/java/src/test -name '*.java'); \
	(cd sdk/java && java -cp $$out/sdk:$$out/sdk-test dev.kstool.pack.sdk.GoldenTest); \
	javac --release 21 -d $$out/ex $$(find sdk/java/src/main -name '*.java') examples/postgres/java/*.java; \
	(cd examples/postgres/java && java -cp $$out/ex PostgresTest); \
	rm -rf $$out

golden: ## rewrite the golden files the SDKs and examples are compared with (after a deliberate change of the format)
	UPDATE_GOLDEN=1 $(GO) test ./sdk ./examples/postgres/go -run 'Golden'

vet: ## go vet of the core
	$(GO) vet ./pkg/... ./internal/... ./cmd/... ./sdk/... ./examples/...

fmt: ## gofmt of the core
	gofmt -l -w pkg internal cmd sdk examples emit secretstore

clean: ## remove bin/
	rm -rf $(BIN)

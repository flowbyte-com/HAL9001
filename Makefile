.PHONY: build test vet fmt lint run clean legacy-build legacy-test help

BINARY := hal9001
TAGS   :=

## build: compile the HAL9001 application.
build:
	go build ./...

## build-binary: compile the hal9001 binary into ./build/.
build-binary:
	mkdir -p build
	go build -o build/$(BINARY) ./cmd/hal9001

## test: run the full test suite.
test:
	go test -count=1 ./...

## vet: run go vet.
vet:
	go vet ./...

## fmt: check formatting (fails on drift).
fmt:
	gofmt -l cmd internal
	test -z "$$(gofmt -l cmd internal)"

## lint: vet plus formatting.
lint: vet fmt

## run: launch the TUI (needs a TTY; stores into ~/.hal/data/sessions).
run: build-binary
	./build/$(BINARY)

## clean: remove build output.
clean:
	rm -rf build

## legacy-build: verify the preserved reference still compiles.
legacy-build:
	cd legacy_base && go build -tags custom_channels ./pkg/agent/ ./pkg/config/ ./pkg/promptwork/ ./pkg/tools/ ./pkg/commands/

## legacy-test: run the legacy Prompt Workshop tests as a reference check.
legacy-test:
	cd legacy_base && go test -count=1 ./pkg/promptwork/

## help: list targets.
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //'

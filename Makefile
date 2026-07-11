BINARY_NAME   := opssweep
MODULE        := github.com/anirudh/opssweep
CMD_PATH      := ./cmd/opssweep
BUILD_DIR     := ./bin
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS       := -ldflags "-X $(MODULE)/cmd/opssweep/commands.version=$(VERSION) -s -w"

.PHONY: all build test lint clean update-pricing generate-mocks help

## all: build the binary (default target)
all: build

## build: compile the opssweep binary into ./bin/
build:
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_PATH)
	@echo "Built: $(BUILD_DIR)/$(BINARY_NAME) ($(VERSION))"

## run: build and run with args (usage: make run ARGS="scan --days 30")
run: build
	$(BUILD_DIR)/$(BINARY_NAME) $(ARGS)

## test: run all unit tests
test:
	go test ./... -v -race -count=1

## test-short: run tests without race detector (faster for CI pre-check)
test-short:
	go test ./... -count=1

## lint: run golangci-lint (must be installed: https://golangci-lint.run/usage/install/)
lint:
	golangci-lint run ./...

## vet: run go vet
vet:
	go vet ./...

## fmt: format all Go source files
fmt:
	gofmt -w .

## tidy: tidy and verify go.mod / go.sum
tidy:
	go mod tidy
	go mod verify

## clean: remove build artefacts
clean:
	rm -rf $(BUILD_DIR)

## update-pricing: fetch the latest AWS on-demand pricing snapshot and embed it
## Requires Python 3 and the boto3 library (pip install boto3).
## The script writes a trimmed JSON snapshot to internal/pricing/data/prices.json.
update-pricing:
	@echo "Fetching AWS pricing snapshot..."
	@python3 scripts/update_pricing.py
	@echo "Pricing data updated at internal/pricing/data/prices.json"

## generate-mocks: regenerate AWS SDK mocks via mockery
## Requires mockery: go install github.com/vektra/mockery/v2@latest
generate-mocks:
	mockery --all --dir internal/discovery --output internal/discovery/mocks --outpkg mocks
	mockery --all --dir internal/storage   --output internal/storage/mocks   --outpkg mocks

## install: install the binary to $GOPATH/bin
install:
	go install $(LDFLAGS) $(CMD_PATH)

## help: print this help message
help:
	@grep -E '^## ' Makefile | sed 's/^## //'

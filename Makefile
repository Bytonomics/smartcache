# smartcache Makefile
# Run `make help` for available targets

SHELL := /bin/bash

.PHONY: help build build-check test test-run unit-test-coverage vet fmt fmt-check lint deps check clean pre-commit

.DEFAULT_GOAL := help

# ============================================
# HELP
# ============================================

help: ## Show this help message
	@echo "smartcache - generic type-safe read-through / delete-on-write Redis cache"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "\033[1mTest Runner Examples (make test-run):\033[0m"
	@echo "  make test-run                    # All tests"
	@echo "  make test-run RUN=TestMyFunction # Single test by name"
	@echo "  make test-run PKG=./memstore/... # Tests in a specific package"
	@echo "  make test-run TIMEOUT=5m         # Custom timeout"

# ============================================
# FLEXIBLE TEST RUNNER
# ============================================

RUN ?=
PKG ?= ./...
TIMEOUT ?= 5m

GO_TEST_FLAGS := -v -race -count=1

test-run: ## Flexible test runner (use RUN=TestName PKG=./path TIMEOUT=5m)
	@echo "═══════════════════════════════════════════════════════"
	@echo "Test Run: $$(date)"
	@echo "═══════════════════════════════════════════════════════"
	@echo "Config:"
	@echo "  RUN:     $(if $(RUN),$(RUN),(all tests))"
	@echo "  PKG:     $(PKG)"
	@echo "  TIMEOUT: $(TIMEOUT)"
	@echo "═══════════════════════════════════════════════════════"
	@echo ""
	@go test $(GO_TEST_FLAGS) \
		$(if $(RUN),-run=$(RUN),) \
		-timeout $(TIMEOUT) \
		$(PKG) 2>&1 | tee /tmp/smartcache_test_output.log; \
		EXIT_CODE=$${PIPESTATUS[0]}; \
		echo ""; \
		echo "═══════════════════════════════════════════════════════"; \
		echo "Exit code: $$EXIT_CODE"; \
		echo "Output saved to: /tmp/smartcache_test_output.log"; \
		echo "═══════════════════════════════════════════════════════"; \
		exit $$EXIT_CODE

# ============================================
# BUILD & QUALITY
# ============================================

build: ## Build the module
	@echo "Building..."
	go build -v ./...

build-check: ## Build-check production AND test code (compile only, no run)
	@echo "Build-checking production code..."
	go build ./...
	@echo "Build-checking test code (type-check without running)..."
	go vet ./...

vet: ## Run go vet
	@echo "Running go vet..."
	go vet ./...

fmt: ## Format code with goimports + gofmt
	@echo "Formatting code..."
	@which goimports > /dev/null || (echo "goimports not installed. Install with: go install golang.org/x/tools/cmd/goimports@latest" && exit 1)
	goimports -w -local github.com/Bytonomics/smartcache .
	gofmt -s -w .

fmt-check: ## Check formatting without writing (fails if unformatted files exist)
	@echo "Checking formatting..."
	@UNFORMATTED=$$(gofmt -l .); \
	if [ -n "$$UNFORMATTED" ]; then \
		echo "Unformatted files:"; \
		echo "$$UNFORMATTED"; \
		exit 1; \
	fi
	@echo "All files formatted"

# Pinned to match the version other Go submodules in this repo install (see
# smritea-oss/pedantigo/validator/Makefile) — invoked by direct path, not via
# PATH resolution, so a stale golangci-lint elsewhere on PATH can never
# silently shadow it and let a local `make lint` pass on issues CI would catch.
GOLANGCI_LINT_VERSION := v2.12.2
GOLANGCI_LINT_BIN := $(shell go env GOPATH)/bin/golangci-lint

$(GOLANGCI_LINT_BIN): ## Install the pinned golangci-lint version if missing or outdated
	@if [ ! -x "$(GOLANGCI_LINT_BIN)" ] || ! "$(GOLANGCI_LINT_BIN)" version 2>/dev/null | grep -q "version $(GOLANGCI_LINT_VERSION:v%=%) "; then \
		echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)..."; \
		go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
	fi

lint: $(GOLANGCI_LINT_BIN) ## Run golangci-lint
	@echo "Running golangci-lint..."
	"$(GOLANGCI_LINT_BIN)" run ./...

deps: ## Download and tidy Go dependencies
	@echo "Tidying dependencies..."
	go mod download
	go mod tidy
	@echo "Dependencies up to date"

test: ## Run all tests (parallel with race detection)
	@echo "Running tests (race detection enabled)..."
	go test -race -count=1 ./...

unit-test-coverage: ## Run unit tests with coverage and threshold enforcement (-race, for pre-commit)
	@echo "Running unit tests with coverage (race detection enabled)..."
	go test -race -count=1 -cover -coverprofile=/tmp/smartcache-coverage.out ./...
	go tool cover -html=/tmp/smartcache-coverage.out -o /tmp/smartcache-coverage.html
	@echo "Coverage report: /tmp/smartcache-coverage.html"
	@echo "Checking coverage threshold..."
	@COVERAGE=$$(go tool cover -func=/tmp/smartcache-coverage.out | grep total | awk '{print $$3}' | sed 's/%//'); \
	THRESHOLD=85.0; \
	echo "Current coverage: $${COVERAGE}%"; \
	echo "Target coverage: $${THRESHOLD}%"; \
	if awk -v cov="$$COVERAGE" -v thresh="$$THRESHOLD" 'BEGIN {exit !(cov >= thresh)}'; then \
		echo "Coverage check passed"; \
	else \
		echo "Coverage below target: $${COVERAGE}% < $${THRESHOLD}%"; \
		exit 1; \
	fi

check: fmt-check vet lint unit-test-coverage ## Run fmt-check + vet + lint + unit tests with coverage

pre-commit: fmt-check vet lint build-check unit-test-coverage ## Quick check before commit

clean: ## Clean build artifacts and test cache
	@echo "Cleaning..."
	go clean -testcache
	rm -f /tmp/smartcache-coverage.out /tmp/smartcache-coverage.html

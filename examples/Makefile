# smartcache/examples Makefile
# Run `make help` for available targets

SHELL := /bin/bash

.PHONY: help build-check fmt fmt-check deps run-memstore run-redisstore clean

.DEFAULT_GOAL := help

help: ## Show this help message
	@echo "smartcache/examples - runnable demos of the smartcache library"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "run-redisstore needs a local Redis: docker run --rm -p 6379:6379 redis:7"

deps: ## Download and tidy Go dependencies (isolated from the root module)
	@echo "Tidying dependencies..."
	go mod download
	go mod tidy
	@echo "Dependencies up to date"

build-check: ## Build-check all examples (compile only, no run)
	@echo "Build-checking examples..."
	go build ./...

fmt: ## Format code with gofmt
	@echo "Formatting code..."
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

run-memstore: ## Run the memstore example (no infra required)
	go run ./memstore

run-redisstore: ## Run the redisstore example (needs a local Redis on REDIS_ADDR, default localhost:6379)
	go run ./redisstore

clean: ## Clean build artifacts
	@echo "Cleaning..."
	go clean

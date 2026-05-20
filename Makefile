SHELL := /bin/bash

# Versioning: take a git tag if present, else short sha; built timestamp in UTC.
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
    -X main.Version=$(VERSION) \
    -X main.Commit=$(COMMIT) \
    -X main.BuildTime=$(BUILD_TIME)

# Paths
BACKEND_DIR  := backend
FRONTEND_DIR := frontend
DIST_DIR     := $(BACKEND_DIR)/internal/web/dist
BIN          := $(BACKEND_DIR)/oto-server

.PHONY: help all build build-backend build-frontend run dev test vet lint fmt tidy clean docker docker-run version smoke

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

all: build-frontend build-backend ## Build frontend then backend with version info baked in

build: all ## Alias for `all`

build-frontend: ## pnpm install + pnpm build (writes into backend/internal/web/dist)
	cd $(FRONTEND_DIR) && pnpm install && pnpm build

# Build tag `sqlite_fts5` switches on FTS5 in mattn/go-sqlite3 — the long-term
# memory layer (L1 BM25 over `memories_fts`) requires it. Without the tag the
# server will boot but `EnsureSchema` returns `no such module: fts5`.
GO_TAGS := sqlite_fts5

build-backend: ## go build the server binary with version info
	cd $(BACKEND_DIR) && go build -trimpath -tags='$(GO_TAGS)' -ldflags="$(LDFLAGS)" -o oto-server ./cmd/server

run: build-backend ## Run the server (no frontend rebuild)
	$(BIN) --config $(BACKEND_DIR)/config.yaml

dev: ## Run backend with `go run` (no embed) — for use alongside `pnpm dev`
	cd $(BACKEND_DIR) && go run -tags='$(GO_TAGS)' ./cmd/server

test: ## go test ./... (race detector on)
	cd $(BACKEND_DIR) && go test -race -count=1 -tags='$(GO_TAGS)' ./...

vet: ## go vet ./...
	cd $(BACKEND_DIR) && go vet -tags='$(GO_TAGS)' ./...

lint: ## golangci-lint run ./... (install first: https://golangci-lint.run)
	cd $(BACKEND_DIR) && golangci-lint run ./...

fmt: ## gofmt -s -w
	cd $(BACKEND_DIR) && gofmt -s -w .

tidy: ## go mod tidy
	cd $(BACKEND_DIR) && go mod tidy

clean: ## Remove built binaries and embedded dist
	rm -f $(BIN)
	rm -rf $(DIST_DIR)/assets

docker: ## Build a docker image (use VERSION=v0.1.0 to override the tag)
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t opentheone:$(VERSION) .

docker-run: docker ## Build then run via docker compose
	docker compose up -d

version: ## Print the values that will be baked into the binary
	@echo VERSION=$(VERSION)
	@echo COMMIT=$(COMMIT)
	@echo BUILD_TIME=$(BUILD_TIME)

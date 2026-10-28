# mdc-lite build entry points. `make` on its own runs the fast gate that CI runs
# on every pull request: vet + unit tests + lint + frontend build/typecheck.

BINARY := bin/mdc
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/2017fighting/mdc-lite/internal/cli.Version=$(VERSION)

# CGO off: the binary is static, so a container needs no libc and no build tools.
export CGO_ENABLED := 0

WEB_DIR := web
WEB_DIST := internal/web/dist

.PHONY: all
all: build

## build: compile the single static binary (frontend first, so its output is embedded)
.PHONY: build
build: web
	@mkdir -p bin
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/mdc

## test: the whole Go test suite
.PHONY: test
test:
	go test ./...

## vet: go vet over every package
.PHONY: vet
vet:
	go vet ./...

## lint: golangci-lint over every package, config verified first
.PHONY: lint
lint:
	golangci-lint config verify
	golangci-lint run ./...

## web: build the frontend into the directory the binary embeds
.PHONY: web
web:
	@if [ -f $(WEB_DIR)/package.json ]; then \
		cd $(WEB_DIR) && npm ci --no-audit --no-fund && npm run build; \
	else \
		echo "web/: no package.json yet, keeping the embedded placeholder page"; \
	fi

## typecheck: typecheck the frontend
.PHONY: typecheck
typecheck:
	@if [ -f $(WEB_DIR)/package.json ]; then \
		cd $(WEB_DIR) && npm ci --no-audit --no-fund && npm run typecheck; \
	else \
		echo "web/: no package.json yet, nothing to typecheck"; \
	fi

## check: everything a pull request must pass
.PHONY: check
check: vet test lint typecheck

## run: run the server against examples/
.PHONY: run
run:
	go run ./cmd/mdc serve --config examples/config.yaml --pipelines-dir examples/pipelines --db-path $(CURDIR)/.scratch/mdc.db

## config-check: print the effective configuration of examples/
.PHONY: config-check
config-check:
	go run ./cmd/mdc config check --config examples/config.yaml --pipelines-dir examples/pipelines

## clean: drop the binary and the built frontend, restoring the committed placeholder
.PHONY: clean
clean:
	rm -rf bin
	git checkout -- $(WEB_DIST) 2>/dev/null || true
	rm -rf $(WEB_DIST)/assets

## help: list the targets
.PHONY: help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'

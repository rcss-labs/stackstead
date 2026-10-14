# Stackstead build entry point. Only `ui`, `build`, `release`, `ui-check`,
# `ui-test` and `check` need Node; `test` and `e2e` do not build the UI.
.PHONY: help ui ui-check ui-test build build-go release test check e2e clean

BIN := bin

# Build identity stamped into the binary (internal/version). Plan 7's release
# targets reuse LDFLAGS; these names appear nowhere else.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_PKG := github.com/rcss-labs/stackstead/internal/version
LDFLAGS := -s -w -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).Date=$(DATE)

help: ## List targets
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

# Installs dependencies only when the lockfile is newer than the last install, so
# `make check` (ui-check + ui-test) runs `npm ci` at most once.
web/node_modules/.package-lock.json: web/package-lock.json
	cd web && npm ci

ui: web/node_modules/.package-lock.json ## Build the web UI into web/dist (needs Node)
	cd web && npm run build

ui-check: web/node_modules/.package-lock.json ## Type-check the web UI (svelte-check)
	cd web && npm run check

ui-test: web/node_modules/.package-lock.json ## Run the web UI unit tests (vitest)
	cd web && npm run test

build: ui ## Build the UI, then the static binary
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN)/stackstead ./cmd/stackstead

build-go: ## Build the host binary without the UI step (for CLI work, no Node needed)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN)/stackstead ./cmd/stackstead

release: ui ## Build linux/amd64 and linux/arm64 binaries
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BIN)/stackstead-linux-amd64 ./cmd/stackstead
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BIN)/stackstead-linux-arm64 ./cmd/stackstead

test: ## Go tests (no Node needed)
	go test -race -timeout 300s ./...

check: ui-check ui-test ## Vet, gofmt, Go tests, UI checks and tests
	go vet ./...
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	$(MAKE) test

e2e: ## Smoke test against the real binary (needs a built UI)
	go test -tags e2e -count=1 -timeout 600s -run TestE2ESmoke -v ./cmd/stackstead/

clean: ## Remove build output
	rm -rf $(BIN)
	find web/dist -mindepth 1 ! -name .gitignore -delete 2>/dev/null || true

# Stackstead build entry point. Only `ui`, `build`, `release`, `ui-check`,
# `ui-test` and `check` need Node; `test` and `e2e` do not build the UI.
.PHONY: help ui ui-check ui-test build release verify-release dist-clean test check e2e clean

BIN := bin

help: ## List targets
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

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
	CGO_ENABLED=0 go build -o $(BIN)/stackstead ./cmd/stackstead

# --- Release artifacts (Plan 7). VERSION/COMMIT/DATE/LDFLAGS belong to Plan 6;
# RELEASE_VERSION is recursive (=) so it sees Plan 6's VERSION wherever it is defined.
RELEASE_VERSION = $(or $(VERSION),$(shell git describe --tags --always --dirty))
DIST ?= dist
RELEASE_TARGETS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
# Commit time for archive mtimes. Archives are byte-identical across rebuilds of the
# same commit only if the binaries are too (needs DATE from the commit time, Plan 6).
SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct)
# Empty = unsigned (current policy). Set to an ssh private key to also write SHA256SUMS.sig.
SIGNING_KEY ?=
# allowed_signers file; verify-release checks the signature only when this is set.
SIGNERS ?=
SIGN_NAMESPACE := stackstead-release
SIGNER_IDENTITY ?= release@stackstead
MKRELEASE_EXTRAS = \
	-extra install.sh=install.sh \
	-extra config.example.yaml=deploy/config.example.yaml \
	-extra README.md=README.md \
	-extra LICENSE=LICENSE \
	-extra linux:stackstead.service=deploy/systemd/stackstead.service \
	-extra darwin:launchagent.plist.in=deploy/macos/launchagent.plist.in

release: ui ## Build release tarballs + SHA256SUMS in dist/ (run after packages/pkg; unsigned unless SIGNING_KEY is set)
	mkdir -p $(DIST)
	cp install.sh $(DIST)/install.sh
	chmod 755 $(DIST)/install.sh
	@for t in $(RELEASE_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/stackstead-$$os-$$arch ./cmd/stackstead || exit 1; \
	done
	SOURCE_DATE_EPOCH=$(SOURCE_DATE_EPOCH) go run ./tools/mkrelease -version '$(RELEASE_VERSION)' -dist $(DIST) -bin $(BIN) \
		-targets $(subst $(space),$(comma),$(strip $(RELEASE_TARGETS))) $(MKRELEASE_EXTRAS) \
		$(if $(filter 1,$(ALLOW_DEV_RELEASE)),-allow-dev)
ifneq ($(strip $(SIGNING_KEY)),)
	rm -f $(DIST)/SHA256SUMS.sig
	ssh-keygen -Y sign -f '$(SIGNING_KEY)' -n $(SIGN_NAMESPACE) $(DIST)/SHA256SUMS
else
	@rm -f $(DIST)/SHA256SUMS.sig  # a stale signature would not match the regenerated sums
endif

verify-release: ## Verify dist/SHA256SUMS (and SHA256SUMS.sig when SIGNERS=<allowed_signers> is given)
	@cd $(DIST) && test -s SHA256SUMS || { echo "$(DIST)/SHA256SUMS is missing or empty"; exit 1; }; \
	status=0; \
	if command -v sha256sum >/dev/null 2>&1; then sha256sum -c SHA256SUMS || status=1; \
	else \
		while read -r sum name; do \
			got=$$(shasum -a 256 "$$name" | awk '{print $$1}'); \
			if [ "$$got" = "$$sum" ]; then echo "$$name: OK"; else echo "$$name: FAILED"; status=1; fi; \
		done < SHA256SUMS; \
	fi; \
	for f in *; do \
		case "$$f" in SHA256SUMS|SHA256SUMS.sig) continue;; esac; \
		[ -f "$$f" ] || continue; \
		awk -v f="$$f" 'substr($$0,67)==f{found=1} END{exit !found}' SHA256SUMS || { echo "$$f: in $(DIST) but not listed in SHA256SUMS"; status=1; }; \
	done; \
	exit $$status
ifneq ($(strip $(SIGNERS)),)
	@test -f $(DIST)/SHA256SUMS.sig || { echo "SIGNERS given but $(DIST)/SHA256SUMS.sig is missing"; exit 1; }
	ssh-keygen -Y verify -f '$(SIGNERS)' -I '$(SIGNER_IDENTITY)' -n $(SIGN_NAMESPACE) -s $(DIST)/SHA256SUMS.sig < $(DIST)/SHA256SUMS
endif

dist-clean: ## Remove release output (dist/)
	rm -rf $(DIST)

comma := ,
space := $(null) $(null)

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

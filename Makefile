# Stackstead build entry point. Only `ui`, `build`, `release`, `ui-check`,
# `ui-test` and `check` need Node; `test` and `e2e` do not build the UI.
.PHONY: help ui ui-check ui-test build release verify-release dist-clean packages package-lint lint-image print-lint-image test check e2e clean

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

# --- Packages (deb + rpm via nfpm). NFPM_VERSION is pinned to an exact release (never
# latest) so builds are reproducible; `go run pkg@version` is verified by the Go sumdb and
# needs no go.mod change. Override NFPM= to use a local nfpm binary.
NFPM_VERSION := v2.43.1
NFPM ?= go run github.com/goreleaser/nfpm/v2/cmd/nfpm@$(NFPM_VERSION)
PKG_ARCHES ?= amd64 arm64
# Version fields come from mkrelease so they match the archives: ver<TAB>pkgver<TAB>prerelease.
PKG_VERSION_INFO = $(shell go run ./tools/mkrelease -version '$(RELEASE_VERSION)' $(if $(filter 1,$(ALLOW_DEV_RELEASE)),-allow-dev) -print-version)
LINT_DOCKERFILE := test/installer/images/lint.Dockerfile
LINT_IMAGE = stackstead-itest/lint:$(shell shasum -a 256 $(LINT_DOCKERFILE) | cut -c1-12)

packages: ## Build deb + rpm for linux amd64/arm64 into dist/ (run before release so SHA256SUMS covers them)
	mkdir -p $(DIST) $(BIN)
	@set -e; info='$(PKG_VERSION_INFO)'; \
	[ -n "$$info" ] || { echo "could not determine the package version"; exit 1; }; \
	pkgver=$$(printf '%s' "$$info" | cut -f2); pre=$$(printf '%s' "$$info" | cut -f3); \
	for arch in $(PKG_ARCHES); do \
		case $$arch in amd64) rpmarch=x86_64;; arm64) rpmarch=aarch64;; *) echo "unsupported arch $$arch"; exit 1;; esac; \
		echo "build linux/$$arch"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $(BIN)/stackstead-linux-$$arch ./cmd/stackstead; \
		for fmt in deb rpm; do \
			if [ $$fmt = deb ]; then out=$(DIST)/stackstead_$${pkgver}_$$arch.deb; else out=$(DIST)/stackstead-$${pkgver}-1.$$rpmarch.rpm; fi; \
			ARCH=$$arch VERSION=$${pkgver%%~*} PRERELEASE=$$pre BIN=$(abspath $(BIN))/stackstead-linux-$$arch \
				$(NFPM) package --config deploy/nfpm.yaml --packager $$fmt --target $(abspath $(DIST))/$$(basename $$out); \
		done; \
	done

lint-image: ## Build the package lint image (lintian + rpmlint)
	docker build -q -t $(LINT_IMAGE) -f $(LINT_DOCKERFILE) test/installer/images >/dev/null

print-lint-image:
	@echo $(LINT_IMAGE)

# One labelled, uniquely named container; --rm removes it, and nothing else is touched.
package-lint: lint-image ## Run lintian and rpmlint over every package in dist/ in a container
	@test -n "$$(ls $(DIST)/*.deb $(DIST)/*.rpm 2>/dev/null)" || { echo "no packages in $(DIST): run make packages"; exit 1; }
	docker run --rm --name stackstead-itest-lint-$$$$ --label stackstead.test=lint \
		-v "$(abspath $(DIST))":/dist:ro -v "$(abspath deploy/pkg)":/pkg:ro $(LINT_IMAGE) \
		sh -c 'set -eu; lintian --fail-on error,warning --tag-display-limit 0 /dist/*.deb; rpmlint -r /pkg/rpmlintrc /dist/*.rpm'

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

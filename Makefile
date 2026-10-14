# Stackstead build entry point. Only `ui`, `build`, `release`, `ui-check`,
# `ui-test` and `check` need Node; `test` and `e2e` do not build the UI.
.PHONY: help ui ui-check ui-test build build-go binaries release dist verify-release dist-clean packages pkg pkg-skip package-lint lint-image print-lint-image test check e2e clean

BIN := bin

# Build identity stamped into the binary (internal/version). Plan 7's release
# targets reuse LDFLAGS; these names appear nowhere else.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
# Commit time (UTC), not build time, so rebuilding a commit is reproducible.
DATE    ?= $(shell TZ=UTC git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)
VERSION_PKG := github.com/rcss-labs/stackstead/internal/version
LDFLAGS := -s -w -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).Date=$(DATE)

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
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN)/stackstead ./cmd/stackstead

build-go: ## Build the host binary without the UI step (for CLI work, no Node needed)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN)/stackstead ./cmd/stackstead

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

# One build of the release binaries, shared by the tarballs (release) and the packages
# (packages). Recursive (=) so Plan 6's LDFLAGS is seen wherever it is defined. SKIP_UI=1
# leaves out the Node-based UI build (used by the packaging tests; never for a real release).
RELEASE_LDFLAGS = -s -w $(LDFLAGS)

binaries: $(if $(filter 1,$(SKIP_UI)),,ui) ## Build every RELEASE_TARGETS binary into bin/ (shared by release and packages)
	@mkdir -p $(BIN)
	@for t in $(RELEASE_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		echo "build $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(RELEASE_LDFLAGS)" -o $(BIN)/stackstead-$$os-$$arch ./cmd/stackstead || exit 1; \
	done

# Order matters: binaries, then packages, then the macOS pkg (darwin hosts only), then release
# (which writes SHA256SUMS over everything in dist/).
UNAME_S ?= $(shell uname -s)
dist: binaries packages $(if $(filter Darwin,$(UNAME_S)),pkg,pkg-skip) release ## Build binaries, deb/rpm, macOS pkg (on macOS) and release archives with checksums (full release build)
.NOTPARALLEL: dist

release: binaries ## Build release tarballs + SHA256SUMS in dist/ (run after packages/pkg; unsigned unless SIGNING_KEY is set)
	mkdir -p $(DIST)
	cp install.sh $(DIST)/install.sh
	chmod 755 $(DIST)/install.sh
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
# Version fields come from `mkrelease -print-version` (ver<TAB>pkgver<TAB>prerelease), the same
# normalisation as the archives; it refuses dev versions unless ALLOW_DEV_RELEASE=1.
LINT_DOCKERFILE := test/installer/images/lint.Dockerfile
LINT_IMAGE = stackstead-itest/lint:$(shell (sha256sum $(LINT_DOCKERFILE) 2>/dev/null || shasum -a 256 $(LINT_DOCKERFILE)) | cut -c1-12)

packages: ## Build deb + rpm into dist/ from the binaries made by `make binaries` (run before release so SHA256SUMS covers them)
	@set -e; \
	info=$$(go run ./tools/mkrelease -version '$(RELEASE_VERSION)' $(if $(filter 1,$(ALLOW_DEV_RELEASE)),-allow-dev) -print-version) \
		|| { echo "cannot build packages: $(RELEASE_VERSION) is a dev version (tag the commit, or set ALLOW_DEV_RELEASE=1)"; exit 1; }; \
	for arch in $(PKG_ARCHES); do \
		[ -f "$(BIN)/stackstead-linux-$$arch" ] || { echo "missing binary $(BIN)/stackstead-linux-$$arch: run make binaries first"; exit 1; }; \
	done; \
	mkdir -p $(DIST); \
	pkgver=$$(printf '%s' "$$info" | cut -f2); pre=$$(printf '%s' "$$info" | cut -f3); \
	ver=$$(printf '%s' "$$info" | cut -f1); \
	$(CHECK_BIN_VERSION); for arch in $(PKG_ARCHES); do chk "$(BIN)/stackstead-linux-$$arch" "$$ver"; done; \
	for arch in $(PKG_ARCHES); do \
		case $$arch in amd64) rpmarch=x86_64;; arm64) rpmarch=aarch64;; *) echo "unsupported arch $$arch"; exit 1;; esac; \
		for fmt in deb rpm; do \
			if [ $$fmt = deb ]; then out=stackstead_$${pkgver}_$$arch.deb; else out=stackstead-$${pkgver}-1.$$rpmarch.rpm; fi; \
			ARCH=$$arch VERSION=$${pkgver%%~*} PRERELEASE=$$pre BIN=$(abspath $(BIN))/stackstead-linux-$$arch \
				$(NFPM) package --config deploy/nfpm.yaml --packager $$fmt --target $(abspath $(DIST))/$$out; \
		done; \
	done

lint-image: ## Build the package lint image (lintian + rpmlint)
	docker build -q -t $(LINT_IMAGE) -f $(LINT_DOCKERFILE) test/installer/images >/dev/null

print-lint-image:
	@echo $(LINT_IMAGE)

# One labelled, uniquely named container; --rm removes it, and nothing else is touched.
package-lint: ## Run lintian and rpmlint over the packages in dist/ in a container
	@test -n "$$(ls $(DIST)/*.deb $(DIST)/*.rpm 2>/dev/null)" || { echo "no packages in $(DIST): run make packages"; exit 1; }
	@$(MAKE) --no-print-directory lint-image
	docker run --rm --name stackstead-itest-lint-$$$$ --label stackstead.test=lint \
		-v "$(abspath $(DIST))":/dist:ro -v "$(abspath deploy/pkg)":/pkg:ro $(LINT_IMAGE) \
		sh -c 'set -eu; \
		set -- /dist/*.deb; if [ -e "$$1" ]; then lintian --fail-on error,warning --tag-display-limit 0 "$$@"; fi; \
		set -- /dist/*.rpm; if [ -e "$$1" ]; then rpmlint --strict -c /pkg/rpmlint.toml -r /pkg/rpmlintrc "$$@"; fi'

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
	go test -tags e2e -count=1 -timeout 600s -run TestE2E -v ./cmd/stackstead/

clean: ## Remove build output
	rm -rf $(BIN)
	find web/dist -mindepth 1 ! -name .gitignore -delete 2>/dev/null || true

# --- macOS .pkg (unsigned flow is the deliverable; signing/notarization is optional and
# off by default). Needs a macOS host and the binaries from `make binaries`.
# Nondeterminism: the universal binary and its ad-hoc signature are reproducible for the same
# inputs, but pkgbuild/productbuild stamp creation times into the xar table of contents, so the
# .pkg bytes differ between rebuilds. The SHA256SUMS entry is computed over the file as built
# once; publish that exact file (do not rebuild after `make release`).
PKG_IDENTIFIER := io.github.rcss-labs.stackstead
PKG_SIGN_COUNT = $(words $(if $(strip $(MACOS_APP_IDENTITY)),x) $(if $(strip $(MACOS_INSTALLER_IDENTITY)),x) $(if $(strip $(NOTARY_PROFILE)),x))
MACOS_APP_IDENTITY ?=
MACOS_INSTALLER_IDENTITY ?=
NOTARY_PROFILE ?=

# CHECK_BIN_VERSION defines chk FILE VERSION for a recipe: refuse a stale bin/. A binary this host
# cannot run (another OS or architecture, or an old CLI without version --short) is skipped.
define CHECK_BIN_VERSION
chk() { got=$$("$$1" version --short 2>/dev/null </dev/null) || return 0; \
	case $$got in v$$2|$$2) ;; *) echo "stale binary $$1 reports $$got but the release is $$2: run make binaries"; exit 1;; esac; }
endef

pkg-skip:
	@echo "skipping the macOS .pkg: it can only be built on a macOS host (uname -s is $(UNAME_S))"

pkg: ## Build dist/stackstead_<ver>_darwin.pkg (macOS host; unsigned unless all of MACOS_APP_IDENTITY, MACOS_INSTALLER_IDENTITY, NOTARY_PROFILE are set)
	@set -e; \
	[ "$(UNAME_S)" = Darwin ] || { echo "make pkg builds the macOS package and needs a macOS host (uname -s is $(UNAME_S))"; exit 1; }; \
	case $(PKG_SIGN_COUNT) in 0|3) ;; *) \
		echo "partial signing configuration: set all three of MACOS_APP_IDENTITY, MACOS_INSTALLER_IDENTITY and NOTARY_PROFILE for a signed and notarized pkg, or none of them for the unsigned pkg"; exit 1;; esac; \
	signed=; [ $(PKG_SIGN_COUNT) -eq 3 ] && signed=1; \
	for tool in lipo codesign pkgbuild productbuild; do command -v $$tool >/dev/null || { echo "missing $$tool (install the Xcode command line tools)"; exit 1; }; done; \
	for arch in amd64 arm64; do [ -f "$(BIN)/stackstead-darwin-$$arch" ] || { echo "missing binary $(BIN)/stackstead-darwin-$$arch: run make binaries first"; exit 1; }; done; \
	info=$$(go run ./tools/mkrelease -version '$(RELEASE_VERSION)' $(if $(filter 1,$(ALLOW_DEV_RELEASE)),-allow-dev) -print-version) \
		|| { echo "cannot build the pkg: $(RELEASE_VERSION) is a dev version (tag the commit, or set ALLOW_DEV_RELEASE=1)"; exit 1; }; \
	ver=$$(printf '%s' "$$info" | cut -f1); pkgver=$$(printf '%s' "$$info" | cut -f2); pkgver=$${pkgver%%~*}; \
	$(CHECK_BIN_VERSION); chk "$(BIN)/stackstead-darwin-amd64" "$$ver"; chk "$(BIN)/stackstead-darwin-arm64" "$$ver"; \
	mkdir -p "$(DIST)"; out="$(abspath $(DIST))/stackstead_$${ver}_darwin.pkg"; \
	work=$$(mktemp -d "$${TMPDIR:-/tmp}/stackstead-pkg.XXXXXX"); trap 'rm -rf "$$work"' EXIT; \
	root="$$work/pkgroot"; mkdir -p "$$root/usr/local/bin" "$$root/usr/local/share/stackstead" "$$work/res" "$$work/pkgs"; \
	lipo -create -output "$$root/usr/local/bin/stackstead" "$(BIN)/stackstead-darwin-amd64" "$(BIN)/stackstead-darwin-arm64"; \
	if [ -n "$$signed" ]; then codesign --force --sign "$$MACOS_APP_IDENTITY" --options runtime --timestamp "$$root/usr/local/bin/stackstead"; \
	else codesign --force --sign - "$$root/usr/local/bin/stackstead"; fi; \
	chmod 755 "$$root/usr/local/bin/stackstead"; \
	install -m 755 install.sh "$$root/usr/local/share/stackstead/install.sh"; \
	install -m 644 deploy/macos/launchagent.plist.in deploy/config.example.yaml LICENSE "$$root/usr/local/share/stackstead/"; \
	stamp=$$(TZ=UTC date -r '$(SOURCE_DATE_EPOCH)' +%Y%m%d%H%M.%S); \
	find "$$root" -exec touch -h -t "$$stamp" {} +; \
	xattr -cr "$$root" 2>/dev/null || true; \
	scripts="$$work/scripts"; mkdir -p "$$scripts"; \
	install -m 755 deploy/macos/scripts/postinstall install.sh "$$scripts/"; \
	install -m 644 deploy/macos/launchagent.plist.in deploy/config.example.yaml "$$scripts/"; \
	pkgbuild --root "$$root" --identifier $(PKG_IDENTIFIER) --version "$$pkgver" --install-location / \
		--ownership recommended --scripts "$$scripts" "$$work/pkgs/stackstead-component.pkg" >/dev/null; \
	sed -e "s|@IDENTIFIER@|$(PKG_IDENTIFIER)|g" -e "s|@VERSION@|$$pkgver|g" -e "s|@COMPONENT@|stackstead-component.pkg|g" deploy/macos/distribution.xml.in >"$$work/distribution.xml"; \
	cp deploy/macos/resources/welcome.html deploy/macos/resources/conclusion.html "$$work/res/"; \
	rm -f "$$out"; \
	if [ -n "$$signed" ]; then \
		productbuild --distribution "$$work/distribution.xml" --resources "$$work/res" --package-path "$$work/pkgs" --sign "$$MACOS_INSTALLER_IDENTITY" "$$out" >/dev/null; \
	else \
		productbuild --distribution "$$work/distribution.xml" --resources "$$work/res" --package-path "$$work/pkgs" "$$out" >/dev/null; \
	fi; \
	if [ -n "$$signed" ]; then \
		xcrun notarytool submit "$$out" --keychain-profile "$$NOTARY_PROFILE" --wait; \
		xcrun stapler staple "$$out"; \
		echo "built, signed and notarized $$out"; \
	else \
		echo "built $$out (universal binary, ad-hoc codesigned; the pkg itself is unsigned)"; \
		echo ""; \
		echo "Gatekeeper note (paste into the release notes): this package is not signed with an Apple"; \
		echo "Developer ID. Double-clicking it shows \"cannot be opened because it is from an unidentified"; \
		echo "developer\"; right-click the package and choose Open, or run"; \
		echo "  sudo installer -pkg stackstead_$${ver}_darwin.pkg -target /"; \
		echo "from a terminal. spctl --assess --type install reports \"rejected\" for an unsigned package;"; \
		echo "that is expected. The installed binary runs normally (it is ad-hoc signed, and neither"; \
		echo "installer nor curl sets a quarantine attribute)."; \
	fi

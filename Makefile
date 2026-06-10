.PHONY: ui build test test-ui test-all dist samples-tarball install-samples release clean-dist test-release test-install

# Release version/build metadata. VERSION defaults to the current git tag with a
# leading "v" stripped (tag v1.2.3 -> 1.2.3), falling back to the short SHA when
# untagged. Override on the command line: `make dist VERSION=0.0.0-test`.
# DATE uses the commit date (not wall-clock) so two builds of the same commit are
# byte-identical (reproducible builds).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    ?= $(shell git show -s --format=%cI HEAD 2>/dev/null)

# The os/arch matrix for release artifacts (6 targets). Pure-Go (CGO_ENABLED=0)
# cross-compiles every target from a single host — no per-OS runner needed.
DIST_PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

# Reproducible build flags: -trimpath strips local paths; -buildid= zeroes the
# build id; -s -w drop symbol/debug tables; -X stamps the version vars consumed
# by `arbitron version`.
DIST_LDFLAGS = -s -w -buildid= -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# Rebuild the dashboard and sync it into the embedded dir that the Go binary
# serves (adapter/dashboard/dist, via //go:embed dist/*). Run this after
# changing anything under dashboard/ so a host `go build`/`go run` serves the
# current UI. The Docker image builds the dashboard itself, so this is only for
# local (non-Docker) runs — but commit the refreshed adapter/dashboard/dist so
# the checked-in artifact does not drift.
ui:
	cd dashboard && npm ci && npm run build
	rm -rf adapter/dashboard/dist
	cp -r dashboard/dist adapter/dashboard/dist

# Build the server binary (embeds whatever is in adapter/dashboard/dist).
build:
	CGO_ENABLED=0 go build -o arbitron ./cmd/arbitron

# Run the Go test suite (the project's primary gate). Go-only — it does NOT
# cover the dashboard (Phase 3) surface; use `test-all` for the full gate.
test:
	go test ./...

# Run the dashboard test suite: TypeScript type-check + Vitest. Not part of
# `test`; kept separate because it needs Node, not Go.
test-ui:
	cd dashboard && npx tsc --noEmit && npx vitest run

# Full Definition-of-Done gate: the Go suite AND the dashboard suite. "Full
# test suite green" means this target passes, not just `test`.
test-all: test test-ui

# Package the bundled sample workflows into a release tarball alongside the
# binaries, so the installer can fetch + verify + extract them.
samples-tarball:
	@mkdir -p dist
	tar -czf dist/arbitron_samples_$(VERSION).tar.gz -C samples --exclude='*_test.go' .
	@echo "wrote dist/arbitron_samples_$(VERSION).tar.gz"

# Copy the bundled sample workflows into a local sample source dir for a manual
# or dev install. Override DEST to target a different samples dir (e.g. a shared
# install's <data>/samples). The binary seeds from this dir on boot when
# samples.sourcedir points at it.
DEST ?= $(HOME)/.arbitron/samples
install-samples:
	@mkdir -p $(DEST)
	cp -R samples/pr-review samples/feature-pipeline $(DEST)/
	@echo "installed samples into $(DEST)"

# Cross-compile the 6 release binaries named arbitron_<version>_<os>_<arch>[.exe]
# into dist/, then emit dist/SHA256SUMS with basename-only entries (so
# `sha256sum -c SHA256SUMS` works from inside dist/). The artifact is the raw
# binary (no tar/zip). This target builds from the CURRENT adapter/dashboard/dist
# — CI runs `make ui` first to rebuild the SPA from source before calling it.
dist: clean-dist samples-tarball
	@mkdir -p dist
	@for platform in $(DIST_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=; [ "$$os" = "windows" ] && ext=.exe; \
		out=dist/arbitron_$(VERSION)_$${os}_$${arch}$$ext; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags "$(DIST_LDFLAGS)" -o $$out ./cmd/arbitron || exit 1; \
	done
	@cd dist && (sha256sum arbitron_* > SHA256SUMS 2>/dev/null || shasum -a 256 arbitron_* > SHA256SUMS)
	@echo "wrote dist/SHA256SUMS:"; cat dist/SHA256SUMS

# Release = dist plus a detached cosign keyless signature over SHA256SUMS. Signing
# requires an OIDC token (the GitHub Actions workflow provides it). Locally,
# without cosign on PATH, this is a no-op skip with a printed notice so a bare
# `make release` still produces the artifacts.
release: dist
	@if command -v cosign >/dev/null 2>&1; then \
		echo "signing dist/SHA256SUMS with cosign (keyless)"; \
		cosign sign-blob --yes dist/SHA256SUMS \
			--output-signature dist/SHA256SUMS.sig \
			--output-certificate dist/SHA256SUMS.pem; \
	else \
		echo "NOTICE: cosign not found on PATH; skipping signature (CI signs the release)"; \
	fi

# Remove the dist/ output directory.
clean-dist:
	rm -rf dist

# Run the release-artifact bats tests (naming, checksum self-consistency,
# determinism, version round-trip). Requires `bats`.
test-release:
	bats tests/release

# Lint + test the bash installer (install.sh, P1 local path): shellcheck the
# script clean, then run the mocked bats suite under tests/install. Requires
# `shellcheck` and `bats` on PATH.
test-install:
	shellcheck install.sh
	bats tests/install

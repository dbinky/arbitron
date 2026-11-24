.PHONY: ui check-ui-fresh build test test-go-fresh-ui test-ui test-all test-gate test-render test-dashboard-browser test-font-portability test-scripts dist samples-tarball install-samples release clean-dist test-release test-install

# Release version/build metadata. VERSION defaults to the current git tag with a
# leading "v" stripped (tag v1.2.3 -> 1.2.3), falling back to the short SHA when
# untagged. Override on the command line: `make dist VERSION=0.0.0-test`.
# DATE uses the commit date (not wall-clock) so two builds of the same commit are
# byte-identical (reproducible builds).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    ?= $(shell git show -s --format=%cI HEAD 2>/dev/null)

# The os/arch matrix for server artifacts (4 targets). Pure-Go (CGO_ENABLED=0)
# cross-compiles every target from a single host — no per-OS runner needed.
DIST_PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64
# Remote-runner agent targets: macOS only.
RUNNER_PLATFORMS := darwin/amd64 darwin/arm64

# Reproducible build flags: -trimpath strips local paths; -buildid= zeroes the
# build id; -s -w drop symbol/debug tables; -X stamps the version vars consumed
# by `arbitron version`.
DIST_LDFLAGS = -s -w -buildid= -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# Install dashboard packages once per lockfile change. A real file target (not a
# phony step) so `make -j` runs npm ci exactly once, before every Node consumer,
# and skips it entirely when node_modules is already current. npm ci wipes
# node_modules, so the stamp is written only after it succeeds.
DASHBOARD_DEPS := dashboard/node_modules/.stamp
$(DASHBOARD_DEPS): dashboard/package-lock.json dashboard/package.json
	cd dashboard && npm ci
	touch $@

# Rebuild the dashboard and sync it into the embedded dir that the Go binary
# serves (adapter/dashboard/dist, via //go:embed dist/*). Run this after
# changing anything under dashboard/ so a host `go build`/`go run` serves the
# current UI. The Docker image builds the dashboard itself, so this is only for
# local (non-Docker) runs — but commit the refreshed adapter/dashboard/dist so
# the checked-in artifact does not drift.
ui: $(DASHBOARD_DEPS)
	cd dashboard && npm run build
	rm -rf adapter/dashboard/dist
	cp -r dashboard/dist adapter/dashboard/dist

# The committed embedded dist must be exactly what `make ui` builds from source.
# git status --porcelain also reports new untracked files (e.g. a freshly
# fingerprinted chunk), which `git diff` alone would miss. Usable by CI directly.
check-ui-fresh: ui
	@stale="$$(git status --porcelain --untracked-files=all -- adapter/dashboard/dist)"; \
	if [ -n "$$stale" ]; then \
		echo "adapter/dashboard/dist is stale: run 'make ui' and commit the result:"; \
		echo "$$stale"; \
		exit 1; \
	fi

# Build the server binary (embeds whatever is in adapter/dashboard/dist).
build:
	CGO_ENABLED=0 go build -o arbitron ./cmd/arbitron

# Run the Go test suite (the project's primary gate). Go-only: no Node and no
# UI rebuild — it embeds the committed adapter/dashboard/dist. Use `test-all`
# for the full gate.
test:
	go test -timeout 30m ./...

# test-all's Go run: starts only after the embedded dist has been rebuilt and
# proven fresh, so `make -j test-all` never compiles a half-copied dist.
test-go-fresh-ui: check-ui-fresh
	go test -timeout 30m ./...

# Run the dashboard test suite: TypeScript type-check + Vitest. Not part of
# `test`; kept separate because it needs Node, not Go.
test-ui: $(DASHBOARD_DEPS)
	cd dashboard && npx tsc --noEmit && npx vitest run

# Full gate: fresh embedded UI, Go, dashboard, and browser-required rendered review.
# CI runs test-gate on every change and test-font-portability (Docker) in its own
# job when its inputs change; together they are exactly test-all.
test-all: test-gate test-font-portability

test-gate: check-ui-fresh test-go-fresh-ui test-ui test-scripts test-render test-dashboard-browser

# Workflow script tests: shellcheck every listed script (SCRIPT_LINT; each task
# appends its new or changed scripts), bats for *_test.bats, sh for *_test.sh.
SCRIPT_LINT := $(addprefix samples/feature-pipeline/scripts/,intake.sh intake-check.sh adopt-ideate.sh halt.sh) samples/_lib/checkpoint-push.sh samples/_lib/notify-teams.sh samples/_lib/init-workspace.sh samples/_lib/pass-mark.sh samples/_lib/finalize-docs.sh samples/_lib/manifest-read.sh samples/_lib/manifest-append.sh samples/_lib/tamper-gate.sh samples/_lib/tamper-verdict.sh samples/_lib/commit-leftovers.sh samples/_lib/clear-owned-docs.sh samples/ideate/scripts/finalize-ideate.sh samples/plan/scripts/deps-count.sh samples/plan/scripts/list-plans.sh $(addprefix samples/implement/scripts/,count-plan-steps.sh baseline-check.sh persist-baseline.sh import-baseline.sh record-baseline.sh) $(addprefix samples/ralph-loop/scripts/,init-workspace.sh derive-scope.sh record-baseline.sh import-baseline.sh push-and-pr.sh record-pass-commit.sh verify-ralph-seed.sh) $(addprefix samples/harden/scripts/,scope.sh detect.sh mutate.sh mutate-go.sh mutate-csharp.sh sandbox.sh mutate-js.sh mutate-python.sh mutate-java.sh mutate-rust.sh score.sh equivalence.sh report.sh clean-check.sh state-guard.sh)
test-scripts:
	shellcheck $(SCRIPT_LINT)
	bats $(shell find samples -name '*_test.bats')
	@set -e; for t in $(wildcard samples/_lib/*_test.sh samples/*/scripts/*_test.sh); do echo "== $$t"; sh $$t; done

# Browser-only dashboard scripts: shared font classes, wrap widths and per-graph
# marker namespaces in real Chrome. A missing browser is a failure, not a skip.
test-dashboard-browser: $(DASHBOARD_DEPS)
	cd dashboard && ARBITRON_REQUIRE_BROWSER=1 npm run test:arranged-browser

# Bundled fonts must measure and render identically under different system-font
# inventories; the proof runs inside the shipped image, so build it first.
# CI builds the image itself with a layer cache and passes FONT_IMAGE_BUILD=true.
FONT_IMAGE_BUILD ?= docker build -t arbitron:layout-render .
test-font-portability: $(DASHBOARD_DEPS)
	$(FONT_IMAGE_BUILD)
	cd dashboard && npm run test:font-portability

# Browser-required adapter and production-root suites. Rebuild render.html first;
# a missing Chrome/Chromium is a failure, never a skipped release verification.
# Plain `go test ./...` skips every real-browser test, so this is their only run:
# both packages whole, never name-filtered.
# One test: ARBITRON_REQUIRE_BROWSER=1 go test ./cmd/arbitron/ -run '^TestName$' -count=1
test-render: ui
	ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview/ ./cmd/arbitron/ -count=1 -timeout 90m

# Package the bundled sample workflows into a release tarball alongside the
# binaries, so the installer can fetch + verify + extract them.
# The tarball is byte-reproducible for a given commit (its checksum is signed
# into SHA256SUMS): git archive stamps every entry with the commit time and fixed
# ownership (--mtime pins it: given a TREE, git archive would otherwise stamp the
# current time), and gzip -n drops the gzip header's name and timestamp. It packs the
# COMMITTED samples tree, which is exactly what a tagged release ships; the entry
# layout ("./<workflow>/...", no *_test.go) matches what install.sh extracts.
samples-tarball:
	@mkdir -p dist
	git archive --format=tar --prefix=./ --mtime='$(DATE)' HEAD:samples -- ':(exclude)*_test.go' | gzip -n -9 > dist/arbitron_samples_$(VERSION).tar.gz
	@echo "wrote dist/arbitron_samples_$(VERSION).tar.gz"

# Copy the bundled sample workflows into a local sample source dir for a manual
# or dev install. Override DEST to target a different samples dir (e.g. a shared
# install's <data>/samples). The binary seeds from this dir on boot when
# samples.sourcedir points at it.
DEST ?= $(HOME)/.arbitron/samples
install-samples:
	@mkdir -p $(DEST)
	for d in samples/*/; do cp -R "$${d%/}" $(DEST)/; done
	@echo "installed samples into $(DEST)"

# Cross-compile 4 server and 2 darwin runner binaries into dist/, then hash both
# artifact families and the samples tarball with basename-only SHA256SUMS entries.
# The release signature covers these checksums; artifacts are raw binaries.
# Builds from CURRENT adapter/dashboard/dist — CI runs `make ui` first.
dist: clean-dist samples-tarball
	@mkdir -p dist
	@for platform in $(DIST_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		out=dist/arbitron_$(VERSION)_$${os}_$${arch}; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags "$(DIST_LDFLAGS)" -o $$out ./cmd/arbitron || exit 1; \
	done
	@for platform in $(RUNNER_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		out=dist/arbitron-runner_$(VERSION)_$${os}_$${arch}; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags "$(DIST_LDFLAGS)" -o $$out ./cmd/arbitron-runner || exit 1; \
	done
	@cd dist && (sha256sum arbitron_* arbitron-runner_* > SHA256SUMS 2>/dev/null || shasum -a 256 arbitron_* arbitron-runner_* > SHA256SUMS)
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

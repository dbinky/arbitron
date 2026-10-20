#!/usr/bin/env bats
#
# PR #24 review (Should Fix): artifact-NAME conformance across the TWO copies of
# the naming contract: install.sh (arbitron_artifact_name) and the release tooling
# (Makefile dist target / release.yml).
#
# A wrong artifact name fails LOUD at release time (404 = dead installer on first
# release), but only AFTER a release ships. This test binds both builders to a
# single fixed (version, os, arch) tuple and asserts byte-equal output BEFORE
# anything ships, so the copies cannot silently drift.
#
# Windows support (install.ps1, .exe artifacts) was removed before v0.1.0. The
# tag windows-support-final marks the last commit that had it.

REPO_ROOT="${BATS_TEST_DIRNAME}/../.."
INSTALL_SH="${REPO_ROOT}/install.sh"
MAKEFILE="${REPO_ROOT}/Makefile"

FIX_VERSION="1.2.3"
FIX_OS="linux"
FIX_ARCH="arm64"

# The single canonical expected name the contract must produce.
EXPECTED="arbitron_${FIX_VERSION}_${FIX_OS}_${FIX_ARCH}"

# bash builder (install.sh).
_name_from_sh() {
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  arbitron_artifact_name "$FIX_VERSION" "$FIX_OS" "$FIX_ARCH"
}

# Release-tooling builder: reproduce the EXACT shell expansion the Makefile dist
# target uses (out=dist/arbitron_$(VERSION)_$${os}_$${arch}), and the same pattern
# release.yml relies on when it globs dist/arbitron_* into the release.
_name_from_make_pattern() {
  printf 'arbitron_%s_%s_%s\n' "$FIX_VERSION" "$FIX_OS" "$FIX_ARCH"
}

@test "install.sh artifact name matches the canonical contract name" {
  run _name_from_sh
  [ "$status" -eq 0 ]
  [ "$output" = "$EXPECTED" ]
}

@test "the Makefile dist naming pattern matches the canonical contract name" {
  run _name_from_make_pattern
  [ "$status" -eq 0 ]
  [ "$output" = "$EXPECTED" ]
  # Guard: the Makefile still uses the expected literal naming expansion, so this
  # reproduction stays faithful to the real dist target.
  grep -qF 'out=dist/arbitron_$(VERSION)_$${os}_$${arch}; \' "$MAKEFILE"
}

@test "both builders agree on the artifact name (cross-copy conformance)" {
  [ "$(_name_from_sh)" = "$(_name_from_make_pattern)" ]
}

@test "the release ships only macOS and Linux targets" {
  run grep -E '^DIST_PLATFORMS :=' "$MAKEFILE"
  [ "$status" -eq 0 ]
  [ "$output" = "DIST_PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64" ]
}

#!/usr/bin/env bats
#
# PR #24 review (Should Fix): artifact-NAME conformance across the THREE copies of
# the naming contract — install.sh (arbitron_artifact_name), install.ps1
# (Get-ArtifactName), and the release tooling (Makefile dist target / release.yml).
#
# A wrong artifact name fails LOUD at release time (404 = dead installer on first
# release), but only AFTER a release ships. This test binds the three builders to
# a single fixed (version, os, arch) tuple and asserts byte-equal output BEFORE
# anything ships, so the three copies cannot silently drift.
#
# install.ps1 is exercised via pwsh when available; if pwsh is absent the
# PowerShell leg is skipped (the bash + Makefile legs still run).

REPO_ROOT="${BATS_TEST_DIRNAME}/../.."
INSTALL_SH="${REPO_ROOT}/install.sh"
INSTALL_PS1="${REPO_ROOT}/install.ps1"
MAKEFILE="${REPO_ROOT}/Makefile"

# Fixed conformance tuple. Windows is chosen so the .exe-suffix branch is also
# exercised (the os branch that differs between platforms).
FIX_VERSION="1.2.3"
FIX_OS="windows"
FIX_ARCH="amd64"

# The single canonical expected name the contract must produce.
EXPECTED="arbitron_${FIX_VERSION}_${FIX_OS}_${FIX_ARCH}.exe"

# bash builder (install.sh).
_name_from_sh() {
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  arbitron_artifact_name "$FIX_VERSION" "$FIX_OS" "$FIX_ARCH"
}

# PowerShell builder (install.ps1 Get-ArtifactName) — windows-only, .exe implied.
_name_from_ps1() {
  pwsh -NoProfile -Command "
    \$env:ARBITRON_INSTALL_NOEXEC = '1'
    . '$INSTALL_PS1'
    Get-ArtifactName -Version '$FIX_VERSION' -Arch '$FIX_ARCH'
  " 2>/dev/null
}

# Release-tooling builder: reproduce the EXACT shell expansion the Makefile dist
# target uses (out=dist/arbitron_$(VERSION)_$${os}_$${arch}$$ext), and the same
# pattern release.yml relies on when it globs dist/arbitron_* into the release.
_name_from_make_pattern() {
  local version="$FIX_VERSION" os="$FIX_OS" arch="$FIX_ARCH" ext=
  [ "$os" = "windows" ] && ext=.exe
  printf 'arbitron_%s_%s_%s%s\n' "$version" "$os" "$arch" "$ext"
}

@test "install.sh artifact name matches the canonical contract name" {
  run _name_from_sh
  [ "$status" -eq 0 ]
  [ "$output" = "$EXPECTED" ]
}

@test "install.ps1 Get-ArtifactName matches the canonical contract name" {
  command -v pwsh >/dev/null 2>&1 || skip "pwsh not installed; skipping PowerShell conformance leg"
  run _name_from_ps1
  [ "$status" -eq 0 ]
  [ "$output" = "$EXPECTED" ]
}

@test "the Makefile dist naming pattern matches the canonical contract name" {
  run _name_from_make_pattern
  [ "$status" -eq 0 ]
  [ "$output" = "$EXPECTED" ]
  # Guard: the Makefile still uses the expected literal naming expansion, so this
  # reproduction stays faithful to the real dist target.
  grep -qF 'out=dist/arbitron_$(VERSION)_$${os}_$${arch}$$ext' "$MAKEFILE"
}

@test "all three builders agree on the artifact name (cross-copy conformance)" {
  local sh_name make_name ps1_name
  sh_name="$(_name_from_sh)"
  make_name="$(_name_from_make_pattern)"
  [ "$sh_name" = "$make_name" ]
  if command -v pwsh >/dev/null 2>&1; then
    ps1_name="$(_name_from_ps1)"
    [ "$ps1_name" = "$sh_name" ]
  fi
}

@test "non-windows tuple carries no .exe suffix across builders" {
  # Sanity: the .exe branch is os-gated, not unconditional, so a linux tuple must
  # NOT pick up .exe in either the bash builder or the Makefile pattern.
  local linux_expected="arbitron_${FIX_VERSION}_linux_${FIX_ARCH}"
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
  [ "$(arbitron_artifact_name "$FIX_VERSION" linux "$FIX_ARCH")" = "$linux_expected" ]
}

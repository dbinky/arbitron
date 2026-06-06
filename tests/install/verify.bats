#!/usr/bin/env bats
#
# P5 Task 2: the fail-closed checksum-verification contract + version selection.
#
# Proves verify_download fails closed on tampered binary, tampered checksum,
# missing SHA256SUMS, and missing-entry; that the version resolver / URL builders
# are pure (no network) and honour ARBITRON_VERSION pinning; and that there is no
# checksum bypass flag/env.

load test_helper

setup() {
  install_setup_sandbox
  install_isolate_path
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
}

teardown() {
  install_teardown_sandbox
}

# --- fixtures --------------------------------------------------------------

# write_fixture <dir> — create a binary + a correct SHA256SUMS that lists it.
# Echoes the artifact basename.
_make_fixture() {
  local dir="$1" name="arbitron_1.2.3_linux_amd64"
  printf 'REAL-BINARY-BYTES' >"$dir/$name"
  local hash
  hash="$(sha256_tool <"$dir/$name")"
  printf '%s  %s\n' "$hash" "$name" >"$dir/SHA256SUMS"
  printf '%s\n' "$name"
}

# --- happy path ------------------------------------------------------------

@test "verify_download exits 0 for a binary that matches SHA256SUMS" {
  local name
  name="$(_make_fixture "$TEST_SANDBOX")"
  run verify_download "$TEST_SANDBOX/$name" "$name" "$TEST_SANDBOX/SHA256SUMS"
  [ "$status" -eq 0 ]
  # The verified file is left in place (the caller installs it).
  [ -f "$TEST_SANDBOX/$name" ]
}

# --- (1) tampered binary (FAIL CLOSED) -------------------------------------

@test "verify_download FAILS CLOSED on a tampered binary and removes the download" {
  local name
  name="$(_make_fixture "$TEST_SANDBOX")"
  # Corrupt the binary AFTER the sums file was computed.
  printf 'TAMPERED' >>"$TEST_SANDBOX/$name"
  run verify_download "$TEST_SANDBOX/$name" "$name" "$TEST_SANDBOX/SHA256SUMS"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qiE 'mismatch'
  # Fail-closed: the tampered download is deleted so nothing is installed.
  [ ! -e "$TEST_SANDBOX/$name" ]
}

# --- (2) tampered checksum (FAIL CLOSED) -----------------------------------

@test "verify_download FAILS CLOSED when the SHA256SUMS entry is tampered" {
  local name hash
  name="$(_make_fixture "$TEST_SANDBOX")"
  # Flip the leading hex digit of the recorded hash.
  hash="$(sha256_tool <"$TEST_SANDBOX/$name")"
  local flipped
  case "${hash:0:1}" in 0) flipped="1" ;; *) flipped="0" ;; esac
  printf '%s%s  %s\n' "$flipped" "${hash:1}" "$name" >"$TEST_SANDBOX/SHA256SUMS"
  run verify_download "$TEST_SANDBOX/$name" "$name" "$TEST_SANDBOX/SHA256SUMS"
  [ "$status" -ne 0 ]
  [ ! -e "$TEST_SANDBOX/$name" ]
}

# --- (3) missing SHA256SUMS (FAIL CLOSED) ----------------------------------

@test "verify_download FAILS CLOSED when SHA256SUMS is missing" {
  local name
  name="$(_make_fixture "$TEST_SANDBOX")"
  rm -f "$TEST_SANDBOX/SHA256SUMS"
  run verify_download "$TEST_SANDBOX/$name" "$name" "$TEST_SANDBOX/SHA256SUMS"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qiE 'SHA256SUMS'
}

# --- (4) missing entry (FAIL CLOSED) ---------------------------------------

@test "verify_download FAILS CLOSED when the artifact is not listed in SHA256SUMS" {
  local name
  name="$(_make_fixture "$TEST_SANDBOX")"
  # A sums file that lists some OTHER artifact, never our name.
  printf '%s  arbitron_1.2.3_darwin_arm64\n' \
    "$(sha256_tool <"$TEST_SANDBOX/$name")" >"$TEST_SANDBOX/SHA256SUMS"
  run verify_download "$TEST_SANDBOX/$name" "$name" "$TEST_SANDBOX/SHA256SUMS"
  [ "$status" -ne 0 ]
  echo "$output" | grep -qiE 'no checksum'
}

# --- version selection -----------------------------------------------------

@test "arbitron_artifact_name builds arbitron_<version>_<os>_<arch>" {
  run arbitron_artifact_name "1.2.3" linux amd64
  [ "$status" -eq 0 ]
  [ "$output" = "arbitron_1.2.3_linux_amd64" ]
}

@test "arbitron_artifact_name appends .exe for windows" {
  run arbitron_artifact_name "1.2.3" windows amd64
  [ "$output" = "arbitron_1.2.3_windows_amd64.exe" ]
}

@test "arbitron_release_base targets the pinned tag download path" {
  run arbitron_release_base "1.2.3"
  [ "$output" = "https://github.com/dbinky/arbitron/releases/download/v1.2.3" ]
}

@test "ARBITRON_VERSION=1.2.3 pins the version verbatim (no leading v)" {
  export ARBITRON_VERSION="1.2.3"
  run arbitron_resolve_version
  [ "$status" -eq 0 ]
  [ "$output" = "1.2.3" ]
}

@test "ARBITRON_VERSION=v1.2.3 normalizes to 1.2.3" {
  export ARBITRON_VERSION="v1.2.3"
  run arbitron_resolve_version
  [ "$output" = "1.2.3" ]
}

@test "unset ARBITRON_VERSION resolves latest via the releases/latest redirect" {
  unset ARBITRON_VERSION
  # Mock curl: the redirect probe (curl -fsSLI -w %{url_effective}) echoes the
  # effective URL ending in tag/v9.9.9.
  install_fake curl '
    case "$*" in
      *url_effective*) printf "https://github.com/dbinky/arbitron/releases/tag/v9.9.9\n" ;;
      *) ;;
    esac
  '
  run arbitron_resolve_version
  [ "$status" -eq 0 ]
  [ "$output" = "9.9.9" ]
}

@test "the resolved version flows into the artifact name + release base" {
  export ARBITRON_VERSION="v4.5.6"
  local v
  v="$(arbitron_resolve_version)"
  [ "$v" = "4.5.6" ]
  run arbitron_artifact_name "$v" linux amd64
  [ "$output" = "arbitron_4.5.6_linux_amd64" ]
  run arbitron_release_base "$v"
  [ "$output" = "https://github.com/dbinky/arbitron/releases/download/v4.5.6" ]
}

# --- no-bypass -------------------------------------------------------------

@test "there is NO checksum bypass flag/env in install.sh" {
  # No skip-verify / insecure / no-verify path for the CHECKSUM gate. The only
  # optional skip is the signature step.
  run grep -niE 'skip[-_ ]*verif(y|ication)|--insecure|no[-_]verify' "$INSTALL_SH"
  [ "$status" -ne 0 ]
}

@test "verify_download has no env that makes a mismatch return 0" {
  # Even with every plausible bypass env set, a mismatch still fails closed.
  local name
  name="$(_make_fixture "$TEST_SANDBOX")"
  printf 'TAMPERED' >>"$TEST_SANDBOX/$name"
  ARBITRON_SKIP_VERIFY=1 ARBITRON_INSECURE=1 ARBITRON_NO_VERIFY=1 \
    run verify_download "$TEST_SANDBOX/$name" "$name" "$TEST_SANDBOX/SHA256SUMS"
  [ "$status" -ne 0 ]
}

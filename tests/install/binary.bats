#!/usr/bin/env bats
#
# Task 3: arbitron binary download + checksum-verify.

load test_helper

setup() {
  install_setup_sandbox
  install_isolate_path
  ARBITRON_INSTALL_LIB_ONLY=1 source "$INSTALL_SH"
}

teardown() {
  install_teardown_sandbox
}

@test "download_binary builds a URL with the resolved os/arch triple" {
  export ARBITRON_VERSION="1.2.3"
  export ARBITRON_UNAME_S=Darwin ARBITRON_UNAME_M=arm64
  # curl mock: write deterministic bytes to -o dest, and record argv.
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS*) printf "%s  arbitron_1.2.3_darwin_arm64\n" "$(printf BIN | shasum -a 256 | cut -d" " -f1)" > "$dest" ;;
      *)            printf "BIN" > "$dest" ;;
    esac
  '
  run download_binary
  [ "$status" -eq 0 ]
  mock_log_has "arbitron_1.2.3_darwin_arm64"
  [ -x "$(layout_bin)/arbitron" ]
}

@test "download_binary installs mode 0755 on checksum match" {
  export ARBITRON_VERSION="2.0.0"
  export ARBITRON_UNAME_S=Linux ARBITRON_UNAME_M=x86_64
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS*) printf "%s  arbitron_2.0.0_linux_amd64\n" "$(printf BIN | sha256sum | cut -d" " -f1)" > "$dest" ;;
      *)            printf "BIN" > "$dest" ;;
    esac
  '
  run download_binary
  [ "$status" -eq 0 ]
  local perms
  perms="$(file_mode "$(layout_bin)/arbitron")"
  [ "$perms" = "755" ]
}

@test "download_binary works called directly under functrace and leaves no temp dir" {
  # Regression: fetch_binary_to once used a RETURN trap for temp-dir cleanup.
  # bats runs tests with functrace (set -T), under which a RETURN trap is
  # inherited by nested calls (verify_download/verify_signature) and fires
  # BEFORE mv — deleting the temp dir and breaking the install. Calling
  # download_binary directly (NOT via `run`) exercises that functrace context.
  export ARBITRON_VERSION="2.0.0"
  export ARBITRON_UNAME_S=Linux ARBITRON_UNAME_M=x86_64
  # Isolate TMPDIR so the leftover-temp-dir assertion only sees what THIS test
  # creates (fetch_binary_to mktemps under ${TMPDIR:-/tmp}).
  export TMPDIR="$TEST_SANDBOX/bin-tmp"
  mkdir -p "$TMPDIR"
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS*) printf "%s  arbitron_2.0.0_linux_amd64\n" "$(printf BIN | shasum -a 256 | cut -d" " -f1)" > "$dest" ;;
      *)            printf "BIN" > "$dest" ;;
    esac
  '
  download_binary
  [ -x "$(layout_bin)/arbitron" ]
  run find "$TMPDIR" -maxdepth 1 -name 'arbitron-bin.*' -type d
  [ -z "$output" ]
}

@test "download_binary aborts on checksum mismatch and installs nothing" {
  export ARBITRON_VERSION="3.0.0"
  export ARBITRON_UNAME_S=Linux ARBITRON_UNAME_M=x86_64
  install_fake curl '
    for a in "$@"; do prev="$last"; last="$a"; if [ "$prev" = "-o" ]; then dest="$a"; fi; done
    case "$*" in
      *SHA256SUMS*) printf "%s  arbitron_3.0.0_linux_amd64\n" "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef" > "$dest" ;;
      *)            printf "REAL-BYTES" > "$dest" ;;
    esac
  '
  run download_binary
  [ "$status" -ne 0 ]
  [ ! -e "$(layout_bin)/arbitron" ]
}
